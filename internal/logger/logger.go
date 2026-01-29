package logger

import (
	"log"
	"strings"
	"sync"
)

// Level represents a log level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	currentLevel Level = LevelInfo
	mu           sync.RWMutex
)

// SetLevel sets the global log level from a string.
// Valid values: "debug", "info", "warn", "error".
func SetLevel(level string) {
	mu.Lock()
	defer mu.Unlock()

	switch strings.ToLower(level) {
	case "debug":
		currentLevel = LevelDebug
	case "info":
		currentLevel = LevelInfo
	case "warn":
		currentLevel = LevelWarn
	case "error":
		currentLevel = LevelError
	default:
		currentLevel = LevelInfo
	}
	log.Printf("[INFO] Log level set to: %s", strings.ToLower(level))
}

func getLevel() Level {
	mu.RLock()
	defer mu.RUnlock()
	return currentLevel
}

// Debugf logs a debug message.
func Debugf(format string, args ...interface{}) {
	if getLevel() <= LevelDebug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Infof logs an info message.
func Infof(format string, args ...interface{}) {
	if getLevel() <= LevelInfo {
		log.Printf("[INFO] "+format, args...)
	}
}

// Warnf logs a warning message.
func Warnf(format string, args ...interface{}) {
	if getLevel() <= LevelWarn {
		log.Printf("[WARN] "+format, args...)
	}
}

// Errorf logs an error message.
func Errorf(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}
