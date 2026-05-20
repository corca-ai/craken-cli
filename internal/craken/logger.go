package craken

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type logger struct {
	path string
}

func newLogger(path string) (*logger, error) {
	if path == "" {
		return &logger{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &logger{path: path}, nil
}

func (l *logger) line(scope string, message string) {
	if l == nil || l.path == "" {
		return
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "[%s] [%s] %s\n", time.Now().UTC().Format(time.RFC3339Nano), scope, message)
}

func (l *logger) close() {}
