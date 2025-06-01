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
	http.Handle("/metrics", http.HandlerFunc(metricsHandlerWithGaugeReset))
	http.HandleFunc("/health", HealthHandler)

	logger.Infof("Starting metrics server on %s/metrics", addr)
	logger.Infof("Health check available at %s/health", addr)

	server := &http.Server{
		Addr: addr,
	}

	// Update health status to indicate service is running
	UpdateHealthStatus("http_server", "running", nil)

	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- fmt.Errorf("failed to start metrics server: %w", err)
		}
	}()

	// Check if server started successfully
	select {
	case err := <-errChan:
		return err
	default:
		// Server started successfully
		SetServiceReady()
		logger.Info("Metrics server started successfully")
		return nil
	}
}

func metricsHandlerWithGaugeReset(w http.ResponseWriter, r *http.Request) {
	// Serve metrics
	promhttp.Handler().ServeHTTP(w, r)

	endpointErrorRate.Reset()
	endpointClientErrorRate.Reset()
	endpointServerErrorRate.Reset()
}
