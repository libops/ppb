package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libops/ppb/pkg/config"
	"github.com/libops/ppb/pkg/machine"
)

func TestStartPingRoutine_Integration(t *testing.T) {
	// Track ping requests
	var pingCount int
	var pingURLs []string
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		pingCount++
		pingURLs = append(pingURLs, r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("pong"))
		if err != nil {
			slog.Error("Unable to write ping response", "err", err)
			os.Exit(1)
		}
	})
	server := &http.Server{
		Addr:    ":8808",
		Handler: mux,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Ignore "address already in use" errors during testing
			if !strings.Contains(err.Error(), "address already in use") {
				t.Errorf("Mock server error: %v", err)
			}
		}
	}()

	time.Sleep(1 * time.Second)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(ctx)
		if err != nil {
			slog.Error("Server shutdown failed", "err", err)
			os.Exit(1)
		}
	}()

	mockMachine := machine.NewGceMachine()
	mockMachine.SetHostForTesting("127.0.0.1")

	config := &config.Config{
		Machine: mockMachine,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)

	go startPingRoutine(ctx, &wg, config, 100*time.Millisecond)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if pingCount == 0 {
		t.Error("Expected at least one ping request, got none")
	}

	// Verify all requests were to /ping endpoint
	for _, url := range pingURLs {
		if url != "/ping" {
			t.Errorf("Expected ping to /ping endpoint, got %s", url)
		}
	}

	// Should have made multiple pings (at least 2-3 in 350ms with 100ms interval)
	if pingCount < 2 {
		t.Errorf("Expected at least 2 ping requests, got %d", pingCount)
	}
}

func TestStartPingRoutine_ContextCancellation(t *testing.T) {
	mockMachine := machine.NewGceMachine()
	mockMachine.SetHostForTesting("127.0.0.1")

	config := &config.Config{
		Machine: mockMachine,
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	routineFinished := make(chan bool, 1)

	go func() {
		startPingRoutine(ctx, &wg, config, 50*time.Millisecond)
		routineFinished <- true
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	wg.Wait()

	// Verify the routine actually finished
	select {
	case <-routineFinished:
	case <-time.After(1 * time.Second):
		t.Error("Ping routine did not finish within expected time after context cancellation")
	}
}
