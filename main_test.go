package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libops/ppb/pkg/config"
	"github.com/libops/ppb/pkg/machine"
)

func TestHandlerPowerFailureReturnsRetryableUnavailable(t *testing.T) {
	t.Parallel()

	_, allowed, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatalf("net.ParseCIDR() error = %v", err)
	}
	backendCalled := false
	handler := newHandler(&config.Config{
		AllowedIps:      []config.IPNet{{IPNet: allowed}},
		PowerOnCooldown: 30,
		PowerOnTimeout:  1,
		Machine:         &machine.GoogleComputeEngine{},
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		backendCalled = true
	}))
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if backendCalled {
		t.Fatal("backend handler ran after power-on failure")
	}
}

func TestHandlerCanonicalizesValidatedClientIdentity(t *testing.T) {
	t.Parallel()

	_, allowed, err := net.ParseCIDR("203.0.113.9/32")
	if err != nil {
		t.Fatalf("net.ParseCIDR() error = %v", err)
	}
	machine := machine.NewGceMachine()
	machine.SetHostForTesting("10.42.0.8")
	machine.LastPowerOnAttempt = time.Now()

	backendCalled := false
	handler := newHandler(&config.Config{
		AllowedIps:        []config.IPNet{{IPNet: allowed}},
		IpForwardedHeader: "X-Forwarded-For",
		PowerOnCooldown:   30,
		PowerOnTimeout:    1,
		Machine:           machine,
	}, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		backendCalled = true
		if got := request.Header.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "203.0.113.9" {
			t.Errorf("X-Forwarded-For = %#v, want one validated client address", got)
		}
		if got := request.Header.Get("X-Real-IP"); got != "" {
			t.Errorf("X-Real-IP = %q, want stripped", got)
		}
		if got := request.Header.Get("Forwarded"); got != "" {
			t.Errorf("Forwarded = %q, want stripped", got)
		}
	}))
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.Header.Add("X-Forwarded-For", "10.0.0.8")
	request.Header.Add("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Real-IP", "10.0.0.8")
	request.Header.Set("Forwarded", "for=10.0.0.8")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !backendCalled {
		t.Fatal("backend handler was not called")
	}
}

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
