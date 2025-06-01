package main

import (
	"flag"
	logger "github.com/sirupsen/logrus"
	"os"
	"time"
)

// EstBytesPerLine Estimated number of bytes per line - for log rotation
var EstBytesPerLine = 150

func main() {
	debugLog := flag.Bool("debug", false, "Enable debug logging. False by default.")
	configLocation := flag.String("config-file", "", "Path to the config file.")
	servePort := flag.String("listen-port", "8080", "Which port to expose metrics on")
	jsonLogs := flag.Bool("json-logs", false, "If true, parse JSON logs instead of accessLog format")
	useK8s := flag.Bool("use-k8s", false, "Read logs from Kubernetes pods instead of file")
	logFileConfig := AddFileFlags(flag.CommandLine)
	k8sConfig := AddKubernetesFlags(flag.CommandLine)

	flag.Parse()

	if *debugLog {
		logger.SetLevel(logger.DebugLevel)
	}

	// Load configuration
	config, err := LoadConfig(*configLocation)
	if err != nil {
		logger.Warnf("Failed to load configuration: %v. Using default configuration.", err)
	}

	// Log configuration
	if *useK8s {
		logger.Infof("Kubernetes Mode - "+
			"Namespace: %s, "+
			"Container: %s, "+
			"Label Selector: %s",
			k8sConfig.Namespace, k8sConfig.ContainerName, k8sConfig.LabelSelector)
	} else {
		logger.Info("File Mode - Access Logs At:", logFileConfig.FileLocation)
	}

	logger.Info("Config File At:", *configLocation)
	logger.Info("JSON Logs:", *jsonLogs)

	// Start background task to update top paths
	startTopPathsUpdater(30 * time.Second)
	startMetricsCleaner(60 * time.Minute)

	// Start metrics server
	go func() {
		if err := serveProm(*servePort); err != nil {
			logger.Errorf("Metrics server error: %v", err)
		}
	}()

	// Create log source
	logSource, err := createLogSource(*useK8s, logFileConfig, k8sConfig)
	if err != nil {
		UpdateHealthStatus("log_source", "error", err)
		logger.Error("Failed to create log source:", err)
		os.Exit(1)
	}
	defer func() {
		if err := logSource.Close(); err != nil {
			UpdateHealthStatus("log_source", "close_error", err)
			logger.Errorf("Error closing log source: %v", err)
		} else {
			UpdateHealthStatus("log_source", "closed", nil)
		}
	}()

	UpdateHealthStatus("log_processor", "running", nil)

	// Start log processing
	logger.Info("Starting log processing")
	processLogs(logSource, config, useK8s, logFileConfig, jsonLogs)
}
