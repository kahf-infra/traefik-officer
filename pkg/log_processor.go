package main

import (
	_ "flag"
	"fmt"
	logger "github.com/sirupsen/logrus"
	"strings"
	_ "time"
)

func processLogs(logSource LogSource, parse parser, whiteListChecker func(string, []string) bool, config traefikOfficerConfig, useK8sPtr *bool, fileNamePtr *string, linesToRotate int, requestArgsPtr *bool, strictWhiteListPtr *bool, passLogAboveThresholdPtr *float64, jsonLogsPtr *bool) {
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

		// Push metrics to prometheus exporter
		linesProcessed.Inc()

		requestPath := d.RequestPath
		if *requestArgsPtr == false {
			logger.Debug("Trimming query arguments")
			requestPath = strings.Split(d.RequestPath, "?")[0]
			requestPath = strings.Split(requestPath, "&")[0]

			// Merge paths where query args are embedded in the url (/api/arg1/arg1)
			requestPath = mergePaths(requestPath, config.MergePathsWithExtensions)
		}

		// Check whether a path is in the allowlist:
		isWhitelisted := whiteListChecker(requestPath, config.WhitelistPaths)

		if !isWhitelisted && *strictWhiteListPtr {
			linesIgnored.Inc()
			continue
		}
		if !isWhitelisted {
			// Check whether router name matches ignored namespace, router or path regex.
			ignoreMatched := checkMatches(d.RouterName, config.IgnoredNamespaces) ||
				checkMatches(d.RouterName, config.IgnoredRouters) || checkMatches(requestPath, config.IgnoredPathsRegex)

			if ignoreMatched {
				linesIgnored.Inc()
				logger.Debug("Ignoring line, due to ignore rule: ", logLine.Text)
				continue
			}
		}

		if d.Duration > *passLogAboveThresholdPtr && isWhitelisted {
			logger.Info("Request took longer than threshold: ", *passLogAboveThresholdPtr)
			fmt.Println(logLine.Text)
		}

		// Not ignored, publish metric
		latencyMetrics.WithLabelValues(requestPath, d.RequestMethod).Observe(d.Duration)

		processLogEntry(&d, config.URLPatterns)

		// Only JSON logs have Overhead metrics
		if *jsonLogsPtr {
			traefikOverhead.Observe(d.Overhead)
		}
	}
}
