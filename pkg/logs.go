package main

import (
	"fmt"
	logger "github.com/sirupsen/logrus"
	"time"
)

// LogSource represents the source of logs
type LogSource interface {
	ReadLines() <-chan LogLine
	Close() error
}

// LogLine represents a single log line with metadata
type LogLine struct {
	Text string
	Time time.Time
	Err  error
}

// createLogSource creates the appropriate log source based on configuration
func createLogSource(useK8s bool, filename, namespace, containerName, labelSelector string) (LogSource, error) {
	if useK8s {
		logger.Info("Creating Kubernetes log source with label selector:", labelSelector)
		kls, err := NewKubernetesLogSource(namespace, containerName, labelSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes log source: %v", err)
		}
		err = kls.startStreaming()
		if err != nil {
			return nil, fmt.Errorf("failed to start Kubernetes log streaming: %v", err)
		}
		return kls, nil
	} else {
		logger.Info("Creating file log source")
		return NewFileLogSource(filename)
	}
}
