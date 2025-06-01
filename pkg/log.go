package main

import (
	_ "flag"
	"fmt"
	logger "github.com/sirupsen/logrus"
	_ "time"
)

type parser func(line string) (traefikLogConfig, error)

func processLogs(logSource LogSource, config TraefikOfficerConfig, useK8sPtr *bool, logFileConfig *LogFileConfig, jsonLogsPtr *bool) {
	// Only set up log rotation for file mode
	var linesToRotate int
	if !*useK8sPtr {
		if logFileConfig.MaxFileBytes <= 0 {
			logFileConfig.MaxFileBytes = 10 // Default to 10MB if invalid value provided
			logger.Warnf("Invalid max-accesslog-size %d, using default: 10MB", logFileConfig.MaxFileBytes)
		}

		linesToRotate = (1000000 * logFileConfig.MaxFileBytes) / EstBytesPerLine
		if linesToRotate <= 0 {
			linesToRotate = 1000 // Ensure we have a reasonable minimum
		}
		logger.Infof("Rotating logs every %d lines (approximately %dMB)", linesToRotate, logFileConfig.MaxFileBytes)
	}

	// Set up parser
	var parse parser
	if *jsonLogsPtr {
		logger.Info("Setting parser to JSON")
		parse = parseJSON
	} else {
		parse = parseLine
	}
	// Main processing loop
	i := 0
	for logLine := range logSource.ReadLines() {
		// Update last processed time for health checks
		UpdateLastProcessedTime()

		if logLine.Err != nil {
			logger.Error("Log reading error:", logLine.Err)
			continue
		}

		// Only rotate logs in file mode
		if !*useK8sPtr {
			i++
			if i >= linesToRotate {
				i = 0
				if err := logRotate(logFileConfig.FileLocation); err != nil {
					logger.Errorf("Error rotating log file: %v", err)
				}
			}
		}

		//logger.Debugf("Read Line: %s", logLine.Text)
		d, err := parse(logLine.Text)
		if err != nil {
			// Skip lines that couldn't be parsed (already logged in parseLine)
			if err.Error() != "not an access log line" &&
				err.Error() != "empty line" &&
				err.Error() != "invalid access log format" {
				logger.Debugf("Parse error (%v) for line: %s", err, logLine.Text)
			}
			continue
		}

		// Check if this service should be ignored
		if !startsWith(config.AllowedServices, d.RouterName) {
			logger.Debugf("Ignoring service: %s, not in allowed list %s", d.RouterName, config.AllowedServices)
			continue
		}

		logger.Debugf("Found Matching service: %s, in allowed list", d.RouterName)

		updateMetrics(&d, config.URLPatterns)

		// Only JSON logs have Overhead metrics
		if *jsonLogsPtr {
			traefikOverhead.Observe(d.Overhead)
		}
	}
}

// createLogSource creates the appropriate log source based on configuration
func createLogSource(useK8s bool, logFileConfig *LogFileConfig, k8sConfig *K8SConfig) (LogSource, error) {
	if useK8s {
		logger.Info("Creating Kubernetes log source with label selector:", k8sConfig.LabelSelector)

		kls, err := NewKubernetesLogSource(k8sConfig)
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
		return NewFileLogSource(logFileConfig)
	}
}
