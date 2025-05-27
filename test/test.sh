#!/bin/bash

# Enhanced continuous testing script for API endpoints
# Usage: ./continuous-test.sh [duration_in_seconds] [requests_per_second] [output_file]

DURATION=${1:-300}          # Default 5 minutes
RPS=${2:-2}                 # Default 2 requests per second
OUTPUT_FILE=${3:-results.csv} # Default output file
DELAY=$(awk "BEGIN {print 1/$RPS}")

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Initialize results file
echo "timestamp,method,url,response_time,http_code" > "$OUTPUT_FILE"

echo -e "${YELLOW}Starting continuous testing for $DURATION seconds at $RPS requests/second${NC}"
echo -e "Request delay: ${GREEN}$DELAY seconds${NC}"
echo -e "Output file: ${GREEN}$OUTPUT_FILE${NC}"

echo "Starting continuous testing for $DURATION seconds at $RPS requests/second"

# Add hosts to /etc/hosts (requires sudo)
echo "Adding test hosts to /etc/hosts..."
sudo bash -c 'echo "127.0.0.1 api.example.local" >> /etc/hosts'
sudo bash -c 'echo "127.0.0.1 jsonapi.example.local" >> /etc/hosts'

# Array of API endpoints to test
endpoints=(
    "GET http://jsonapi.example.local/api/users"
    "GET http://jsonapi.example.local/api/users/123"
    "GET http://jsonapi.example.local/api/users/456"
    "GET http://jsonapi.example.local/api/v1/products"
    "GET http://jsonapi.example.local/api/v1/products/abc-123"
    "GET http://jsonapi.example.local/api/v2/products"
    "GET http://jsonapi.example.local/api/users/123/orders"
    "GET http://jsonapi.example.local/api/orders/550e8400-e29b-41d4-a716-446655440000"
    "GET http://jsonapi.example.local/health"
    "GET http://jsonapi.example.local/nonexistent"
    "POST http://jsonapi.example.local/api/users"
    "PUT http://jsonapi.example.local/api/users/123"
    "DELETE http://jsonapi.example.local/api/users/123"
)

start_time=$(date +%s)
end_time=$((start_time + DURATION))
request_count=0
success_count=0
error_count=0

# Function to make API requests
make_request() {
    local method=$1
    local url=$2
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    case $method in
        "GET")
            response=$(curl -s -w "%{time_total},%{http_code}\n" \
                -H "Accept: application/json" \
                -o /dev/null \
                "$url")
            ;;
        "POST")
            response=$(curl -s -w "%{time_total},%{http_code}\n" \
                -X POST \
                -H "Content-Type: application/json" \
                -H "Accept: application/json" \
                -d '{"name":"Test User","email":"test@example.com"}' \
                -o /dev/null \
                "$url")
            ;;
        "PUT")
            response=$(curl -s -w "%{time_total},%{http_code}\n" \
                -X PUT \
                -H "Content-Type: application/json" \
                -H "Accept: application/json" \
                -d '{"name":"Updated User","email":"updated@example.com"}' \
                -o /dev/null \
                "$url")
            ;;
        "DELETE")
            response=$(curl -s -w "%{time_total},%{http_code}\n" \
                -X DELETE \
                -o /dev/null \
                "$url")
            ;;
    esac

    # Parse response
    IFS=',' read -ra response_data <<< "$response"
    local response_time=${response_data[0]}
    local http_code=${response_data[1]}

    # Log to CSV
    echo "$timestamp,$method,$url,$response_time,$http_code" >> "$OUTPUT_FILE"

    # Update counters
    if [[ $http_code -ge 200 && $http_code -lt 300 ]]; then
        echo -e "${GREEN}[$timestamp] Success: $method $url (${response_time}s, HTTP $http_code)${NC}"
        ((success_count++))
    else
        echo -e "${RED}[$timestamp] Error: $method $url (${response_time}s, HTTP $http_code)${NC}"
        ((error_count++))
    fi

    return $http_code
}

# Main loop
while [[ $(date +%s) -lt $end_time ]]; do
    # Select random endpoint
    endpoint=${endpoints[$RANDOM % ${#endpoints[@]}]}
    method=$(echo "$endpoint" | cut -d' ' -f1)
    url=$(echo "$endpoint" | cut -d' ' -f2)

    make_request "$method" "$url"
    ((request_count++))

    # Calculate dynamic sleep to maintain RPS
    current_time=$(date +%s.%N)
    target_time=$(awk "BEGIN {print $start_time + ($request_count / $RPS)}")
    sleep_time=$(awk "BEGIN {if ($target_time > $current_time) print $target_time - $current_time; else print 0}")

    sleep "$sleep_time"
done

# Generate summary
error_rate=$(awk "BEGIN {print $error_count/$request_count*100}")
avg_response_time=$(awk -F, '{sum+=$4} END {print sum/NR}' "$OUTPUT_FILE")

echo -e "\n${YELLOW}=== Test Summary ==="
echo -e "Total requests: ${GREEN}$request_count${NC}"
echo -e "Successful requests: ${GREEN}$success_count${NC}"
echo -e "Failed requests: ${RED}$error_count${NC}"
echo -e "Error rate: ${RED}$error_rate%${NC}"
echo -e "Average response time: ${GREEN}$avg_response_time seconds${NC}"
echo -e "Results saved to: ${GREEN}$OUTPUT_FILE${NC}"
echo -e "\nNext steps:"
echo -e "1. Analyze results: ${GREEN}cat $OUTPUT_FILE | column -t -s,${NC}"
echo -e "2. Check metrics at: ${GREEN}http://localhost:9090/metrics${NC}"
echo -e "3. Traefik dashboard: ${GREEN}http://localhost:8080${NC}"
echo -e "4. Prometheus: ${GREEN}http://localhost:9091${NC}"