package common

import (
	"fmt"
	"io"
	"log"
	"os"
)

// Logger provides a simple logging interface that can be silenced during tests
type Logger struct {
	infoLogger  *log.Logger
	errorLogger *log.Logger
	enabled     bool
}

// Global logger instance
var AppLogger = NewLogger(os.Stdout, os.Stderr, true)

// NewLogger creates a new logger instance
func NewLogger(infoOut, errorOut io.Writer, enabled bool) *Logger {
	return &Logger{
		infoLogger:  log.New(infoOut, "", 0),
		errorLogger: log.New(errorOut, "ERROR: ", log.LstdFlags),
		enabled:     enabled,
	}
}

// SetEnabled enables or disables logging
func (l *Logger) SetEnabled(enabled bool) {
	l.enabled = enabled
}

// Printf logs formatted output (like fmt.Printf)
func (l *Logger) Printf(format string, v ...interface{}) {
	if l.enabled {
		l.infoLogger.Output(2, fmt.Sprintf(format, v...))
	}
}

// Println logs output with newline (like fmt.Println)
func (l *Logger) Println(v ...interface{}) {
	if l.enabled {
		l.infoLogger.Output(2, fmt.Sprintln(v...))
	}
}

// Errorf logs an error
func (l *Logger) Errorf(format string, v ...interface{}) {
	if l.enabled {
		l.errorLogger.Output(2, fmt.Sprintf(format, v...))
	}
}

// DisableForTesting disables the global logger for testing
func DisableLoggingForTesting() {
	AppLogger.SetEnabled(false)
}

// EnableLogging re-enables the global logger
func EnableLogging() {
	AppLogger.SetEnabled(true)
}