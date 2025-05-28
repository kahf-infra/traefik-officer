package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	logger "github.com/sirupsen/logrus"
)

func serveProm(port string) error {
	if port == "" {
		return errors.New("port cannot be empty")
	}

	addr := ":" + port

	// Register handlers
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health", HealthHandler)

	logger.Infof("Starting metrics server on %s/metrics", addr)
	logger.Info("Health check available at /health")

	server := &http.Server{
		Addr: addr,
	}

	// Update health status to indicate service is running
	UpdateHealthStatus("http_server", "running", nil)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		UpdateHealthStatus("http_server", "error", err)
		return fmt.Errorf("failed to start metrics server: %w", err)
	}

	return nil
}
