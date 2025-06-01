package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mitchellh/go-ps"
	logger "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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

func parseJSON(line string) (traefikLogConfig, error) {
	var err error
	var jsonLog traefikLogConfig

	if !json.Valid([]byte(line)) {
		err := fmt.Errorf("invalid JSON format in log line: %s", line)
		logger.Error(err)
		return traefikLogConfig{}, err
	}

	if err := json.Unmarshal([]byte(line), &jsonLog); err != nil {
		logger.Errorf("Failed to unmarshal JSON log: %v", err)
		return traefikLogConfig{}, fmt.Errorf("failed to unmarshal JSON log: %w", err)
	}

	jsonLog.Duration = jsonLog.Duration / 1000000 // JSON Logs format latency in nanoseconds, convert to ms
	jsonLog.Overhead = jsonLog.Overhead / 1000000 // sane for overhead metrics

	logger.Debugf("JSON Parsed: %+v", jsonLog)
	logger.Debugf("ClientHost: %s", jsonLog.ClientHost)
	logger.Debugf("StartUTC: %s", jsonLog.StartUTC)
	logger.Debugf("RouterName: %s", jsonLog.RouterName)
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
	// Pattern 1: Starts with IP address (IPv4 or IPv6)
	ipv4Pattern := `^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`
	ipv6Pattern := `^[0-9a-fA-F:]+`

	// Pattern 2: Starts with [pod-name] followed by IP address
	podPattern := `^\[[^\]]+\]\s+\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`

	// Check for IPv4 at start of line
	if matched, _ := regexp.MatchString(ipv4Pattern, line); matched {
		return true
	}

	// Check for IPv6 at start of line
	if matched, _ := regexp.MatchString(ipv6Pattern, line); matched {
		return true
	}

	// Check for pod name prefix with [pod-name] format
	if matched, _ := regexp.MatchString(podPattern, line); matched {
		return true
	}

	// Additional check for common log patterns that might indicate an access log
	// This catches lines that have a timestamp in common log format
	commonLogPattern := `\[\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [\+\-]\d{4}\]`
	if matched, _ := regexp.MatchString(commonLogPattern, line); matched {
		return true
	}

	return false
}

func parseLine(line string) (traefikLogConfig, error) {
	// Skip empty lines
	line = strings.TrimSpace(line)
	if line == "" {
		return traefikLogConfig{}, errors.New("empty line")
	}

	// Quick check if this looks like an access log line
	if !isAccessLogLine(line) {
		logger.Debugf("Skipping non-access log line: %s", line)
		return traefikLogConfig{}, errors.New("not an access log line")
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
		return traefikLogConfig{}, err
	}

	submatch := regex.FindStringSubmatch(line)
	if len(submatch) <= 13 {
		logger.Debugf("Line doesn't match access log format (matched %d parts): %s", len(submatch), line)
		return traefikLogConfig{}, errors.New("invalid access log format")
	}

	var log traefikLogConfig
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

	//if logger.GetLevel() >= logger.DebugLevel {
	//	logger.Debugf("Parsed access log: %+v", log)
	//}

	return log, parseErr
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

// Helper function to check if a string is in a slice

func contains(slice []TraefikService, item string) bool {
	for _, s := range slice {
		name := BuildServiceName(s.Namespace, s.Name, "-")
		if name == item {
			return true
		}
	}
	return false
}

func startsWith(slice []TraefikService, item string) bool {
	for _, s := range slice {
		name := BuildServiceName(s.Namespace, s.Name, "-")
		if strings.HasPrefix(item, name) {
			return true
		}
	}
	return false
}

func updateTopPaths() {
	logger.Debug("******** Updating top paths... ***********")
	type pathStat struct {
		service    string
		path       string
		avgLatency float64
	}

	// Group paths by service
	servicePaths := make(map[string][]pathStat)

	// Get all paths and their stats
	for key, stat := range endpointStats {
		if stat.TotalRequests > 0 {
			// Split the key into service and path
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				continue
			}
			service, path := parts[0], parts[1]

			// Add to service's path list
			servicePaths[service] = append(servicePaths[service], pathStat{
				service:    service,
				path:       path,
				avgLatency: stat.TotalDuration / float64(stat.TotalRequests),
			})
		}
	}

	topPathsMutex.Lock()
	defer topPathsMutex.Unlock()

	// Clear current top paths
	topPathsPerService = make(map[string]map[string]bool)

	// For each service, find its top N paths
	for service, paths := range servicePaths {
		// Sort paths by average latency (highest first)
		sort.Slice(paths, func(i, j int) bool {
			return paths[i].avgLatency > paths[j].avgLatency
		})

		// Take top N paths for this service
		limit := topNPaths
		if limit > len(paths) {
			limit = len(paths)
		}

		// Initialize service map if not exists
		if _, exists := topPathsPerService[service]; !exists {
			topPathsPerService[service] = make(map[string]bool)
		}

		// Add top paths for this service
		for i := 0; i < limit; i++ {
			pathKey := fmt.Sprintf("%s:%s", service, paths[i].path)
			topPathsPerService[service][pathKey] = true
		}
		logger.Debugf("Updated top paths. Service: %s, Total top paths: %d \n",
			service, countTotalTopPaths(topPathsPerService))
	}
}

// Helper function to count total top paths across all services
func countTotalTopPaths(tps map[string]map[string]bool) int {
	count := 0
	for _, paths := range tps {
		count += len(paths)
	}
	return count
}

func startTopPathsUpdater(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			updateTopPaths()
		}
	}()
}

func extractServiceName(routerName string) string {
	// Remove anything after @ character (including the @ itself)
	if idx := strings.Index(routerName, "@"); idx != -1 {
		routerName = routerName[:idx]
	}

	// Split by dash and try to find a meaningful service name
	parts := strings.Split(routerName, "-")
	if len(parts) >= 3 {
		// Try to identify a service pattern: namespace-service-name-type-protocol-hash
		for i := 0; i < len(parts)-2; i++ {
			if parts[i+1] == "api" || parts[i+1] == "web" || parts[i+1] == "service" {
				if i > 0 {
					return fmt.Sprintf("%s-%s", parts[i], parts[i+1])
				}
				return parts[i+1]
			}
		}

		// Fallback: use first 2-3 parts
		if len(parts) >= 4 {
			return strings.Join(parts[:3], "-")
		} else {
			return strings.Join(parts[:2], "-")
		}
	}

	// If parsing fails, return the first part or original
	if len(parts) > 0 {
		return parts[0]
	}
	return routerName
}

// normalizeURL applies URL patterns to normalize endpoints
func normalizeURL(serviceName, path string, urlPatterns []URLPattern) string {
	// First, try service-specific patterns
	for _, pattern := range urlPatterns {
		patternServiceName := BuildServiceName(pattern.Namespace, pattern.ServiceName, "-")
		if patternServiceName == serviceName && pattern.Regex != nil {
			if pattern.Regex.MatchString(path) {
				match := regexp.MustCompile(pattern.Regex.String())
				return match.ReplaceAllString(path, pattern.Replacement)
			}
		}
	}

	// Default normalization - replace IDs and UUIDs
	normalized := path

	// Replace numeric IDs
	re1 := regexp.MustCompile(`/\d+(/|$|\?)`)
	normalized = re1.ReplaceAllString(normalized, "/{id}$1")

	// Replace UUIDs
	re2 := regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(/|$|\?)`)
	normalized = re2.ReplaceAllString(normalized, "/{uuid}$1")

	// Replace other common patterns (long alphanumeric strings)
	re3 := regexp.MustCompile(`/[a-zA-Z0-9]{20,}(/|$|\?)`)
	normalized = re3.ReplaceAllString(normalized, "/{token}$1")

	// Replace query params
	re4 := regexp.MustCompile(`\?.*`)
	normalized = re4.ReplaceAllString(normalized, "?{query_params}")

	return normalized
}

// homeDir returns the home directory for the current user
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func BuildServiceName(namespace, name, separator string) string {
	// Remove leading/trailing whitespace
	str1 := strings.TrimSpace(namespace)
	str2 := strings.TrimSpace(name)

	// If either string is empty, return the non-empty one
	if str1 == "" {
		return str2
	}
	if str2 == "" {
		return str1
	}

	// Both strings have content, join with separator
	return str1 + separator + str2
}
