// Package handlers provides HTTP handlers and metrics for the Go CDK wrapper API server.
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// RequestMetrics tracks request counts and latencies per endpoint.
type RequestMetrics struct {
	mu       sync.RWMutex
	counts   map[string]int64
	latency  map[string]time.Duration
	errors   map[string]int64
}

// NewRequestMetrics creates a new metrics tracker.
func NewRequestMetrics() *RequestMetrics {
	return &RequestMetrics{
		counts:  make(map[string]int64),
		latency: make(map[string]time.Duration),
		errors:  make(map[string]int64),
	}
}

// Record increments the count and tracks latency for an endpoint.
func (m *RequestMetrics) Record(endpoint string, duration time.Duration, isError bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[endpoint]++
	m.latency[endpoint] += duration
	if isError {
		m.errors[endpoint]++
	}
}

// GetSnapshot returns a copy of current metrics.
func (m *RequestMetrics) GetSnapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := make(map[string]interface{})
	for endpoint, count := range m.counts {
		avgLatency := time.Duration(0)
		if count > 0 {
			avgLatency = m.latency[endpoint] / time.Duration(count)
		}
		snapshot[endpoint] = map[string]interface{}{
			"count":         count,
			"avg_latency_ms": avgLatency.Milliseconds(),
			"errors":        m.errors[endpoint],
		}
	}
	return snapshot
}

// Global metrics instance
var globalMetrics = NewRequestMetrics()

// MetricsMiddleware wraps an http.Handler to track request metrics.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		endpoint := r.URL.Path

		// Wrap response writer to capture status code
		wrapped := &statusCaptureWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)
		isError := wrapped.statusCode >= 400
		globalMetrics.Record(endpoint, duration, isError)

		slog.Info("request",
			"method", r.Method,
			"path", endpoint,
			"status", wrapped.statusCode,
			"duration_ms", duration.Milliseconds(),
			"server", "go-native",
		)
	})
}

// statusCaptureWriter captures the HTTP status code.
type statusCaptureWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// MetricsHandler returns current metrics as JSON.
func MetricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	snapshot := globalMetrics.GetSnapshot()

	// Add server info
	result := map[string]interface{}{
		"server":    "go-native",
		"uptime":    "see logs",
		"endpoints": snapshot,
	}

	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
