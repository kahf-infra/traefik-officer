package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"regexp"
	"strconv"
)

// URLPattern represents a URL pattern configuration for a service
type URLPattern struct {
	ServiceName string         `json:"service_name"`
	Pattern     string         `json:"pattern"`
	Replacement string         `json:"replacement"`
	Regex       *regexp.Regexp `json:"-"`
}

var (
	// Track metrics for calculating averages and error rates
	endpointStats = make(map[string]*EndpointStat)
)

type EndpointStat struct {
	TotalRequests    int64
	TotalDuration    float64
	MaxDuration      float64
	ErrorCount       int64
	ClientErrorCount int64
	ServerErrorCount int64
}

var (
	traefikOverhead = promauto.NewSummary(prometheus.SummaryOpts{
		Name: "traefik_officer_traefik_overhead",
		Help: "The overhead caused by traefik processing of requests",
	})

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

	endpointClientErrorRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "traefik_officer_endpoint_client_error_rate",
			Help: "Error rate per endpoint (ratio of 4xx responses)",
		},
		[]string{"app", "request_path"},
	)

	endpointServerErrorRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "traefik_officer_endpoint_server_error_rate",
			Help: "Error rate per endpoint (ratio of 5xx responses)",
		},
		[]string{"app", "request_path"},
	)
)

func updateMetrics(entry *traefikLogConfig, urlPatterns []URLPattern) {
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

	isError := entry.OriginStatus >= 400
	if isError {
		stat.ErrorCount++
		if entry.OriginStatus >= 500 {
			stat.ServerErrorCount++
		} else {
			stat.ClientErrorCount++
		}
	}

	// Check if this is a top path for its service
	topPathsMutex.RLock()
	isTopPath := topPathsPerService[service][key]
	topPathsMutex.RUnlock()

	if !isTopPath && stat.TotalRequests > 10 { // Only check for top paths after some requests
		updateTopPaths()
		return
	}

	if isTopPath {
		avgLatency := stat.TotalDuration / float64(stat.TotalRequests)
		errorRate := float64(stat.ErrorCount) / float64(stat.TotalRequests)
		clientErrorRate := float64(stat.ClientErrorCount) / float64(stat.TotalRequests)
		serverErrorRate := float64(stat.ServerErrorCount) / float64(stat.TotalRequests)
		endpointAvgLatency.WithLabelValues(service, endpoint).Set(avgLatency)
		endpointMaxLatency.WithLabelValues(service, endpoint).Set(stat.MaxDuration)
		endpointErrorRate.WithLabelValues(service, endpoint).Set(errorRate)
		endpointClientErrorRate.WithLabelValues(service, endpoint).Set(clientErrorRate)
		endpointServerErrorRate.WithLabelValues(service, endpoint).Set(serverErrorRate)
		endpointRequests.WithLabelValues(service, endpoint, method, code).Inc()
		endpointDuration.WithLabelValues(service, endpoint, method, code).Observe(duration)
	}
}
