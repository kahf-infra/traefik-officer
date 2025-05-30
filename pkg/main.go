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
	debugLogPtr := flag.Bool("debug", false, "Enable debug logging. False by default.")
	requestArgsPtr := flag.Bool("include-query-args", false,
		"Set this if you wish for the query arguments to be displayed in the 'RequestPath' in latency metrics. Default: false")
	configLocationPtr := flag.String("config-file", "", "Path to the config file.")
	servePortPtr := flag.String("listen-port", "8080", "Which port to expose metrics on")
	jsonLogsPtr := flag.Bool("json-logs", false,
		"If true, parse JSON logs instead of accessLog format")
	// New Kubernetes flags
	useK8sPtr := flag.Bool("use-k8s", false, "Read logs from Kubernetes pods instead of file")
	logFileConfig := AddFileFlags(flag.CommandLine)
	k8sConfig := AddKubernetesFlags(flag.CommandLine)

	flag.Parse()

	// Load configuration
	config, err := LoadConfig(*configLocationPtr)
	if err != nil {
		logger.Warnf("Failed to load configuration: %v. Using default configuration.", err)
	}

	if *debugLogPtr {
		logger.SetLevel(logger.DebugLevel)
	}

	// Log configuration
	if *useK8sPtr {
		logger.Infof("Kubernetes Mode - "+
			"Namespace: %s, "+
			"Container: %s, "+
			"Label Selector: %s",
			k8sConfig.Namespace, k8sConfig.ContainerName, k8sConfig.LabelSelector)
	} else {
		logger.Info("File Mode - Access Logs At:", logFileConfig.FileLocation)
	}

	logger.Info("Config File At:", *configLocationPtr)
	logger.Info("Display Query Args In Metrics: ", *requestArgsPtr)
	logger.Info("JSON Logs:", *jsonLogsPtr)

	// Start background task to update top paths
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			updateTopPaths()
		}
	}()

	// Start metrics server
	go func() {
		if err := serveProm(*servePortPtr); err != nil {
			logger.Errorf("Metrics server error: %v", err)
		}
	}()

	// Create log source
	logSource, err := createLogSource(*useK8sPtr, logFileConfig, k8sConfig)
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
	processLogs(logSource, config, useK8sPtr, logFileConfig, jsonLogsPtr)
}
