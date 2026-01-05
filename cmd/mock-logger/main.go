package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"
)

var (
	levels   = []string{"debug", "info", "info", "info", "warn", "error"}
	messages = []string{
		"User logged in successfully",
		"Processing request",
		"Database query completed",
		"Cache hit for key",
		"API response sent",
		"Background job started",
		"File uploaded",
		"Email sent to user",
		"Connection established",
		"Request validation passed",
		"Session created",
		"Payment processed",
		"Order confirmed",
		"Notification dispatched",
		"Data synchronized",
	}
	errorMessages = []string{
		"Database connection failed",
		"Authentication error: invalid token",
		"Request timeout exceeded",
		"File not found",
		"Permission denied",
		"Invalid input data",
		"Service unavailable",
		"Rate limit exceeded",
		"Memory allocation failed",
		"Network unreachable",
	}
	warnMessages = []string{
		"High memory usage detected",
		"Slow query detected",
		"Deprecated API called",
		"Connection pool near limit",
		"Retry attempt",
		"Cache miss",
		"Queue backlog growing",
	}
	sources = []string{
		"/var/log/app/api.log",
		"/var/log/app/worker.log",
		"/var/log/app/scheduler.log",
	}
)

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	TraceID   string    `json:"trace_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Duration  int       `json:"duration_ms,omitempty"`
}

func main() {
	logFile := flag.String("output", "/tmp/tracer-mock.log", "output log file path")
	interval := flag.Duration("interval", 500*time.Millisecond, "interval between log entries")
	flag.Parse()

	file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	fmt.Printf("Mock logger started, writing to %s every %v\n", *logFile, *interval)

	// Generate some trace IDs to reuse (simulates request flows)
	traceIDs := make([]string, 10)
	for i := range traceIDs {
		traceIDs[i] = fmt.Sprintf("trace-%s", randomString(16))
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for range ticker.C {
		entry := generateLogEntry(traceIDs)
		data, _ := json.Marshal(entry)
		fmt.Fprintln(file, string(data))
		file.Sync()

		// Print to stdout for visibility
		fmt.Printf("[%s] %s: %s\n", entry.Level, entry.Timestamp.Format("15:04:05"), entry.Message)
	}
}

func generateLogEntry(traceIDs []string) LogEntry {
	level := levels[rand.Intn(len(levels))]

	var message string
	switch level {
	case "error":
		message = errorMessages[rand.Intn(len(errorMessages))]
	case "warn":
		message = warnMessages[rand.Intn(len(warnMessages))]
	default:
		message = messages[rand.Intn(len(messages))]
	}

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}

	// Add trace ID sometimes (70% chance)
	if rand.Float64() < 0.7 {
		entry.TraceID = traceIDs[rand.Intn(len(traceIDs))]
	}

	// Add user ID sometimes
	if rand.Float64() < 0.5 {
		entry.UserID = fmt.Sprintf("user-%d", rand.Intn(1000))
	}

	// Add request ID for API logs
	if rand.Float64() < 0.3 {
		entry.RequestID = fmt.Sprintf("req-%s", randomString(8))
	}

	// Add duration for some logs
	if rand.Float64() < 0.4 {
		entry.Duration = rand.Intn(2000)
	}

	return entry
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
