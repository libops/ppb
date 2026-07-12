package config

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
			testConfig := *config
			if tt.forwardedFor == "" {
				testConfig.IpForwardedHeader = ""
			}
			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     make(http.Header),
			}

			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			result := testConfig.IpIsAllowed(req)
			if result != tt.expectedResult {
				t.Errorf("Config.IpIsAllowed() = %v, want %v for IP %s (forwarded: %s)",
					result, tt.expectedResult, tt.remoteAddr, tt.forwardedFor)
			}
		})
	}
}

func TestConfig_getClientIP(t *testing.T) {
	tests := []struct {
		name         string
		remoteAddr   string
		forwardedFor string
		ipDepth      int
		expectedIP   string
		useHeader    bool
		wantErr      bool
	}{
		{
			name:       "no forwarded header",
			remoteAddr: "192.168.1.1:8080",
			expectedIP: "192.168.1.1",
		},
		{
			name:         "single forwarded IP",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.1",
			expectedIP:   "203.0.113.1",
			useHeader:    true,
		},
		{
			name:         "multiple forwarded IPs, depth 0",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.1, 198.51.100.1, 192.168.1.1",
			ipDepth:      0,
			expectedIP:   "192.168.1.1",
			useHeader:    true,
		},
		{
			name:         "multiple forwarded IPs, depth 1",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.1, 198.51.100.1, 192.168.1.1",
			ipDepth:      1,
			expectedIP:   "198.51.100.1",
			useHeader:    true,
		},
		{
			name:         "attacker prefix cannot replace client behind one trusted proxy",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "10.0.0.8, 203.0.113.9, 192.0.2.10",
			ipDepth:      1,
			expectedIP:   "203.0.113.9",
			useHeader:    true,
		},
		{
			name:       "missing trusted header fails closed",
			remoteAddr: "10.0.0.1:8080",
			ipDepth:    0,
			useHeader:  true,
			wantErr:    true,
		},
		{
			name:         "insufficient trusted hops fail closed",
			remoteAddr:   "10.0.0.1:8080",
			forwardedFor: "203.0.113.9",
			ipDepth:      1,
			useHeader:    true,
			wantErr:      true,
		},
		{
			name:       "IPv6 remote addr",
			remoteAddr: "[2001:db8::1]:8080",
			expectedIP: "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{IpDepth: tt.ipDepth}
			if tt.useHeader {
				config.IpForwardedHeader = "X-Forwarded-For"
			}

			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     make(http.Header),
			}

			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			result, err := config.getClientIP(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Config.getClientIP() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if result.String() != tt.expectedIP {
				t.Errorf("Config.getClientIP() = %v, want %v", result.String(), tt.expectedIP)
			}
		})
	}
}

func TestConfigGetClientIPUsesTheRightmostDuplicateHeaderValue(t *testing.T) {
	t.Parallel()

	config := &Config{
		IpForwardedHeader: "X-Forwarded-For",
		IpDepth:           0,
	}
	request := &http.Request{Header: make(http.Header)}
	request.Header.Add("X-Forwarded-For", "10.0.0.8")
	request.Header.Add("X-Forwarded-For", "203.0.113.9")

	clientIP, err := config.getClientIP(request)
	if err != nil {
		t.Fatalf("getClientIP() error = %v", err)
	}
	if got := clientIP.String(); got != "203.0.113.9" {
		t.Fatalf("getClientIP() = %s, want proxy-appended duplicate value", got)
	}
}

func TestConfig_SetPowerDefaults(t *testing.T) {
	config := &Config{}
	config.setPowerDefaults()
	if config.PowerOnCooldown != 30 || config.PowerOnTimeout != 360 {
		t.Fatalf("power defaults = cooldown %d timeout %d, want 30 and 360", config.PowerOnCooldown, config.PowerOnTimeout)
	}

	config.PowerOnCooldown = 60
	config.PowerOnTimeout = 480
	config.setPowerDefaults()
	if config.PowerOnCooldown != 60 || config.PowerOnTimeout != 480 {
		t.Fatalf("configured power values = cooldown %d timeout %d, want 60 and 480", config.PowerOnCooldown, config.PowerOnTimeout)
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
				DialAttemptTimeout:    5,
				DialRetryInterval:     1,
				KeepAlive:             120,
				IdleConnTimeout:       90,
				TLSHandshakeTimeout:   10,
				ExpectContinueTimeout: 1,
				MaxIdleConns:          100,
			},
		},
		{
			name: "partial config gets defaults for missing values",
			config: Config{
				ProxyTimeouts: ProxyTimeouts{
					DialTimeout:  60,
					MaxIdleConns: 50,
				},
			},
			expected: ProxyTimeouts{
				DialTimeout:           60,  // custom
				DialAttemptTimeout:    5,   // default
				DialRetryInterval:     1,   // default
				KeepAlive:             120, // default
				IdleConnTimeout:       90,  // default
				TLSHandshakeTimeout:   10,  // default
				ExpectContinueTimeout: 1,   // default
				MaxIdleConns:          50,  // custom
			},
		},
		{
			name: "all configured values are preserved",
			config: Config{
				ProxyTimeouts: ProxyTimeouts{
					DialTimeout:           30,
					DialAttemptTimeout:    4,
					DialRetryInterval:     2,
					KeepAlive:             40,
					IdleConnTimeout:       50,
					TLSHandshakeTimeout:   5,
					ExpectContinueTimeout: 2,
					MaxIdleConns:          200,
				},
			},
			expected: ProxyTimeouts{
				DialTimeout:           30,
				DialAttemptTimeout:    4,
				DialRetryInterval:     2,
				KeepAlive:             40,
				IdleConnTimeout:       50,
				TLSHandshakeTimeout:   5,
				ExpectContinueTimeout: 2,
				MaxIdleConns:          200,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.setProxyTimeoutDefaults()

			if tt.config.ProxyTimeouts.DialTimeout != tt.expected.DialTimeout {
				t.Errorf("DialTimeout = %v, want %v", tt.config.ProxyTimeouts.DialTimeout, tt.expected.DialTimeout)
			}
			if tt.config.ProxyTimeouts.DialAttemptTimeout != tt.expected.DialAttemptTimeout {
				t.Errorf("DialAttemptTimeout = %v, want %v", tt.config.ProxyTimeouts.DialAttemptTimeout, tt.expected.DialAttemptTimeout)
			}
			if tt.config.ProxyTimeouts.DialRetryInterval != tt.expected.DialRetryInterval {
				t.Errorf("DialRetryInterval = %v, want %v", tt.config.ProxyTimeouts.DialRetryInterval, tt.expected.DialRetryInterval)
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

func TestLoadConfig_WithPPB_YAML(t *testing.T) {
	// Save original env vars
	originalYAML := os.Getenv("PPB_YAML")
	originalPath := os.Getenv("PPB_CONFIG_PATH")
	defer func() {
		os.Setenv("PPB_YAML", originalYAML)
		os.Setenv("PPB_CONFIG_PATH", originalPath)
	}()

	tests := []struct {
		name        string
		yamlContent string
		wantErr     bool
		wantType    string
	}{
		{
			name: "valid YAML via PPB_YAML",
			yamlContent: `type: google_compute_engine
scheme: https
port: 443
allowedIps:
  - 0.0.0.0/0
machineMetadata:
  project_id: test-project
  zone: us-central1-a
  name: test-instance`,
			wantErr:  false,
			wantType: "google_compute_engine",
		},
		{
			name:        "invalid YAML via PPB_YAML",
			yamlContent: "invalid: yaml: content: [[[",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("PPB_YAML", tt.yamlContent)
			os.Unsetenv("PPB_CONFIG_PATH")

			config, err := LoadConfig()

			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if config.Type != tt.wantType {
					t.Errorf("LoadConfig() Type = %v, want %v", config.Type, tt.wantType)
				}
			}
		})
	}
}

func TestLoadConfigCheckedInExample(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the test source")
	}
	t.Setenv("PPB_YAML", "")
	t.Setenv("PPB_CONFIG_PATH", filepath.Join(filepath.Dir(sourceFile), "..", "..", "ppb.example.yaml"))

	loaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() could not load ppb.example.yaml: %v", err)
	}
	if len(loaded.AllowedIps) != 2 {
		t.Fatalf("ppb.example.yaml allowedIps = %+v, want two loopback networks", loaded.AllowedIps)
	}
	if loaded.IpForwardedHeader != "" {
		t.Fatalf("ppb.example.yaml ipForwardedHeader = %q, want direct-client mode", loaded.IpForwardedHeader)
	}
	if loaded.PowerOnCooldown != 30 || loaded.PowerOnTimeout != 360 {
		t.Fatalf("ppb.example.yaml power values = %d/%d, want 30/360", loaded.PowerOnCooldown, loaded.PowerOnTimeout)
	}
}

func TestLoadConfig_PriorityOrder(t *testing.T) {
	// Save original env vars
	originalYAML := os.Getenv("PPB_YAML")
	originalPath := os.Getenv("PPB_CONFIG_PATH")
	defer func() {
		os.Setenv("PPB_YAML", originalYAML)
		os.Setenv("PPB_CONFIG_PATH", originalPath)
	}()

	yamlContent := `type: google_compute_engine
scheme: https
port: 443
allowedIps:
  - 0.0.0.0/0
machineMetadata:
  project_id: from-env-var
  zone: us-central1-a
  name: test-instance`

	// Test that PPB_YAML takes priority over PPB_CONFIG_PATH
	os.Setenv("PPB_YAML", yamlContent)
	os.Setenv("PPB_CONFIG_PATH", "/nonexistent/file.yaml")

	config, err := LoadConfig()
	if err != nil {
		t.Errorf("LoadConfig() should succeed with PPB_YAML even if PPB_CONFIG_PATH is invalid, error = %v", err)
		return
	}

	if config.Machine.ProjectId != "from-env-var" {
		t.Errorf("LoadConfig() should load from PPB_YAML, got project_id = %v, want from-env-var", config.Machine.ProjectId)
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
				KeepAlive:             90,
				IdleConnTimeout:       45,
				TLSHandshakeTimeout:   15,
				ExpectContinueTimeout: 2,
				MaxIdleConns:          200,
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
