package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/libops/ppb/pkg/machine"
	yaml "gopkg.in/yaml.v3"
)

type Config struct {
	Type              string         `yaml:"type"`
	Scheme            string         `yaml:"scheme"`
	Port              int            `yaml:"port"`
	AllowedIps        []IPNet        `yaml:"allowedIps"`
	IpForwardedHeader string         `yaml:"ipForwardedHeader"`
	IpDepth           int            `yaml:"ipDepth"`
	PowerOnCooldown   int            `yaml:"powerOnCooldown"` // seconds
	PowerOnTimeout    int            `yaml:"powerOnTimeout"`  // seconds, default: 360
	ProxyTimeouts     ProxyTimeouts  `yaml:"proxyTimeouts"`
	MachineMetadata   map[string]any `yaml:"machineMetadata"`
	ProxyTarget       *ProxyTarget   `yaml:"proxyTarget"`
	Machine           *machine.GoogleComputeEngine
}

// ProxyTarget optionally overrides where requests are proxied to.
// When set, the machine referenced by MachineMetadata is still powered on and
// pinged, but HTTP traffic is forwarded to ProxyTarget instead. This supports
// topologies where a sidecar (e.g. a Cloud Run frontend container) serves
// requests while still depending on the remote machine being up.
type ProxyTarget struct {
	Scheme string `yaml:"scheme"`
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
}

type ProxyTimeouts struct {
	DialTimeout           int `yaml:"dialTimeout"`           // total connection retry window in seconds, default: 120
	DialAttemptTimeout    int `yaml:"dialAttemptTimeout"`    // timeout for one connection attempt in seconds, default: 5
	DialRetryInterval     int `yaml:"dialRetryInterval"`     // delay between connection attempts in seconds, default: 1
	KeepAlive             int `yaml:"keepAlive"`             // seconds, default: 120
	IdleConnTimeout       int `yaml:"idleConnTimeout"`       // seconds, default: 90
	TLSHandshakeTimeout   int `yaml:"tlsHandshakeTimeout"`   // seconds, default: 10
	ExpectContinueTimeout int `yaml:"expectContinueTimeout"` // seconds, default: 1
	MaxIdleConns          int `yaml:"maxIdleConns"`          // default: 100
}

type IPNet struct {
	*net.IPNet
}

func (i *IPNet) UnmarshalYAML(value *yaml.Node) error {
	slog.Debug("Unmarshaling", "value", value)
	var cidr string
	if err := value.Decode(&cidr); err != nil {
		return err
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %v", err)
	}
	i.IPNet = network
	slog.Debug("Parsed IP", "ip", i.IPNet)
	return nil
}

func LoadConfig() (*Config, error) {
	var data []byte
	var err error

	// Check for PPB_YAML environment variable first (highest priority)
	yamlContent := os.Getenv("PPB_YAML")
	if yamlContent != "" {
		slog.Debug("Loading config from PPB_YAML environment variable")
		data = []byte(yamlContent)
	} else {
		// Fall back to file-based configuration
		filename := os.Getenv("PPB_CONFIG_PATH")
		if filename == "" {
			filename = "ppb.yaml"
		}
		slog.Debug("Loading config", "filename", filename)
		// PPB_CONFIG_PATH is an intentional local operator interface, not a
		// request-derived path. The unprivileged container may read any mounted
		// configuration path selected by its deployer.
		data, err = os.ReadFile(filename) // #nosec G304,G703 -- trusted deployment configuration
		if err != nil {
			return nil, err
		}
	}

	expandedYaml := os.ExpandEnv(string(data))

	var config Config
	err = yaml.Unmarshal([]byte(expandedYaml), &config)
	if err != nil {
		return nil, err
	}
	config.IpForwardedHeader = strings.TrimSpace(config.IpForwardedHeader)
	if config.IpDepth < 0 {
		return nil, fmt.Errorf("ipDepth must not be negative")
	}
	if config.IpDepth > 0 && config.IpForwardedHeader == "" {
		return nil, fmt.Errorf("ipForwardedHeader is required when ipDepth is greater than zero")
	}

	switch config.Type {
	case "google_compute_engine":
		gce := machine.NewGceMachine()
		machineYAML, _ := yaml.Marshal(config.MachineMetadata)
		if err := yaml.Unmarshal(machineYAML, &gce); err != nil {
			return nil, err
		}
		slog.Debug("loaded gce config", "gce", gce)
		config.Machine = gce
	default:
		return nil, fmt.Errorf("unknown machine type: %s", config.Type)
	}

	// Set default proxy timeouts if not specified
	config.setPowerDefaults()
	config.setProxyTimeoutDefaults()

	return &config, nil
}

func (c *Config) setPowerDefaults() {
	if c.PowerOnCooldown <= 0 {
		c.PowerOnCooldown = 30
	}
	if c.PowerOnTimeout <= 0 {
		c.PowerOnTimeout = 360
	}
}

// setProxyTimeoutDefaults sets default values for proxy timeouts if not configured
func (c *Config) setProxyTimeoutDefaults() {
	if c.ProxyTimeouts.DialTimeout <= 0 {
		c.ProxyTimeouts.DialTimeout = 120
	}
	if c.ProxyTimeouts.DialAttemptTimeout <= 0 {
		c.ProxyTimeouts.DialAttemptTimeout = 5
	}
	if c.ProxyTimeouts.DialRetryInterval <= 0 {
		c.ProxyTimeouts.DialRetryInterval = 1
	}
	if c.ProxyTimeouts.KeepAlive <= 0 {
		c.ProxyTimeouts.KeepAlive = 120
	}
	if c.ProxyTimeouts.IdleConnTimeout <= 0 {
		c.ProxyTimeouts.IdleConnTimeout = 90
	}
	if c.ProxyTimeouts.TLSHandshakeTimeout <= 0 {
		c.ProxyTimeouts.TLSHandshakeTimeout = 10
	}
	if c.ProxyTimeouts.ExpectContinueTimeout <= 0 {
		c.ProxyTimeouts.ExpectContinueTimeout = 1
	}
	if c.ProxyTimeouts.MaxIdleConns <= 0 {
		c.ProxyTimeouts.MaxIdleConns = 100
	}
}
