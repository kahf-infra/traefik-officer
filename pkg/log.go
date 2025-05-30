package main

import (
	_ "flag"
	logger "github.com/sirupsen/logrus"
	_ "time"
)

func processLogs(logSource LogSource, parse parser, config TraefikOfficerConfig, useK8sPtr *bool, fileNamePtr *string, linesToRotate int, jsonLogsPtr *bool) {
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
				if err := logRotate(*fileNamePtr); err != nil {
					logger.Errorf("Error rotating log file: %v", err)
				}
			}
		}

		logger.Debugf("Read Line: %s", logLine.Text)
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
		if !contains(config.AllowedServices, extractServiceName(d.RouterName)) {
			logger.Debugf("Ignoring service: %s, not in allowed list", d.RouterName)
			continue
		}

		updateMetrics(&d, config.URLPatterns)

		// Only JSON logs have Overhead metrics
		if *jsonLogsPtr {
			traefikOverhead.Observe(d.Overhead)
		}
	}
}
