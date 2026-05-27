package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/libops/ppb/pkg/config"
)

type ReverseProxy struct {
	Target    *url.URL
	Transport *http.Transport
	Config    *config.Config
}

func New(c *config.Config) *ReverseProxy {
	// Use configured timeout values (defaults already set in config loading)
	dialTimeout := time.Duration(c.ProxyTimeouts.DialTimeout) * time.Second
	keepAlive := time.Duration(c.ProxyTimeouts.KeepAlive) * time.Second
	idleConnTimeout := time.Duration(c.ProxyTimeouts.IdleConnTimeout) * time.Second
	tlsHandshakeTimeout := time.Duration(c.ProxyTimeouts.TLSHandshakeTimeout) * time.Second
	expectContinueTimeout := time.Duration(c.ProxyTimeouts.ExpectContinueTimeout) * time.Second

	scheme := c.Scheme
	if c.ProxyTarget != nil && c.ProxyTarget.Scheme != "" {
		scheme = c.ProxyTarget.Scheme
	}
	return &ReverseProxy{
		Target: &url.URL{
			Scheme: scheme,
		},
		Config: c,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: keepAlive,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          c.ProxyTimeouts.MaxIdleConns,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
	}
}

func (p *ReverseProxy) targetURL() (*url.URL, bool) {
	if p.Config.ProxyTarget != nil && p.Config.ProxyTarget.Host != "" {
		port := p.Config.ProxyTarget.Port
		if port == 0 {
			port = p.Config.Port
		}
		target := *p.Target
		if p.Config.ProxyTarget.Scheme != "" {
			target.Scheme = p.Config.ProxyTarget.Scheme
		}
		target.Host = net.JoinHostPort(p.Config.ProxyTarget.Host, strconv.Itoa(port))
		slog.Debug("Set proxy target host", "host", target.Host)
		return &target, true
	}

	host := p.Config.Machine.Host()
	if host == "" {
		return nil, false
	}

	target := *p.Target
	target.Host = net.JoinHostPort(host, strconv.Itoa(p.Config.Port))
	slog.Debug("Set machine host", "host", target.Host)
	return &target, true
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, ok := p.targetURL()
	if !ok {
		http.Error(w, "Backend not available", http.StatusServiceUnavailable)
		return
	}

	ip := []string{r.Header.Get("X-Forwarded-For")}
	host := []string{r.Host}
	trace := []string{r.Header.Get("X-Cloud-Trace-Context")}
	slog.Debug("Request headers", "ip", ip, "host", host)

	rp := &httputil.ReverseProxy{
		Transport: p.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header["X-Cloud-Trace-Context"] = trace
			pr.Out.Header["X-Forwarded-For"] = ip
			pr.Out.Header["X-Forwarded-Host"] = host
			pr.Out.Header["X-Forwarded-Proto"] = []string{"https"}
		},
	}

	rp.ServeHTTP(w, r)
}
