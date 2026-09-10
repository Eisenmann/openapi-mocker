// Package logger provides a simple structured logger that implements
// usecase.Logger using the standard library log package.
package logger

import (
	"fmt"
	"log"
	"strings"
)

// Logger is a minimal structured logger.
type Logger struct{}

// New creates a new logger.
func New() *Logger { return &Logger{} }

// Info logs an informational message with key-value pairs.
func (l *Logger) Info(msg string, keyvals ...interface{}) {
	log.Printf("INFO %s %s", msg, formatKeyVals(keyvals...))
}

// Error logs an error message with key-value pairs.
func (l *Logger) Error(msg string, keyvals ...interface{}) {
	log.Printf("ERROR %s %s", msg, formatKeyVals(keyvals...))
}

// formatKeyVals formats key-value pairs into a space-separated string.
// Uses strings.Builder to avoid O(n²) string concatenation in loops.
func formatKeyVals(keyvals ...interface{}) string {
	if len(keyvals) == 0 {
		return ""
	}

	var b strings.Builder

	for i := 0; i < len(keyvals); i += 2 {
		if i+1 < len(keyvals) {
			fmt.Fprintf(&b, " %v=%v", keyvals[i], keyvals[i+1])
		} else {
			fmt.Fprintf(&b, " %v", keyvals[i])
		}
	}

	return b.String()
}
