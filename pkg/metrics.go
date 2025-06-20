package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// URLPattern represents a URL pattern configuration for a service
type URLPattern struct {
	ServiceName string         `json:"service_name"`
	Pattern     string         `json:"pattern"`
	Replacement string         `json:"replacement"`
	Regex       *regexp.Regexp `json:"-"`
	Namespace   string         `json:"namespace"`
}

var (
	// Track metrics for calculating averages and error rates
	endpointStats      = make(map[string]*EndpointStat)
	endpointStatsMutex sync.RWMutex
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
	service := entry.RouterName
	duration := float64(entry.Duration) / 1000.0 // Convert to seconds

	// Original metrics (keeping existing functionality)
	totalRequests.WithLabelValues(method, code, service).Inc()
	requestDuration.WithLabelValues(method, code, service).Observe(duration)

	// New endpoint-specific metrics
	endpoint := normalizeURL(service, entry.RequestPath, urlPatterns)

	key := fmt.Sprintf("%s:%s", service, endpoint)
	endpointStatsMutex.RLock()
	if endpointStats[key] == nil {
		endpointStatsMutex.RUnlock()
		endpointStatsMutex.Lock()
		endpointStats[key] = &EndpointStat{}
		endpointStatsMutex.Unlock()
	} else {
		endpointStatsMutex.RUnlock()
	}
	endpointStatsMutex.RLock()
	stat := endpointStats[key]
	endpointStatsMutex.RUnlock()

	endpointStatsMutex.Lock()
	stat.TotalRequests++
	stat.TotalDuration += duration

	if duration > stat.MaxDuration {
		stat.MaxDuration = duration
	}
	endpointStatsMutex.Unlock()

	isError := entry.OriginStatus >= 400
	if isError {
		endpointStatsMutex.Lock()
		stat.ErrorCount++
		endpointStatsMutex.Unlock()
		errorRate := float64(stat.ErrorCount) / float64(stat.TotalRequests)
		endpointErrorRate.WithLabelValues(service, endpoint).Set(errorRate)
		if entry.OriginStatus >= 500 {
			endpointStatsMutex.Lock()
			stat.ServerErrorCount++
			endpointStatsMutex.Unlock()
			serverErrorRate := float64(stat.ServerErrorCount) / float64(stat.TotalRequests)
			endpointServerErrorRate.WithLabelValues(service, endpoint).Set(serverErrorRate)
		} else {
			endpointStatsMutex.Lock()
			stat.ClientErrorCount++
			endpointStatsMutex.Unlock()
			clientErrorRate := float64(stat.ClientErrorCount) / float64(stat.TotalRequests)
			endpointClientErrorRate.WithLabelValues(service, endpoint).Set(clientErrorRate)
		}
	}

	// Check if this is a top path for its service
	topPathsMutex.RLock()
	isTopPath := topPathsPerService[service][key]
	topPathsMutex.RUnlock()

	if isTopPath {
		avgLatency := stat.TotalDuration / float64(stat.TotalRequests)
		endpointAvgLatency.WithLabelValues(service, endpoint).Set(avgLatency)
		endpointMaxLatency.WithLabelValues(service, endpoint).Set(stat.MaxDuration)
		endpointRequests.WithLabelValues(service, endpoint, method, code).Inc()
		endpointDuration.WithLabelValues(service, endpoint, method, code).Observe(duration)
	}
}

func clearAllPathMetrics() {
	// Clear latency metrics
	endpointAvgLatency.Reset()
	endpointMaxLatency.Reset()
	endpointDuration.Reset()
	endpointRequests.Reset()
}

func startMetricsCleaner(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			clearAllPathMetrics()
		}
	}()
}
