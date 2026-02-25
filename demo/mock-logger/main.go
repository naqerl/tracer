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
	infoMessages = []string{
		"User login successful",
		"HTTP GET request completed",
		"HTTP POST request completed",
		"Database query executed",
		"Cache hit for user session",
		"Cache miss for config data",
		"Background job started",
		"Background job completed",
		"File uploaded to S3",
		"File downloaded from CDN",
		"Email sent to customer",
		"Push notification delivered",
		"Payment processed",
		"Order created",
		"Order fulfilled",
		"Webhook delivered",
		"API key generated",
		"Session refreshed",
		"Config reloaded",
		"Health check passed",
		"Worker picked up job",
		"Queue item processed",
		"Index updated",
		"Migration completed",
		"Backup finished",
		"Search query executed",
		"Report generated",
		"Export completed",
		"Import finished",
		"Sync successful",
	}

	warnMessages = []string{
		"Slow query detected",
		"High memory usage",
		"High CPU usage",
		"Connection pool near limit",
		"Cache eviction rate high",
		"Retry attempt for failed request",
		"Deprecated API endpoint called",
		"Rate limit approaching",
		"Disk space low",
		"Queue backlog growing",
		"Response time degraded",
		"Database replication lag",
		"Token expires soon",
		"SSL certificate expires in 7 days",
		"Service health check slow",
	}

	errorMessages = []string{
		"Database connection failed",
		"Authentication failed",
		"Authorization denied",
		"Request timeout",
		"Internal server error",
		"Service unavailable",
		"Rate limit exceeded",
		"Invalid request payload",
		"Resource not found",
		"Constraint violation",
		"Deadlock detected",
		"Out of memory",
		"Network unreachable",
		"DNS resolution failed",
		"SSL handshake failed",
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
	entriesPerMessage := flag.Int("per-message", 100, "number of entries per message")
	flag.Parse()

	file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Generate some trace IDs to reuse (simulates request flows)
	traceIDs := make([]string, 20)
	for i := range traceIDs {
		traceIDs[i] = fmt.Sprintf("trace-%s", randomString(16))
	}

	var entries []LogEntry

	// Generate entries for each message
	for _, msg := range infoMessages {
		for i := 0; i < *entriesPerMessage; i++ {
			entries = append(entries, generateEntry("info", msg, traceIDs))
		}
	}
	for _, msg := range warnMessages {
		for i := 0; i < *entriesPerMessage; i++ {
			entries = append(entries, generateEntry("warn", msg, traceIDs))
		}
	}
	for _, msg := range errorMessages {
		for i := 0; i < *entriesPerMessage; i++ {
			entries = append(entries, generateEntry("error", msg, traceIDs))
		}
	}

	// Shuffle entries to interleave levels and messages
	rand.Shuffle(len(entries), func(i, j int) {
		entries[i], entries[j] = entries[j], entries[i]
	})

	// Write all entries
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		fmt.Fprintln(file, string(data))
	}
	file.Sync()

	fmt.Printf("Generated %d log entries (%d per message)\n", len(entries), *entriesPerMessage)
	fmt.Printf("Output written to %s\n", *logFile)
}

func generateEntry(level, message string, traceIDs []string) LogEntry {
	entry := LogEntry{
		Timestamp: time.Now().Add(-time.Duration(rand.Intn(3600)) * time.Second),
		Level:     level,
		Message:   message,
	}

	// Add trace ID sometimes (60% chance)
	if rand.Float64() < 0.6 {
		entry.TraceID = traceIDs[rand.Intn(len(traceIDs))]
	}

	// Add user ID sometimes
	if rand.Float64() < 0.4 {
		entry.UserID = fmt.Sprintf("user-%d", rand.Intn(1000))
	}

	// Add request ID for API logs
	if rand.Float64() < 0.3 {
		entry.RequestID = fmt.Sprintf("req-%s", randomString(8))
	}

	// Add duration for some logs
	if rand.Float64() < 0.5 {
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
