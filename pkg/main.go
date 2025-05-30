package main

import (
	"flag"
	logger "github.com/sirupsen/logrus"
	"os"
	"time"
)

// EstBytesPerLine Estimated number of bytes per line - for log rotation
var EstBytesPerLine = 150

type parser func(line string) (traefikLogConfig, error)

func main() {
	debugLogPtr := flag.Bool("debug", false, "Enable debug logging. False by default.")
	fileNamePtr := flag.String("log-file", "./accessLog.txt", "The traefik access log file. Default: ./accessLog.txt")
	requestArgsPtr := flag.Bool("include-query-args", false,
		"Set this if you wish for the query arguments to be displayed in the 'RequestPath' in latency metrics. Default: false")
	configLocationPtr := flag.String("config-file", "", "Path to the config file.")
	servePortPtr := flag.String("listen-port", "8080", "Which port to expose metrics on")
	maxFileBytesPtr := flag.Int("max-accesslog-size", 10,
		"How many megabytes should we allow the accesslog to grow to before rotating")
	jsonLogsPtr := flag.Bool("json-logs", false,
		"If true, parse JSON logs instead of accessLog format")

	// New Kubernetes flags
	useK8sPtr := flag.Bool("use-k8s", false, "Read logs from Kubernetes pods instead of file")
	podLabelSelectorPtr := flag.String("pod-label-selector", "app.kubernetes.io/name=traefik", "Kubernetes pod label selector (e.g., 'app=traefik')")
	containerNamePtr := flag.String("container-name", "traefik", "Container name in the pods")
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
		logger.Infof("Kubernetes Mode - Container: %s, Label Selector: %s",
			*containerNamePtr, *podLabelSelectorPtr)
	} else {
		logger.Info("File Mode - Access Logs At:", *fileNamePtr)
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

	// Only set up log rotation for file mode
	var linesToRotate int
	if !*useK8sPtr {
		if *maxFileBytesPtr <= 0 {
			*maxFileBytesPtr = 10 // Default to 10MB if invalid value provided
			logger.Warnf("Invalid max-accesslog-size %d, using default: 10MB", *maxFileBytesPtr)
		}

		linesToRotate = (1000000 * *maxFileBytesPtr) / EstBytesPerLine
		if linesToRotate <= 0 {
			linesToRotate = 1000 // Ensure we have a reasonable minimum
		}
		logger.Infof("Rotating logs every %d lines (approximately %dMB)", linesToRotate, *maxFileBytesPtr)
	}

	// Create log source
	logSource, err := createLogSource(*useK8sPtr, *fileNamePtr, *containerNamePtr, *podLabelSelectorPtr, k8sConfig)
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

	// Set up parser
	var parse parser
	if *jsonLogsPtr {
		logger.Info("Setting parser to JSON")
		parse = parseJSON
	} else {
		parse = parseLine
	}

	logger.Info("Starting log processing")
	UpdateHealthStatus("log_processor", "running", nil)

	// Start log processing
	processLogs(logSource, parse, config, useK8sPtr, fileNamePtr, linesToRotate, jsonLogsPtr)
}
