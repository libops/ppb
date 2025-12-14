package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libops/ppb/pkg/config"
	"github.com/libops/ppb/pkg/proxy"
)

func init() {
	level := slog.LevelInfo
	ll := os.Getenv("LOG_LEVEL")
	switch strings.ToUpper(ll) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(handler)
}

func startPingRoutine(ctx context.Context, wg *sync.WaitGroup, c *config.Config, interval time.Duration) {
	defer wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("Starting ping routine to GCE instance", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Ping routine shutting down")
			return
		case <-ticker.C:
			host := c.Machine.Host()
			if host == "" {
				slog.Debug("No GCE host IP available for ping")
				continue
			}

			pingURL := fmt.Sprintf("http://%s:8808/ping", host)
			slog.Debug("Pinging GCE instance", "url", pingURL)

			client := &http.Client{
				Timeout: 5 * time.Second,
			}

			resp, err := client.Get(pingURL)
			if err != nil {
				slog.Debug("Ping failed", "url", pingURL, "error", err)
				continue
			}
			resp.Body.Close()

			slog.Debug("Ping successful", "url", pingURL, "status", resp.StatusCode)
		}
	}
}

func main() {
	c, err := config.LoadConfig()
	if err != nil {
		slog.Error("Unable to load config", "err", err)
		os.Exit(1)
	}

	slog.Debug("Loaded config", "config", c)

	// Set default cooldown interval if not specified
	if c.PowerOnCooldown <= 0 {
		c.PowerOnCooldown = 30
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	var wg sync.WaitGroup
	wg.Add(1)
	go startPingRoutine(ctx, &wg, c, 30*time.Second)

	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "OK")
	})

	p := proxy.New(c)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !c.IpIsAllowed(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// Attempt to power on machine with cooldown protection
		reqCtx := context.Background()
		err := c.Machine.PowerOnWithCooldown(reqCtx, c.PowerOnCooldown)
		if err != nil {
			slog.Error("Power-on attempt failed", "err", err)
			http.Error(w, "Backend not available", http.StatusServiceUnavailable)
			return
		}

		p.SetRequestHeaders(r)
		p.SetHost()
		p.ServeHTTP(w, r)
	})

	server := &http.Server{Addr: ":8080"}
	go func() {
		slog.Info("Server listening on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "err", err)
		}
	}()

	<-sigChan
	slog.Info("Received shutdown signal, gracefully shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server shutdown error", "err", err)
	}

	wg.Wait()
	slog.Info("Shutdown complete")
}
