package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"

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
	ProxyTimeouts     ProxyTimeouts  `yaml:"proxyTimeouts"`
	MachineMetadata   map[string]any `yaml:"machineMetadata"`
	Machine           *machine.GoogleComputeEngine
}

type ProxyTimeouts struct {
	DialTimeout           int `yaml:"dialTimeout"`           // seconds, default: 120
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
		data, err = os.ReadFile(filename)
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
	config.setProxyTimeoutDefaults()

	return &config, nil
}

// setProxyTimeoutDefaults sets default values for proxy timeouts if not configured
func (c *Config) setProxyTimeoutDefaults() {
	if c.ProxyTimeouts.DialTimeout <= 0 {
		c.ProxyTimeouts.DialTimeout = 120
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
