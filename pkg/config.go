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
	logger.Debugf("TopNPaths: %d", config.TopNPaths)

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
