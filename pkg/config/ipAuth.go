package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

func (c *Config) IpIsAllowed(r *http.Request) bool {
	_, err := c.AllowedClientIP(r)
	return err == nil
}

// AllowedClientIP returns the parsed original-client address only when it is
// inside an explicitly configured allowlist block.
func (c *Config) AllowedClientIP(r *http.Request) (net.IP, error) {
	ip, err := c.getClientIP(r)
	if err != nil {
		slog.Warn("Unable to determine client IP; denying request", "error", err)
		return nil, err
	}
	for _, block := range c.AllowedIps {
		if block.Contains(ip) {
			return ip, nil
		}
	}
	slog.Debug("Client IP is not allowed", "ip", ip)
	return nil, fmt.Errorf("client IP %s is not allowed", ip)
}

func (c *Config) getClientIP(r *http.Request) (net.IP, error) {
	value := ""
	if c.IpForwardedHeader != "" {
		// Flatten every field line before selecting from the trusted suffix.
		// Header.Get returns only the first value, which could otherwise let an
		// attacker-controlled duplicate hide a proxy-appended client hop.
		headerValue := strings.TrimSpace(strings.Join(r.Header.Values(c.IpForwardedHeader), ","))
		if headerValue == "" {
			return nil, fmt.Errorf("trusted client IP header %q is missing", c.IpForwardedHeader)
		}
		components := strings.Split(headerValue, ",")
		index := len(components) - 1 - c.IpDepth
		if c.IpDepth < 0 || index < 0 {
			return nil, fmt.Errorf("trusted client IP header %q has %d hops, fewer than configured depth %d", c.IpForwardedHeader, len(components), c.IpDepth)
		}
		value = strings.TrimSpace(components[index])
		if strings.ContainsAny(value, " \t") {
			return nil, fmt.Errorf("trusted client IP header %q contains an invalid hop", c.IpForwardedHeader)
		}
	} else {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			value = strings.TrimSpace(r.RemoteAddr)
		} else {
			value = host
		}
	}

	ip := net.ParseIP(value)
	if ip == nil {
		return nil, fmt.Errorf("client IP value %q is not an IP address", value)
	}
	slog.Debug("Got client IP", "ip", ip)
	return ip, nil
}
