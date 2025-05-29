package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"regexp"
	"strconv"
	"strings"
)

// URLPattern represents a URL pattern configuration for a service
type URLPattern struct {
	ServiceName string         `json:"service_name"`
	Pattern     string         `json:"pattern"`
	Name        string         `json:"name"`
	Regex       *regexp.Regexp `json:"-"`
}

var (
	// Original metrics
	totalRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "traefik_officer_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"request_method", "response_code", "app"},
	)

	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "traefik_officer_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"request_method", "response_code", "app"},
	)

	requestSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "traefik_officer_request_size_bytes",
			Help:    "Size of HTTP requests in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8),
		},
		[]string{"request_method", "response_code", "app"},
	)

	responseSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "traefik_officer_response_size_bytes",
			Help:    "Size of HTTP responses in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8),
		},
		[]string{"request_method", "response_code", "app"},
	)

	// New endpoint-specific metrics
	endpointRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "traefik_officer_endpoint_requests_total",
			Help: "Total number of HTTP requests per endpoint",
		},
		[]string{"app", "request_path", "request_method", "response_code"},
	)

	endpointDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "traefik_officer_endpoint_request_duration_seconds",
			Help:    "Duration of HTTP requests per endpoint in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"app", "request_path", "request_method", "response_code"},
	)

	endpointAvgLatency = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "traefik_officer_endpoint_avg_latency_seconds",
			Help: "Average latency per endpoint in seconds",
		},
		[]string{"app", "request_path"},
	)

	endpointMaxLatency = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "traefik_officer_endpoint_max_latency_seconds",
			Help: "Maximum latency per endpoint in seconds",
		},
		[]string{"app", "request_path"},
	)

	endpointErrorRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "traefik_officer_endpoint_error_rate",
			Help: "Error rate per endpoint (ratio of 4xx/5xx responses)",
		},
		[]string{"app", "request_path"},
	)

	// Track metrics for calculating averages and error rates
	endpointStats = make(map[string]*EndpointStat)
)

type EndpointStat struct {
	TotalRequests int64
	TotalDuration float64
	MaxDuration   float64
	ErrorCount    int64
}

// extractServiceName extracts service name from router name (keeping original logic)

func extractServiceName(routerName string) string {
	// Remove anything after @ character (including the @ itself)
	if idx := strings.Index(routerName, "@"); idx != -1 {
		routerName = routerName[:idx]
	}

	// Split by dash and try to find a meaningful service name
	parts := strings.Split(routerName, "-")
	if len(parts) >= 3 {
		// Try to identify a service pattern: namespace-service-name-type-protocol-hash
		for i := 0; i < len(parts)-2; i++ {
			if parts[i+1] == "api" || parts[i+1] == "web" || parts[i+1] == "service" {
				if i > 0 {
					return fmt.Sprintf("%s-%s", parts[i], parts[i+1])
				}
				return parts[i+1]
			}
		}

		// Fallback: use first 2-3 parts
		if len(parts) >= 4 {
			return strings.Join(parts[:3], "-")
		} else {
			return strings.Join(parts[:2], "-")
		}
	}

	// If parsing fails, return the first part or original
	if len(parts) > 0 {
		return parts[0]
	}
	return routerName
}

// normalizeURL applies URL patterns to normalize endpoints
func normalizeURL(serviceName, path string, urlPatterns []URLPattern) string {
	// First, try service-specific patterns
	for _, pattern := range urlPatterns {
		if pattern.ServiceName == serviceName && pattern.Regex != nil {
			if pattern.Regex.MatchString(path) {
				return pattern.Name
			}
		}
	}

	// Then try generic patterns (empty service name)
	for _, pattern := range urlPatterns {
		if pattern.ServiceName == "" && pattern.Regex != nil {
			if pattern.Regex.MatchString(path) {
				return pattern.Name
			}
		}
	}

	// Default normalization - replace IDs and UUIDs
	normalized := path

	// Replace numeric IDs
	re1 := regexp.MustCompile(`/\d+(/|$|\?)`)
	normalized = re1.ReplaceAllString(normalized, "/{id}$1")

	// Replace UUIDs
	re2 := regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(/|$|\?)`)
	normalized = re2.ReplaceAllString(normalized, "/{uuid}$1")

	// Replace other common patterns (long alphanumeric strings)
	re3 := regexp.MustCompile(`/[a-zA-Z0-9]{20,}(/|$|\?)`)
	normalized = re3.ReplaceAllString(normalized, "/{token}$1")

	return normalized
}

// updateEndpointStats updates endpoint statistics for gauge metrics
func updateEndpointStats(service, endpoint string, duration float64, isError bool) {
	key := fmt.Sprintf("%s:%s", service, endpoint)

	if endpointStats[key] == nil {
		endpointStats[key] = &EndpointStat{}
	}

	stat := endpointStats[key]
	stat.TotalRequests++
	stat.TotalDuration += duration

	if duration > stat.MaxDuration {
		stat.MaxDuration = duration
	}

	if isError {
		stat.ErrorCount++
	}

	// Update gauge metrics
	avgLatency := stat.TotalDuration / float64(stat.TotalRequests)
	errorRate := float64(stat.ErrorCount) / float64(stat.TotalRequests)

	endpointAvgLatency.WithLabelValues(service, endpoint).Set(avgLatency)
	endpointMaxLatency.WithLabelValues(service, endpoint).Set(stat.MaxDuration)
	endpointErrorRate.WithLabelValues(service, endpoint).Set(errorRate)
}

func processLogEntry(entry *traefikJSONLog, urlPatterns []URLPattern) {
	method := entry.RequestMethod
	code := strconv.Itoa(entry.OriginStatus)
	service := extractServiceName(entry.RouterName)
	duration := float64(entry.Duration) / 1000.0 // Convert to seconds

	// Original metrics (keeping existing functionality)
	totalRequests.WithLabelValues(method, code, service).Inc()
	requestDuration.WithLabelValues(method, code, service).Observe(duration)
	responseSize.WithLabelValues(method, code, service).Observe(float64(entry.OriginContentSize))
	if entry.OriginContentSize > 0 {
		requestSize.WithLabelValues(method, code, service).Observe(float64(entry.OriginContentSize))
	}

	// New endpoint-specific metrics
	endpoint := normalizeURL(service, entry.RequestPath, urlPatterns)
	endpointRequests.WithLabelValues(service, endpoint, method, code).Inc()
	endpointDuration.WithLabelValues(service, endpoint, method, code).Observe(duration)

	// Update endpoint stats for gauge metrics
	isError := entry.OriginStatus >= 400
	updateEndpointStats(service, endpoint, duration, isError)
}
