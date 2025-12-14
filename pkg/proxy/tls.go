package proxy

import (
	"context"
	"crypto/tls"
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
	Host      []string
	Ip        []string
	Trace     []string
}

func New(c *config.Config) *ReverseProxy {
	// Use configured timeout values (defaults already set in config loading)
	dialTimeout := time.Duration(c.ProxyTimeouts.DialTimeout) * time.Second
	keepAlive := time.Duration(c.ProxyTimeouts.KeepAlive) * time.Second
	idleConnTimeout := time.Duration(c.ProxyTimeouts.IdleConnTimeout) * time.Second
	tlsHandshakeTimeout := time.Duration(c.ProxyTimeouts.TLSHandshakeTimeout) * time.Second
	expectContinueTimeout := time.Duration(c.ProxyTimeouts.ExpectContinueTimeout) * time.Second

	return &ReverseProxy{
		Target: &url.URL{
			Scheme: c.Scheme,
		},
		Config: c,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: keepAlive,
			}).DialContext,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := &net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: keepAlive,
				}
				return tls.DialWithDialer(dialer, network, addr, nil)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          c.ProxyTimeouts.MaxIdleConns,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
	}
}

func (p *ReverseProxy) SetHost() {
	p.Target.Host = net.JoinHostPort(
		p.Config.Machine.Host(),
		strconv.Itoa(p.Config.Port),
	)
	slog.Debug("Set machine host", "host", p.Target.Host)
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rp := &httputil.ReverseProxy{
		Transport: p.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(p.Target)
			pr.Out.Header["X-Cloud-Trace-Context"] = p.Trace
			pr.Out.Header["X-Forwarded-For"] = p.Ip
			pr.Out.Header["X-Forwarded-Host"] = p.Host
		},
	}

	rp.ServeHTTP(w, r)
}

func (p *ReverseProxy) SetRequestHeaders(r *http.Request) {
	p.Ip = []string{
		r.Header.Get("X-Forwarded-For"),
	}
	p.Host = []string{
		r.Host,
	}
	p.Trace = []string{
		r.Header.Get("X-Cloud-Trace-Context"),
	}
	slog.Debug("Request headers", "p.Ip", p.Ip, "p.Host", p.Host)
}
