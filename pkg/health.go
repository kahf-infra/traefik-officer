package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// HealthStatus represents the health status of the service
type HealthStatus struct {
	Status     string            `json:"status"`
	Uptime     string            `json:"uptime,omitempty"`
	Components map[string]string `json:"components,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// Global variables for health status
var (
	healthStatus      atomic.Value
	startupTime       = time.Now()
	lastProcessedTime atomic.Value
)

// Initialize health status
func init() {
	healthStatus.Store(HealthStatus{
		Status: "starting",
		Components: map[string]string{
			"service": "initializing",
		},
	})
	lastProcessedTime.Store(time.Now())
}

// UpdateHealthStatus updates the health status of a component
func UpdateHealthStatus(component, status string, err error) {
	current := healthStatus.Load().(HealthStatus)
	if current.Components == nil {
		current.Components = make(map[string]string)
	}

	current.Components[component] = status
	if err != nil {
		current.Status = "error"
		current.Error = err.Error()
	} else if current.Status != "error" {
		current.Status = "healthy"
	}

	healthStatus.Store(current)
}

// UpdateLastProcessedTime updates the timestamp of the last processed log line
func UpdateLastProcessedTime() {
	lastProcessedTime.Store(time.Now())
}

// HealthHandler handles health check requests
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	status := healthStatus.Load().(HealthStatus)
	status.Uptime = time.Since(startupTime).String()

	// Check if we're processing logs
	if time.Since(lastProcessedTime.Load().(time.Time)) > 5*time.Minute {
		status.Components["log_processing"] = "stale"
		status.Status = "degraded"
		status.Error = "No logs processed in the last 5 minutes"
	}

	w.Header().Set("Content-Type", "application/json")
	if status.Status != "healthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	err := json.NewEncoder(w).Encode(status)
	if err != nil {
		return
	}
}
