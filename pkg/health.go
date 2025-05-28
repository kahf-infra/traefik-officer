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

// SetServiceReady updates the service status to ready
func SetServiceReady() {
	current := healthStatus.Load().(HealthStatus)
	if current.Components == nil {
		current.Components = make(map[string]string)
	}
	current.Status = "healthy"
	current.Components["service"] = "running"
	healthStatus.Store(current)
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
	// Create a local copy of the status to avoid concurrent map writes
	status := healthStatus.Load().(HealthStatus)
	response := HealthStatus{
		Status:     status.Status,
		Uptime:     time.Since(startupTime).Round(time.Second).String(),
		Components: make(map[string]string),
		Error:      status.Error,
	}

	// Safely copy the components
	for k, v := range status.Components {
		response.Components[k] = v
	}

	// Check if we're processing logs
	lastProcessed := lastProcessedTime.Load().(time.Time)
	if time.Since(lastProcessed) > 5*time.Minute {
		response.Components["log_processing"] = "stale"
		if response.Status == "healthy" {
			response.Status = "degraded"
			response.Error = "No logs processed in the last 5 minutes"
		}
	} else {
		response.Components["log_processing"] = "active"
	}

	w.Header().Set("Content-Type", "application/json")
	if response.Status != "healthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	_ = json.NewEncoder(w).Encode(response)
}
