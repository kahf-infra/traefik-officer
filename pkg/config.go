package main

import (
	"encoding/json"
	"fmt"
	logger "github.com/sirupsen/logrus"
	"io"
	"os"
	"regexp"
	"sync"
	"time"
)

var (
	// ... existing variables ...
	topNPaths          int
	topPathsMutex      sync.RWMutex
	topPathsPerService = make(map[string]map[string]bool) // Tracks which paths are in the top N
)

type TraefikOfficerConfig struct {
	IgnoredNamespaces        []string     `json:"IgnoredNamespaces"`
	IgnoredRouters           []string     `json:"IgnoredRouters"`
	IgnoredPathsRegex        []string     `json:"IgnoredPathsRegex"`
	MergePathsWithExtensions []string     `json:"MergePathsWithExtensions"`
	URLPatterns              []URLPattern `json:"URLPatterns"`
	AllowedServices          []string     `json:"AllowedServices"`
	TopNPaths                int          `json:"TopNPaths"`
	Debug                    bool         `json:"Debug"`
}

type traefikLogConfig struct {
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

func LoadConfig(configLocation string) (TraefikOfficerConfig, error) {
	var config TraefikOfficerConfig

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
	if config.URLPatterns == nil {
		config.URLPatterns = []URLPattern{}
	}

	if config.TopNPaths == 0 {
		config.TopNPaths = 20
	}

	// Compile regex patterns
	for i := range config.URLPatterns {
		regex, err := regexp.Compile(config.URLPatterns[i].Pattern)
		if err != nil {
			logger.Warnf("Invalid regex pattern for %s: %v - pattern will be ignored", config.URLPatterns[i].Replacement, err)
			continue
		}
		config.URLPatterns[i].Regex = regex
	}

	topNPaths = config.TopNPaths

	return config, nil
}

type LogSource interface {
	ReadLines() <-chan LogLine
	Close() error
}

// LogLine represents a single log line with metadata
type LogLine struct {
	Text string
	Time time.Time
	Err  error
}

// createLogSource creates the appropriate log source based on configuration
func createLogSource(useK8s bool, filename, containerName, labelSelector string, k8sConfig *K8SConfig) (LogSource, error) {
	if useK8s {
		logger.Info("Creating Kubernetes log source with label selector:", labelSelector)

		kls, err := NewKubernetesLogSource(k8sConfig, containerName, labelSelector)
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
		return NewFileLogSource(filename)
	}
}
