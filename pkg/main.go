package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/mitchellh/go-ps"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	logger "github.com/sirupsen/logrus"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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

type traefikOfficerConfig struct {
	IgnoredNamespaces        []string     `json:"IgnoredNamespaces"`
	IgnoredRouters           []string     `json:"IgnoredRouters"`
	IgnoredPathsRegex        []string     `json:"IgnoredPathsRegex"`
	MergePathsWithExtensions []string     `json:"MergePathsWithExtensions"`
	WhitelistPaths           []string     `json:"WhitelistPaths"`
	URLPatterns              []URLPattern `json:"URLPatterns"`
}

type traefikJSONLog struct {
	ClientHost        string  `json:"ClientHost"`
	StartUTC          string  `json:"StartUTC"`
	RouterName        string  `json:"RouterName"`
	RequestMethod     string  `json:"RequestMethod"`
	RequestPath       string  `json:"RequestPath"`
	RequestProtocol   string  `json:"RequestProtocol"`
	OriginStatus      int     `json:"OriginStatus"`
	OriginContentSize int     `json:"OriginContentSize"`
	RequestCount      int     `json:"RequestCount"`
	Duration          float64 `json:"Duration"`
	Overhead          float64 `json:"Overhead"`
}

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
		logger.Error("Failed to create log source:", err)
		os.Exit(1)
	}
	defer func() {
		if err := logSource.Close(); err != nil {
			logger.Errorf("Error closing log source: %v", err)
		}
	}()

	// Set up parser
	var parse parser
	if *jsonLogsPtr {
		logger.Info("Setting parser to JSON")
		parse = func(line string) (traefikJSONLog, error) {
			result, err := parseJSON(line)
			return result, err
		}
	} else {
		parse = func(line string) (traefikJSONLog, error) {
			result, err := parseLine(line)
			return result, err
		}
	}

	whiteListChecker := checkWhiteList
	if *strictWhiteListPtr {
		whiteListChecker = checkWhiteListStrict
	}

	logger.Info("Starting log processing")

	// Main processing loop
	i := 0
	for logLine := range logSource.ReadLines() {
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

		// Only JSON logs have Overhead metrics
		if *jsonLogsPtr {
			traefikOverhead.Observe(d.Overhead)
		}
		processLogEntry(&d, config.URLPatterns)
	}
}

func checkWhiteListStrict(str string, matchStrings []string) bool {
	for i := 0; i < len(matchStrings); i++ {
		matchStr := matchStrings[i]
		//if strings.Contains(str, matchStr) {
		if matchStr == str {
			return true
		}
	}
	return false
}

func checkWhiteList(str string, matchStrings []string) bool {
	for i := 0; i < len(matchStrings); i++ {
		matchStr := matchStrings[i]
		if strings.Contains(str, matchStr) {
			return true
		}
	}
	return false
}

func mergePaths(str string, matchStrings []string) string {
	for i := 0; i < len(matchStrings); i++ {
		matchStr := matchStrings[i]
		if strings.HasPrefix(str, matchStr) {
			return matchStr
		}
	}
	return str
}

func checkMatches(str string, matchExpressions []string) bool {
	for i := 0; i < len(matchExpressions); i++ {
		expr := matchExpressions[i]
		reg, err := regexp.Compile(expr)

		if err != nil {
			logger.Errorf("Error compiling regex '%s': %v", expr, err)
			continue // Skip this pattern if it doesn't compile
		}

		if reg.MatchString(str) {
			return true
		}
	}
	return false
}

func parseJSON(line string) (traefikJSONLog, error) {
	var err error
	var jsonLog traefikJSONLog

	if !json.Valid([]byte(line)) {
		err := fmt.Errorf("invalid JSON format in log line: %s", line)
		logger.Error(err)
		return traefikJSONLog{}, err
	}

	if err := json.Unmarshal([]byte(line), &jsonLog); err != nil {
		logger.Errorf("Failed to unmarshal JSON log: %v", err)
		return traefikJSONLog{}, fmt.Errorf("failed to unmarshal JSON log: %w", err)
	}

	jsonLog.Duration = jsonLog.Duration / 1000000 // JSON Logs format latency in nanoseconds, convert to ms
	jsonLog.Overhead = jsonLog.Overhead / 1000000 // sane for overhead metrics

	logger.Debugf("JSON Parsed: %+v", jsonLog)
	logger.Debugf("ClientHost: %s", jsonLog.ClientHost)
	logger.Debugf("StartUTC: %s", jsonLog.StartUTC)
	logger.Debugf("RouterName: %s", extractServiceName(jsonLog.RouterName))
	logger.Debugf("RequestMethod: %s", jsonLog.RequestMethod)
	logger.Debugf("RequestPath: %s", jsonLog.RequestPath)
	logger.Debugf("RequestProtocol: %s", jsonLog.RequestProtocol)
	logger.Debugf("OriginStatus: %d", jsonLog.OriginStatus)
	logger.Debugf("OriginContentSize: %dbytes", jsonLog.OriginContentSize)
	logger.Debugf("RequestCount: %d", jsonLog.RequestCount)
	logger.Debugf("Duration: %fms", jsonLog.Duration)
	logger.Debugf("Overhead: %fms", jsonLog.Overhead)

	return jsonLog, err
}

func isAccessLogLine(line string) bool {
	if len(line) == 0 {
		return false
	}

	// Look for common access log patterns
	ipPattern := `^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`
	ipv6Pattern := `^[0-9a-fA-F:]+`

	matched, err := regexp.MatchString(ipPattern, line)
	if err != nil {
		logger.Debugf("Error matching IPv4 pattern: %v", err)
	} else if matched {
		return true
	}

	matched, err = regexp.MatchString(ipv6Pattern, line)
	if err != nil {
		logger.Debugf("Error matching IPv6 pattern: %v", err)
	} else if matched {
		return true
	}

	return false
}

func parseLine(line string) (traefikJSONLog, error) {
	// Skip empty lines
	line = strings.TrimSpace(line)
	if line == "" {
		return traefikJSONLog{}, errors.New("empty line")
	}

	// Quick check if this looks like an access log line
	if !isAccessLogLine(line) {
		logger.Debugf("Skipping non-access log line: %s", line)
		return traefikJSONLog{}, errors.New("not an access log line")
	}

	var buffer bytes.Buffer
	buffer.WriteString(`(\S+)`)                  // 1 - ClientHost
	buffer.WriteString(`\s-\s`)                  // - - Spaces
	buffer.WriteString(`(\S+)\s`)                // 2 - ClientUsername
	buffer.WriteString(`\[([^]]+)\]\s`)          // 3 - StartUTC
	buffer.WriteString(`"(\S*)\s?`)              // 4 - RequestMethod
	buffer.WriteString(`((?:[^"]*(?:\\")?)*)\s`) // 5 - RequestPath
	buffer.WriteString(`([^"]*)"\s`)             // 6 - RequestProtocol
	buffer.WriteString(`(\S+)\s`)                // 7 - OriginStatus
	buffer.WriteString(`(\S+)\s`)                // 8 - OriginContentSize
	buffer.WriteString(`("?\S+"?)\s`)            // 9 - Referrer
	buffer.WriteString(`("\S+")\s`)              // 10 - User-Agent
	buffer.WriteString(`(\S+)\s`)                // 11 - RequestCount
	buffer.WriteString(`("[^"]*"|-)\s`)          // 12 - FrontendName
	buffer.WriteString(`("[^"]*"|-)\s`)          // 13 - BackendURL
	buffer.WriteString(`(\S+)`)                  // 14 - Duration

	regex, err := regexp.Compile(buffer.String())
	if err != nil {
		err = fmt.Errorf("failed to compile regex: %w", err)
		logger.Error(err)
		return traefikJSONLog{}, err
	}

	submatch := regex.FindStringSubmatch(line)
	if len(submatch) <= 13 {
		logger.Debugf("Line doesn't match access log format (matched %d parts): %s", len(submatch), line)
		return traefikJSONLog{}, errors.New("invalid access log format")
	}

	var log traefikJSONLog
	var parseErr error

	// Safely extract fields with error handling
	log.ClientHost = submatch[1]
	log.StartUTC = submatch[3]
	log.RequestMethod = submatch[4]
	log.RequestPath = submatch[5]
	log.RequestProtocol = submatch[6]

	// Parse status code
	if status, err := strconv.Atoi(submatch[7]); err == nil {
		log.OriginStatus = status
	} else {
		logger.Debugf("Invalid status code '%s' in line: %s", submatch[7], line)
		parseErr = errors.New("invalid status code")
	}

	// Parse content size
	if size, err := strconv.Atoi(submatch[8]); err == nil {
		log.OriginContentSize = size
	} else {
		logger.Debugf("Invalid content size '%s' in line: %s", submatch[8], line)
		parseErr = errors.New("invalid content size")
	}

	// Parse request count
	if count, err := strconv.Atoi(submatch[11]); err == nil {
		log.RequestCount = count
	} else {
		logger.Debugf("Invalid request count '%s' in line: %s", submatch[11], line)
		parseErr = errors.New("invalid request count")
	}

	log.RouterName = strings.Trim(submatch[12], "\"")

	// Parse duration
	latencyStr := strings.Trim(submatch[14], "ms")
	if duration, err := strconv.ParseFloat(latencyStr, 64); err == nil {
		log.Duration = duration
	} else {
		logger.Debugf("Invalid duration '%s' in line: %s", latencyStr, line)
		parseErr = errors.New("invalid duration")
	}

	if logger.GetLevel() >= logger.DebugLevel {
		logger.Debugf("Parsed access log: %+v", log)
	}

	return log, parseErr
}

func loadConfig(configLocation string) (traefikOfficerConfig, error) {
	var config traefikOfficerConfig

	if configLocation == "" {
		logger.Warn("No config file specified, using default configuration")
		return config, nil
	}

	cfgFile, err := os.Open(configLocation)
	if err != nil {
		return config, fmt.Errorf("error opening config file %s: %w", configLocation, err)
	}
	defer func() {
		if err := cfgFile.Close(); err != nil {
			logger.Warnf("Error closing config file: %v", err)
		}
	}()

	byteValue, err := io.ReadAll(cfgFile)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %w", err)
	}

	if len(byteValue) == 0 {
		logger.Warn("Config file is empty, using default configuration")
		return config, nil
	}

	if err := json.Unmarshal(byteValue, &config); err != nil {
		return config, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Initialize slices if they are nil to prevent nil pointer dereferences
	if config.IgnoredNamespaces == nil {
		config.IgnoredNamespaces = []string{}
	}
	if config.IgnoredRouters == nil {
		config.IgnoredRouters = []string{}
	}
	if config.IgnoredPathsRegex == nil {
		config.IgnoredPathsRegex = []string{}
	}
	if config.MergePathsWithExtensions == nil {
		config.MergePathsWithExtensions = []string{}
	}
	if config.WhitelistPaths == nil {
		config.WhitelistPaths = []string{}
	}
	if config.URLPatterns == nil {
		config.URLPatterns = []URLPattern{}
	}

	// Compile regex patterns
	for i := range config.URLPatterns {
		regex, err := regexp.Compile(config.URLPatterns[i].Pattern)
		if err != nil {
			logger.Warnf("Invalid regex pattern for %s: %v - pattern will be ignored", config.URLPatterns[i].Name, err)
			continue
		}
		config.URLPatterns[i].Regex = regex
	}

	return config, nil
}

func logRotate(accessLogLocation string) error {
	if accessLogLocation == "" {
		return errors.New("access log location cannot be empty")
	}

	// Get the Traefik process
	traefikPid, err := findTraefikProcess()
	if err != nil {
		return fmt.Errorf("failed to find Traefik process: %w", err)
	}

	if traefikPid == -1 {
		return errors.New("traefik process not found")
	}

	logger.Infof("Found Traefik process @ PID %d", traefikPid)

	traefikProcess, err := os.FindProcess(traefikPid)
	if err != nil {
		return fmt.Errorf("failed to find process with PID %d: %w", traefikPid, err)
	}

	// Delete and recreate the log file
	if err := deleteFile(accessLogLocation); err != nil {
		return fmt.Errorf("failed to delete log file: %w", err)
	}

	if err := createFile(accessLogLocation); err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}

	// Send USR1 signal to Traefik to reopen log files
	if err := traefikProcess.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("failed to send SIGUSR1 to Traefik process: %w", err)
	}

	logger.Info("Successfully rotated log file and signaled Traefik")
	return nil
}

// findTraefikProcess finds the Traefik process and returns its PID
func findTraefikProcess() (int, error) {
	processList, err := ps.Processes()
	if err != nil {
		return -1, fmt.Errorf("failed to list processes: %w", err)
	}

	for _, process := range processList {
		if process.Executable() == "traefik" {
			return process.Pid(), nil
		}
	}

	return -1, nil
}

func createFile(path string) error {
	if path == "" {
		return errors.New("path cannot be empty")
	}

	// Check if file already exists
	if _, err := os.Stat(path); err == nil {
		logger.Debugf("File %s already exists", path)
		return nil
	}

	// Create parent directories if they don't exist
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create the file with appropriate permissions (read/write for owner, read for others)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}

	if err := file.Close(); err != nil {
		logger.Warnf("Error closing file %s: %v", path, err)
	}

	logger.Infof("Created file: %s", path)
	return nil
}

func deleteFile(path string) error {
	if path == "" {
		return errors.New("path cannot be empty")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		logger.Debugf("File %s does not exist, nothing to delete", path)
		return nil
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to delete file %s: %w", path, err)
	}

	logger.Debugf("Successfully deleted file: %s", path)
	return nil
}

func serveProm(port string) error {
	if port == "" {
		return errors.New("port cannot be empty")
	}

	addr := ":" + port
	http.Handle("/metrics", promhttp.Handler())
	logger.Infof("Starting metrics server on %s/metrics", addr)

	server := &http.Server{
		Addr: addr,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to start metrics server: %w", err)
	}

	return nil
}
