package proxy

import (
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

			if transport.DialTLSContext == nil {
				t.Error("DialTLSContext is nil")
			}
		})
	}
}

func TestReverseProxy_SetHost(t *testing.T) {
	// Create a mock machine with a known host
	machine := machine.NewGceMachine()

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
		Machine: machine,
	}

	proxy := New(config)
	proxy.SetHost()

	// Verify the target host format is correct when machine has a host
	// This test is limited without being able to mock the machine host
	if proxy.Target == nil {
		t.Error("Target is nil after SetHost()")
	}
}
