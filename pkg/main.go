package main

import (
	"flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	logger "github.com/sirupsen/logrus"
	"os"
)

// EstBytesPerLine Estimated number of bytes per line - for log rotation
var EstBytesPerLine = 150

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
		[]string{"RequestPath", "RequestMethod"})
)

type parser func(line string) (traefikJSONLog, error)

func main() {
	debugLogPtr := flag.Bool("debug", false, "Enable debug logging. False by default.")
	fileNamePtr := flag.String("log-file", "./accessLog.txt", "The traefik access log file. Default: ./accessLog.txt")
	requestArgsPtr := flag.Bool("include-query-args", false,
		"Set this if you wish for the query arguments to be displayed in the 'RequestPath' in latency metrics. Default: false")
	configLocationPtr := flag.String("config-file", "", "Path to the config file.")
	servePortPtr := flag.String("listen-port", "8080", "Which port to expose metrics on")
	maxFileBytesPtr := flag.Int("max-accesslog-size", 10,
		"How many megabytes should we allow the accesslog to grow to before rotating")
	strictWhiteListPtr := flag.Bool("strict-whitelist", false,
		"If true, ONLY patterns matching the whitelist will be counted. If false, patterns whitelisted just skip ignore rules")
	jsonLogsPtr := flag.Bool("json-logs", false,
		"If true, parse JSON logs instead of accessLog format")
	passLogAboveThresholdPtr := flag.Float64("pass-log-above-threshold", 1,
		"Passthrough traefik accessLog line to stdout if request takes longer that X seconds. Only whitelisted request paths.")

	// New Kubernetes flags
	useK8sPtr := flag.Bool("use-k8s", false, "Read logs from Kubernetes pods instead of file")
	podLabelSelectorPtr := flag.String("pod-label-selector", "app.kubernetes.io/name=traefik", "Kubernetes pod label selector (e.g., 'app=traefik')")
	namespacePtr := flag.String("namespace", "ingress-controller", "Kubernetes namespace")
	containerNamePtr := flag.String("container-name", "traefik", "Container name in the pods")

	flag.Parse()

	if *debugLogPtr {
		logger.SetLevel(logger.DebugLevel)
	}

	// Log configuration
	if *useK8sPtr {
		logger.Infof("Kubernetes Mode - Namespace: %s, Container: %s, Label Selector: %s",
			*namespacePtr, *containerNamePtr, *podLabelSelectorPtr)
	} else {
		logger.Info("File Mode - Access Logs At:", *fileNamePtr)
	}

	logger.Info("Config File At:", *configLocationPtr)
	logger.Info("Display Query Args In Metrics: ", *requestArgsPtr)
	logger.Info("JSON Logs:", *jsonLogsPtr)

	// Load configuration
	config, err := loadConfig(*configLocationPtr)
	if err != nil {
		logger.Warnf("Failed to load configuration: %v. Using default configuration.", err)
	}

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

	fmt.Printf("Ignoring Namespaces: %s \nIgnoring Routers: %s\n Ignoring Paths: %s \n Merging Paths: %s\n Whitelist: %s\n",
		config.IgnoredNamespaces, config.IgnoredRouters, config.IgnoredPathsRegex, config.MergePathsWithExtensions,
		config.WhitelistPaths)

	// Create log source
	logSource, err := createLogSource(*useK8sPtr, *fileNamePtr, *namespacePtr, *containerNamePtr, *podLabelSelectorPtr)
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

	whiteListChecker := checkWhiteList
	if *strictWhiteListPtr {
		whiteListChecker = checkWhiteListStrict
	}

	logger.Info("Starting log processing")
	UpdateHealthStatus("log_processor", "running", nil)

	// Start log processing
	processLogs(logSource, parse, whiteListChecker, config, useK8sPtr, fileNamePtr, linesToRotate, requestArgsPtr, strictWhiteListPtr, passLogAboveThresholdPtr, jsonLogsPtr)
}
