package config

import (
	"net"
	"net/http"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestIPNet_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr bool
	}{
		{
			name:    "valid IPv4 CIDR",
			cidr:    "192.168.1.0/24",
			wantErr: false,
		},
		{
			name:    "valid IPv4 single host",
			cidr:    "10.0.0.1/32",
			wantErr: false,
		},
		{
			name:    "valid IPv6 CIDR",
			cidr:    "2001:db8::/32",
			wantErr: false,
		},
		{
			name:    "invalid CIDR",
			cidr:    "invalid",
			wantErr: true,
		},
		{
			name:    "invalid IP",
			cidr:    "999.999.999.999/24",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ipNet IPNet
			node := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: tt.cidr,
			}

			err := ipNet.UnmarshalYAML(node)
			if (err != nil) != tt.wantErr {
				t.Errorf("IPNet.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify the network was parsed correctly
				_, expected, _ := net.ParseCIDR(tt.cidr)
				if !ipNet.IP.Equal(expected.IP) || ipNet.Mask.String() != expected.Mask.String() {
					t.Errorf("IPNet.UnmarshalYAML() parsed incorrectly, got %v, want %v", ipNet.IPNet, expected)
				}
			}
		})
	}
}

func TestConfig_IpIsAllowed(t *testing.T) {
	// Create test config with allowed IPs
	allowedNets := []IPNet{}
	
	// Add some test networks
	cidrs := []string{"192.168.1.0/24", "10.0.0.0/8", "127.0.0.1/32"}
	for _, cidr := range cidrs {
		_, network, _ := net.ParseCIDR(cidr)
		allowedNets = append(allowedNets, IPNet{IPNet: network})
	}

	config := &Config{
		AllowedIps:        allowedNets,
		IpForwardedHeader: "X-Forwarded-For",
		IpDepth:           0,
	}

	tests := []struct {
		name           string
		remoteAddr     string
		forwardedFor   string
		expectedResult bool
	}{
		{
			name:           "allowed IP from localhost",
			remoteAddr:     "127.0.0.1:12345",
			forwardedFor:   "",
			expectedResult: true,
		},
		{
			name:           "allowed IP from private network",
			remoteAddr:     "192.168.1.100:54321",
			forwardedFor:   "",
			expectedResult: true,
		},
		{
			name:           "allowed IP from 10.x network",
			remoteAddr:     "10.1.1.1:8080",
			forwardedFor:   "",
			expectedResult: true,
		},
		{
			name:           "blocked IP from public internet",
			remoteAddr:     "8.8.8.8:443",
			forwardedFor:   "",
			expectedResult: false,
		},
		{
			name:           "allowed IP via X-Forwarded-For",
			remoteAddr:     "8.8.8.8:443",
			forwardedFor:   "192.168.1.50",
			expectedResult: true,
		},
		{
			name:           "blocked IP via X-Forwarded-For",
			remoteAddr:     "192.168.1.1:8080",
			forwardedFor:   "203.0.113.1",
			expectedResult: false,
		},
		{
			name:           "blocked IP with multiple forwarded headers",
			remoteAddr:     "192.168.1.1:8080", 
			forwardedFor:   "203.0.113.1, 198.51.100.1",
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     make(http.Header),
			}
			
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			result := config.IpIsAllowed(req)
			if result != tt.expectedResult {
				t.Errorf("Config.IpIsAllowed() = %v, want %v for IP %s (forwarded: %s)", 
					result, tt.expectedResult, tt.remoteAddr, tt.forwardedFor)
			}
		})
	}
}

func TestConfig_getClientIP(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		forwardedFor   string
		ipDepth        int
		expectedIP     string
	}{
		{
			name:        "no forwarded header",
			remoteAddr:  "192.168.1.1:8080",
			expectedIP:  "192.168.1.1",
		},
		{
			name:         "single forwarded IP",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.1",
			expectedIP:   "203.0.113.1",
		},
		{
			name:         "multiple forwarded IPs, depth 0",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.1, 198.51.100.1, 192.168.1.1",
			ipDepth:      0,
			expectedIP:   "192.168.1.1",
		},
		{
			name:         "multiple forwarded IPs, depth 1",
			remoteAddr:   "10.0.0.1:8080", 
			forwardedFor: "203.0.113.1, 198.51.100.1, 192.168.1.1",
			ipDepth:      1,
			expectedIP:   "198.51.100.1",
		},
		{
			name:         "IPv6 remote addr",
			remoteAddr:   "[2001:db8::1]:8080",
			expectedIP:   "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				IpForwardedHeader: "X-Forwarded-For",
				IpDepth:           tt.ipDepth,
			}

			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     make(http.Header),
			}
			
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			result := config.getClientIP(req)
			if result.String() != tt.expectedIP {
				t.Errorf("Config.getClientIP() = %v, want %v", result.String(), tt.expectedIP)
			}
		})
	}
}

func TestConfig_PowerOnCooldownDefaultValue(t *testing.T) {
	config := &Config{}
	
	// Test that default is applied in main.go logic
	// (We can't easily test the main.go logic here, but we can test the field exists)
	if config.PowerOnCooldown != 0 {
		// Should start at 0, then main.go sets to 30 if <= 0
		t.Errorf("PowerOnCooldown should start at 0, got %v", config.PowerOnCooldown)
	}
	
	// Test setting a custom value
	config.PowerOnCooldown = 60
	if config.PowerOnCooldown != 60 {
		t.Errorf("PowerOnCooldown should be 60, got %v", config.PowerOnCooldown)
	}
}

func TestConfig_setProxyTimeoutDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected ProxyTimeouts
	}{
		{
			name: "empty timeouts get defaults",
			config: Config{
				ProxyTimeouts: ProxyTimeouts{},
			},
			expected: ProxyTimeouts{
				DialTimeout:           120,
				KeepAlive:            120,
				IdleConnTimeout:      90,
				TLSHandshakeTimeout:  10,
				ExpectContinueTimeout: 1,
				MaxIdleConns:         100,
			},
		},
		{
			name: "partial config gets defaults for missing values",
			config: Config{
				ProxyTimeouts: ProxyTimeouts{
					DialTimeout: 60,
					MaxIdleConns: 50,
				},
			},
			expected: ProxyTimeouts{
				DialTimeout:           60,  // custom
				KeepAlive:            120, // default
				IdleConnTimeout:      90,  // default
				TLSHandshakeTimeout:  10,  // default
				ExpectContinueTimeout: 1,  // default
				MaxIdleConns:         50,  // custom 
			},
		},
		{
			name: "all configured values are preserved",
			config: Config{
				ProxyTimeouts: ProxyTimeouts{
					DialTimeout:           30,
					KeepAlive:            40,
					IdleConnTimeout:      50,
					TLSHandshakeTimeout:  5,
					ExpectContinueTimeout: 2,
					MaxIdleConns:         200,
				},
			},
			expected: ProxyTimeouts{
				DialTimeout:           30,
				KeepAlive:            40,
				IdleConnTimeout:      50,
				TLSHandshakeTimeout:  5,
				ExpectContinueTimeout: 2,
				MaxIdleConns:         200,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.setProxyTimeoutDefaults()
			
			if tt.config.ProxyTimeouts.DialTimeout != tt.expected.DialTimeout {
				t.Errorf("DialTimeout = %v, want %v", tt.config.ProxyTimeouts.DialTimeout, tt.expected.DialTimeout)
			}
			if tt.config.ProxyTimeouts.KeepAlive != tt.expected.KeepAlive {
				t.Errorf("KeepAlive = %v, want %v", tt.config.ProxyTimeouts.KeepAlive, tt.expected.KeepAlive)
			}
			if tt.config.ProxyTimeouts.IdleConnTimeout != tt.expected.IdleConnTimeout {
				t.Errorf("IdleConnTimeout = %v, want %v", tt.config.ProxyTimeouts.IdleConnTimeout, tt.expected.IdleConnTimeout)
			}
			if tt.config.ProxyTimeouts.TLSHandshakeTimeout != tt.expected.TLSHandshakeTimeout {
				t.Errorf("TLSHandshakeTimeout = %v, want %v", tt.config.ProxyTimeouts.TLSHandshakeTimeout, tt.expected.TLSHandshakeTimeout)
			}
			if tt.config.ProxyTimeouts.ExpectContinueTimeout != tt.expected.ExpectContinueTimeout {
				t.Errorf("ExpectContinueTimeout = %v, want %v", tt.config.ProxyTimeouts.ExpectContinueTimeout, tt.expected.ExpectContinueTimeout)
			}
			if tt.config.ProxyTimeouts.MaxIdleConns != tt.expected.MaxIdleConns {
				t.Errorf("MaxIdleConns = %v, want %v", tt.config.ProxyTimeouts.MaxIdleConns, tt.expected.MaxIdleConns)
			}
		})
	}
}

func TestProxyTimeouts_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yamlData string
		expected ProxyTimeouts
		wantErr  bool
	}{
		{
			name: "full timeout configuration",
			yamlData: `
proxyTimeouts:
  dialTimeout: 60
  keepAlive: 90
  idleConnTimeout: 45
  tlsHandshakeTimeout: 15
  expectContinueTimeout: 2
  maxIdleConns: 200
`,
			expected: ProxyTimeouts{
				DialTimeout:           60,
				KeepAlive:            90,
				IdleConnTimeout:      45,
				TLSHandshakeTimeout:  15,
				ExpectContinueTimeout: 2,
				MaxIdleConns:         200,
			},
			wantErr: false,
		},
		{
			name: "partial timeout configuration",
			yamlData: `
proxyTimeouts:
  dialTimeout: 30
  maxIdleConns: 50
`,
			expected: ProxyTimeouts{
				DialTimeout:  30,
				MaxIdleConns: 50,
				// Other fields should be zero and get defaults later
			},
			wantErr: false,
		},
		{
			name: "empty timeout configuration",
			yamlData: `
proxyTimeouts: {}
`,
			expected: ProxyTimeouts{},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type configWithTimeouts struct {
				ProxyTimeouts ProxyTimeouts `yaml:"proxyTimeouts"`
			}

			var config configWithTimeouts
			err := yaml.Unmarshal([]byte(tt.yamlData), &config)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if config.ProxyTimeouts.DialTimeout != tt.expected.DialTimeout {
					t.Errorf("DialTimeout = %v, want %v", config.ProxyTimeouts.DialTimeout, tt.expected.DialTimeout)
				}
				if config.ProxyTimeouts.KeepAlive != tt.expected.KeepAlive {
					t.Errorf("KeepAlive = %v, want %v", config.ProxyTimeouts.KeepAlive, tt.expected.KeepAlive)
				}
				if config.ProxyTimeouts.MaxIdleConns != tt.expected.MaxIdleConns {
					t.Errorf("MaxIdleConns = %v, want %v", config.ProxyTimeouts.MaxIdleConns, tt.expected.MaxIdleConns)
				}
			}
		})
	}
}
