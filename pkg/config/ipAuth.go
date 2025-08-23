package config

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
)

func (c *Config) IpIsAllowed(r *http.Request) bool {
	ip := c.getClientIP(r)
	for _, block := range c.AllowedIps {
		if block.Contains(ip) {
			return true
		}
	}

	return false
}

func (c *Config) getClientIP(r *http.Request) net.IP {
	ip := r.Header.Get(c.IpForwardedHeader)
	if c.IpForwardedHeader != "" {
		components := strings.Split(ip, ",")
		depth := c.IpDepth
		ip = ""
		for i := len(components) - 1; i >= 0; i-- {
			_ip := strings.TrimSpace(components[i])
			if depth == 0 {
				ip = _ip
				break
			}
			depth--
		}
	}

	if ip == "" {
		if c.IpForwardedHeader != "" {
			slog.Debug("No IPs in header. Using r.RemoteAddr", "ipDepth", c.IpDepth, c.IpForwardedHeader, r.Header.Get(c.IpForwardedHeader))
		}
		ip = r.RemoteAddr
	}

	if strings.Contains(ip, ":") {
		host, _, err := net.SplitHostPort(ip)
		if err != nil {
			slog.Error("Error splitting host and port from client IP", "ip", ip, "err", err)
		}
		ip = host
	}

	slog.Debug("Got client IP", "ip", ip)

	return net.ParseIP(ip)
}