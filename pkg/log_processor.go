package main

import (
	_ "flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	logger "github.com/sirupsen/logrus"
	"strings"
	_ "time"
)

var (
	linesProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "traefik_officer_lines_processed",
		Help: "Number of access log lines processed",
	})

	linesIgnored = promauto.NewCounter(prometheus.CounterOpts{
		Name: "traefik_officer_lines_ignored",
		Help: "Number of access log lines ignored from latency metrics",
	})

	traefikOverhead = promauto.NewSummary(prometheus.SummaryOpts{
		Name: "traefik_officer_traefik_overhead",
		Help: "The overhead caused by traefik processing of requests",
	})

	latencyMetrics = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "traefik_officer_latency",
		Help:    "Latency metrics per service / endpoint",
		Buckets: []float64{0, 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 20000, 60000},
	},
		[]string{"request_path", "request_method"})
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

		// Check if this service should be ignored
		if !contains(config.AllowedServices, extractServiceName(d.RouterName)) {
			logger.Debugf("Ignoring service: %s, not in allowed list", d.RouterName)
			continue
		}

		proceedNext := updateV1metric(&d, config, logLine, whiteListChecker, requestArgsPtr, strictWhiteListPtr, passLogAboveThresholdPtr)
		if proceedNext {
			continue
		}

		processLogEntry(&d, config.URLPatterns)

		// Only JSON logs have Overhead metrics
		if *jsonLogsPtr {
			traefikOverhead.Observe(d.Overhead)
		}
	}
}

func updateV1metric(d *traefikJSONLog, config traefikOfficerConfig, logLine LogLine, whiteListChecker func(string, []string) bool, requestArgsPtr *bool, strictWhiteListPtr *bool, passLogAboveThresholdPtr *float64) (proceedNext bool) {
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
		return true
	}
	if !isWhitelisted {
		// Check whether router name matches ignored namespace, router or path regex.
		ignoreMatched := checkMatches(d.RouterName, config.IgnoredNamespaces) ||
			checkMatches(d.RouterName, config.IgnoredRouters) || checkMatches(requestPath, config.IgnoredPathsRegex)

		if ignoreMatched {
			linesIgnored.Inc()
			logger.Debug("Ignoring line, due to ignore rule: ", logLine.Text)
			return true
		}
	}

	if d.Duration > *passLogAboveThresholdPtr && isWhitelisted {
		logger.Info("Request took longer than threshold: ", *passLogAboveThresholdPtr)
		fmt.Println(logLine.Text)
	}

	// Not ignored, publish metric
	latencyMetrics.WithLabelValues(requestPath, d.RequestMethod).Observe(d.Duration)
	return false
}
