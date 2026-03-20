package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// DenialEvent represents a single egress denial event.
type DenialEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
}

// DenialTracker stores denial events for the /denials endpoint.
type DenialTracker struct {
	mu      sync.RWMutex
	denials []DenialEvent
}

// Add records a new denial event.
func (d *DenialTracker) Add(event DenialEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.denials = append(d.denials, event)
	// Keep only the last 1000 denials
	if len(d.denials) > 1000 {
		d.denials = d.denials[len(d.denials)-1000:]
	}
}

// GetAll returns all recorded denial events.
func (d *DenialTracker) GetAll() []DenialEvent {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]DenialEvent, len(d.denials))
	copy(result, d.denials)
	return result
}

// StartHealthEndpoints starts HTTP endpoints for health checks.
func StartHealthEndpoints(tracker *DenialTracker, port int) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	mux.HandleFunc("/denials", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		denials := tracker.GetAll()
		if err := json.NewEncoder(w).Encode(denials); err != nil {
			log.Printf("ERROR: encode denials: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("INFO: health endpoints listening on %s", addr)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("ERROR: health server: %v", err)
		}
	}()
}
