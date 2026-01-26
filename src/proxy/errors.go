package proxy

import (
	"fmt"
	"log"
	"runtime/debug"
)

// logErrorWithStack logs an error with full stack trace
func logErrorWithStack(err error, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if err != nil {
		message = fmt.Sprintf("%s: %v", message, err)
	}
	
	stack := string(debug.Stack())
	log.Printf("%s\nStack trace:\n%s", message, stack)
}

// logErrorfWithStack logs a formatted error with full stack trace
func logErrorfWithStack(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	stack := string(debug.Stack())
	log.Printf("%s\nStack trace:\n%s", message, stack)
}

// handleErrorWithStack handles an error by logging it with stack trace
func handleErrorWithStack(err error, context string) {
	if err != nil {
		logErrorWithStack(err, "%s", context)
	}
}
