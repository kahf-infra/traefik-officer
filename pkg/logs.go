package main

import (
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
func createLogSource(useK8s bool, filename, podName, namespace, containerName string) (LogSource, error) {
	if useK8s {
		logger.Info("Creating Kubernetes log source")
		kls, err := NewKubernetesLogSource(podName, namespace, containerName)
		if err != nil {
			return nil, err
		}
		err = kls.startStreaming()
		if err != nil {
			return nil, err
		}
		return kls, nil
	} else {
		logger.Info("Creating file log source")
		return NewFileLogSource(filename)
	}
}
