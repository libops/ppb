package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

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
		ctx := context.Background()
		err := c.Machine.PowerOnWithCooldown(ctx, c.PowerOnCooldown)
		if err != nil {
			slog.Error("Power-on attempt failed", "err", err)
			http.Error(w, "Backend not available", http.StatusServiceUnavailable)
			return
		}
		
		p.SetHost()
		slog.Info(r.Method, "path", r.URL.Path, "host", r.Host)
		p.ServeHTTP(w, r)
	})

	slog.Info("Server listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
