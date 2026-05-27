package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/libops/ppb/pkg/config"
	"github.com/libops/ppb/pkg/machine"
)

func TestNew_UsesConfiguredTimeouts(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		expected struct {
			dialTimeout           time.Duration
			keepAlive             time.Duration
			idleConnTimeout       time.Duration
			tlsHandshakeTimeout   time.Duration
			expectContinueTimeout time.Duration
			maxIdleConns          int
		}
	}{
		{
			name: "default timeout values",
			config: &config.Config{
				Scheme: "http",
				Port:   80,
				ProxyTimeouts: config.ProxyTimeouts{
					DialTimeout:           120,
					KeepAlive:             120,
					IdleConnTimeout:       90,
					TLSHandshakeTimeout:   10,
					ExpectContinueTimeout: 1,
					MaxIdleConns:          100,
				},
				Machine: machine.NewGceMachine(),
			},
			expected: struct {
				dialTimeout           time.Duration
				keepAlive             time.Duration
				idleConnTimeout       time.Duration
				tlsHandshakeTimeout   time.Duration
				expectContinueTimeout time.Duration
				maxIdleConns          int
			}{
				dialTimeout:           120 * time.Second,
				keepAlive:             120 * time.Second,
				idleConnTimeout:       90 * time.Second,
				tlsHandshakeTimeout:   10 * time.Second,
				expectContinueTimeout: 1 * time.Second,
				maxIdleConns:          100,
			},
		},
		{
			name: "custom timeout values",
			config: &config.Config{
				Scheme: "https",
				Port:   443,
				ProxyTimeouts: config.ProxyTimeouts{
					DialTimeout:           60,
					KeepAlive:             90,
					IdleConnTimeout:       45,
					TLSHandshakeTimeout:   15,
					ExpectContinueTimeout: 2,
					MaxIdleConns:          200,
				},
				Machine: machine.NewGceMachine(),
			},
			expected: struct {
				dialTimeout           time.Duration
				keepAlive             time.Duration
				idleConnTimeout       time.Duration
				tlsHandshakeTimeout   time.Duration
				expectContinueTimeout time.Duration
				maxIdleConns          int
			}{
				dialTimeout:           60 * time.Second,
				keepAlive:             90 * time.Second,
				idleConnTimeout:       45 * time.Second,
				tlsHandshakeTimeout:   15 * time.Second,
				expectContinueTimeout: 2 * time.Second,
				maxIdleConns:          200,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := New(tt.config)

			if proxy == nil {
				t.Fatal("New() returned nil")
			}

			if proxy.Target.Scheme != tt.config.Scheme {
				t.Errorf("Target.Scheme = %v, want %v", proxy.Target.Scheme, tt.config.Scheme)
			}

			transport := proxy.Transport
			if transport == nil {
				t.Fatal("Transport is nil")
			}

			if transport.IdleConnTimeout != tt.expected.idleConnTimeout {
				t.Errorf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, tt.expected.idleConnTimeout)
			}

			if transport.TLSHandshakeTimeout != tt.expected.tlsHandshakeTimeout {
				t.Errorf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, tt.expected.tlsHandshakeTimeout)
			}

			if transport.ExpectContinueTimeout != tt.expected.expectContinueTimeout {
				t.Errorf("ExpectContinueTimeout = %v, want %v", transport.ExpectContinueTimeout, tt.expected.expectContinueTimeout)
			}

			if transport.MaxIdleConns != tt.expected.maxIdleConns {
				t.Errorf("MaxIdleConns = %v, want %v", transport.MaxIdleConns, tt.expected.maxIdleConns)
			}

			if transport.DialContext == nil {
				t.Error("DialContext is nil")
			}
		})
	}
}

func TestReverseProxy_ServeHTTPUsesRequestLocalTargetAndHeaders(t *testing.T) {
	var gotForwardedFor string
	var gotForwardedHost string
	var gotTrace string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwardedFor = r.Header.Get("X-Forwarded-For")
		gotForwardedHost = r.Header.Get("X-Forwarded-Host")
		gotTrace = r.Header.Get("X-Cloud-Trace-Context")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	host, portString, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatalf("split backend host: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}

	machine := machine.NewGceMachine()
	machine.SetHostForTesting(host)

	config := &config.Config{
		Scheme: backendURL.Scheme,
		Port:   port,
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           120,
			KeepAlive:             120,
			IdleConnTimeout:       90,
			TLSHandshakeTimeout:   10,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          100,
		},
		Machine: machine,
	}

	proxy := New(config)
	req := httptest.NewRequest(http.MethodGet, "http://customer.example/path", nil)
	req.Host = "customer.example"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	req.Header.Set("X-Cloud-Trace-Context", "trace-id")
	resp := httptest.NewRecorder()

	proxy.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("ServeHTTP status = %d, want %d", resp.Code, http.StatusNoContent)
	}
	if gotForwardedFor != "198.51.100.8" {
		t.Errorf("X-Forwarded-For = %q, want %q", gotForwardedFor, "198.51.100.8")
	}
	if gotForwardedHost != "customer.example" {
		t.Errorf("X-Forwarded-Host = %q, want %q", gotForwardedHost, "customer.example")
	}
	if gotTrace != "trace-id" {
		t.Errorf("X-Cloud-Trace-Context = %q, want %q", gotTrace, "trace-id")
	}
}

func TestReverseProxy_ServeHTTPReturnsUnavailableWhenHostMissing(t *testing.T) {
	config := &config.Config{
		Scheme: "http",
		Port:   8080,
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           120,
			KeepAlive:             120,
			IdleConnTimeout:       90,
			TLSHandshakeTimeout:   10,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          100,
		},
		Machine: machine.NewGceMachine(),
	}

	req := httptest.NewRequest(http.MethodGet, "http://customer.example/path", nil)
	resp := httptest.NewRecorder()
	New(config).ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("ServeHTTP status = %d, want %d", resp.Code, http.StatusServiceUnavailable)
	}
}

func TestReverseProxy_TargetURLUsesProxyTargetOverride(t *testing.T) {
	config := &config.Config{
		Scheme: "http",
		Port:   8080,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   "localhost",
			Port:   9000,
		},
		Machine: machine.NewGceMachine(),
	}

	proxy := New(config)
	target, ok := proxy.targetURL()
	if !ok {
		t.Fatal("targetURL ok = false, want true")
	}

	if target.Host != "localhost:9000" {
		t.Errorf("Target.Host = %q, want localhost:9000", target.Host)
	}
	if target.Scheme != "http" {
		t.Errorf("Target.Scheme = %q, want http", target.Scheme)
	}
	if proxy.Target.Host != "" {
		t.Errorf("proxy.Target.Host = %q, want request-local target to leave shared target untouched", proxy.Target.Host)
	}
}
