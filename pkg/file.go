package main

import (
	"flag"
	"github.com/hpcloud/tail"
)

type LogFileConfig struct {
	FileLocation string
	MaxFileBytes int
}

// FileLogSource reads from file using tail
type FileLogSource struct {
	tail     *tail.Tail
	filename string
	lines    chan LogLine
}

// NewFileLogSource creates a new file-based log source
func NewFileLogSource(logFileConfig *LogFileConfig) (*FileLogSource, error) {
	tCfg := tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		Poll:      true,
	}

	t, err := tail.TailFile(logFileConfig.FileLocation, tCfg)
	if err != nil {
		return nil, err
	}

	fls := &FileLogSource{
		tail:     t,
		filename: logFileConfig.FileLocation,
		lines:    make(chan LogLine, 100),
	}

	// Start goroutine to convert tail.Line to LogLine
	go func() {
		defer close(fls.lines)
		for line := range t.Lines {
			if line.Err != nil {
				fls.lines <- LogLine{Text: "", Time: line.Time, Err: line.Err}
				continue
			}
			fls.lines <- LogLine{Text: line.Text, Time: line.Time, Err: nil}
		}
	}()

	return fls, nil
}

func (fls *FileLogSource) ReadLines() <-chan LogLine {
	return fls.lines
}

func (fls *FileLogSource) Close() error {
	if fls.tail != nil {
		return fls.tail.Stop()
	}
	return nil
}

func AddFileFlags(flags *flag.FlagSet) *LogFileConfig {
	config := &LogFileConfig{}

	flags.StringVar(&config.FileLocation, "log-file", "./accessLog.txt", "The traefik access log file. Default: ./accessLog.txt")
	flags.IntVar(&config.MaxFileBytes, "max-accesslog-size", 10,
		"How many megabytes should we allow the accesslog to grow to before rotating")
	return config
}
