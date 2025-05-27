#!/usr/bin/env python3

import csv
import datetime
import random
import time

import requests

# Config
RPS = 10
DURATION = 60
OUTPUT_FILE = 'test_results.csv'

# Precompute end time
end_time = int(time.time() + DURATION)

# Endpoints
endpoints = [
    "GET http://jsonapi.example.local/api/users",
    "GET http://jsonapi.example.local/api/users/123",
    "GET http://jsonapi.example.local/api/users/456",
    "GET http://jsonapi.example.local/api/v1/products",
    "GET http://jsonapi.example.local/api/v1/products/abc-123",
    "GET http://jsonapi.example.local/api/v2/products",
    "GET http://jsonapi.example.local/api/users/123/orders",
    "GET http://jsonapi.example.local/api/orders/550e8400-e29b-41d4-a716-446655440000",
    "GET http://jsonapi.example.local/health",
    "GET http://jsonapi.example.local/nonexistent",
    "POST http://jsonapi.example.local/api/users",
    "PUT http://jsonapi.example.local/api/users/123",
    "DELETE http://jsonapi.example.local/api/users/123"
]

# Colors
RED = '\033[91m'
GREEN = '\033[92m'
YELLOW = '\033[93m'
NC = '\033[0m'

# Main loop
request_count = 0
success_count = 0
error_count = 0

with open(OUTPUT_FILE, 'w', newline='') as csvfile:
    writer = csv.writer(csvfile)
    writer.writerow(['timestamp', 'method', 'url', 'response_time', 'http_code'])

    while time.time() < end_time:
        # Select random endpoint
        endpoint = random.choice(endpoints)
        method, url = endpoint.split()

        start_time = time.time()
        try:
            response = requests.request(method, url, timeout=10)
        except requests.exceptions.RequestException as e:
            response_time = time.time() - start_time
            http_code = 0
            print(f'{RED}Error: {method} {url} ({response_time:.2f}s, {e}){NC}')
        else:
            response_time = time.time() - start_time
            http_code = response.status_code
            if 200 <= response.status_code < 300:
                print(f'{GREEN}Success: {method} {url} ({response_time:.2f}s, HTTP {http_code}){NC}')
                success_count += 1
            else:
                print(f'{RED}Error: {method} {url} ({response_time:.2f}s, HTTP {http_code}){NC}')
                error_count += 1

        writer.writerow([datetime.datetime.now(), method, url, response_time, http_code])
        request_count += 1

        # Calculate dynamic sleep to maintain RPS
        current_time = time.time()
        target_time = start_time + (request_count / RPS)
        sleep_time = max(0, target_time - current_time)
        time.sleep(sleep_time)

# Generate summary
error_rate = error_count / request_count * 100
avg_response_time = sum(row[3] for row in csv.reader(open(OUTPUT_FILE))) / request_count

print(f'\n{YELLOW}=== Test Summary ===')
print(f'Total requests: {GREEN}{request_count}{NC}')
print(f'Successful requests: {GREEN}{success_count}{NC}')
print(f'Failed requests: {RED}{error_count}{NC}')
print(f'Error rate: {RED}{error_rate:.2f}%{NC}')
print(f'Average response time: {GREEN}{avg_response_time:.2f} seconds{NC}')
print(f'Results saved to: {GREEN}{OUTPUT_FILE}{NC}')
print(f'\nNext steps:')
print(f'1. Analyze results: {GREEN}cat {OUTPUT_FILE} | column -t -s,{NC}')
print(f'2. Check metrics at: {GREEN}http://localhost:9090/metrics{NC}')
print(f'3. Traefik dashboard: {GREEN}http://localhost:8080{NC}')
print(f'4. Prometheus: {GREEN}http://localhost:9091{NC}')