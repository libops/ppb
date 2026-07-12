package proxy

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/libops/ppb/pkg/config"
)

type ReverseProxy struct {
	Transport *http.Transport
	Config    *config.Config
}

type retryingDialer struct {
	totalTimeout   time.Duration
	attemptTimeout time.Duration
	retryInterval  time.Duration
	keepAlive      time.Duration
	dial           func(context.Context, string, string) (net.Conn, error)
	jitter         func(time.Duration) time.Duration
}

func New(c *config.Config) *ReverseProxy {
	// Use configured timeout values (defaults already set in config loading)
	dialTimeout := time.Duration(c.ProxyTimeouts.DialTimeout) * time.Second
	dialAttemptTimeout := time.Duration(c.ProxyTimeouts.DialAttemptTimeout) * time.Second
	dialRetryInterval := time.Duration(c.ProxyTimeouts.DialRetryInterval) * time.Second
	keepAlive := time.Duration(c.ProxyTimeouts.KeepAlive) * time.Second
	idleConnTimeout := time.Duration(c.ProxyTimeouts.IdleConnTimeout) * time.Second
	tlsHandshakeTimeout := time.Duration(c.ProxyTimeouts.TLSHandshakeTimeout) * time.Second
	expectContinueTimeout := time.Duration(c.ProxyTimeouts.ExpectContinueTimeout) * time.Second

	dialer := &retryingDialer{
		totalTimeout:   dialTimeout,
		attemptTimeout: dialAttemptTimeout,
		retryInterval:  dialRetryInterval,
		keepAlive:      keepAlive,
	}
	return &ReverseProxy{
		Config: c,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          c.ProxyTimeouts.MaxIdleConns,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
	}
}

func (p *ReverseProxy) targetURL() (*url.URL, error) {
	scheme := p.Config.Scheme
	if p.Config.ProxyTarget != nil && p.Config.ProxyTarget.Scheme != "" {
		scheme = p.Config.ProxyTarget.Scheme
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported proxy target scheme %q", scheme)
	}

	if p.Config.ProxyTarget != nil && p.Config.ProxyTarget.Host != "" {
		port := p.Config.ProxyTarget.Port
		if port == 0 {
			port = p.Config.Port
		}
		return &url.URL{
			Scheme: scheme,
			Host:   net.JoinHostPort(p.Config.ProxyTarget.Host, strconv.Itoa(port)),
		}, nil
	}

	host := p.Config.Machine.Host()
	if host == "" {
		return nil, errors.New("machine does not have a proxy target IP")
	}
	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(p.Config.Port)),
	}, nil
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, err := p.targetURL()
	if err != nil {
		slog.Warn("Proxy target is unavailable", "error", err)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Backend not available", http.StatusServiceUnavailable)
		return
	}

	forwardedFor := r.Header.Get("X-Forwarded-For")
	forwardedHost := r.Host
	trace := r.Header.Get("X-Cloud-Trace-Context")

	rp := &httputil.ReverseProxy{
		Transport: p.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			setOrDeleteHeader(pr.Out.Header, "X-Cloud-Trace-Context", trace)
			setOrDeleteHeader(pr.Out.Header, "X-Forwarded-For", forwardedFor)
			setOrDeleteHeader(pr.Out.Header, "X-Forwarded-Host", forwardedHost)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("Backend proxy request failed", "target", target.Redacted(), "error", err)
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Backend not available", http.StatusServiceUnavailable)
		},
	}

	rp.ServeHTTP(w, r)
}

func setOrDeleteHeader(header http.Header, name, value string) {
	if value == "" {
		header.Del(name)
		return
	}
	header.Set(name, value)
}

func (d *retryingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	retryCtx, cancel := context.WithTimeout(ctx, d.totalTimeout)
	defer cancel()

	dial := d.dial
	if dial == nil {
		networkDialer := &net.Dialer{KeepAlive: d.keepAlive}
		dial = networkDialer.DialContext
	}

	var lastErr error
	retryDelay := d.retryInterval
	for attempt := 1; ; attempt++ {
		attemptTimeout := d.attemptTimeout
		if deadline, ok := retryCtx.Deadline(); ok && time.Until(deadline) < attemptTimeout {
			attemptTimeout = time.Until(deadline)
		}
		if attemptTimeout <= 0 {
			return nil, d.exhaustedError(ctx, address, lastErr)
		}

		attemptCtx, attemptCancel := context.WithTimeout(retryCtx, attemptTimeout)
		connection, err := dial(attemptCtx, network, address)
		attemptCancel()
		if err == nil {
			return connection, nil
		}
		lastErr = err
		if retryCtx.Err() != nil {
			return nil, d.exhaustedError(ctx, address, lastErr)
		}
		if !isRetryableDialError(err) {
			return nil, err
		}

		slog.Debug("Backend connection attempt failed; retrying", "address", address, "attempt", attempt, "error", err)
		jitter := d.jitter
		if jitter == nil {
			jitter = jitteredDelay
		}
		timer := time.NewTimer(jitter(retryDelay))
		select {
		case <-retryCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, d.exhaustedError(ctx, address, lastErr)
		case <-timer.C:
		}
		if retryDelay < 5*time.Second {
			retryDelay *= 2
			if retryDelay > 5*time.Second {
				retryDelay = 5 * time.Second
			}
		}
	}
}

func (d *retryingDialer) exhaustedError(parent context.Context, address string, lastErr error) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if lastErr == nil {
		return fmt.Errorf("backend %s did not accept a connection within %s", address, d.totalTimeout)
	}
	return fmt.Errorf("backend %s did not accept a connection within %s: %w", address, d.totalTimeout, lastErr)
}

func isRetryableDialError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var addressError *net.AddrError
	if errors.As(err, &addressError) {
		return false
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && !dnsError.IsTimeout && !dnsError.IsTemporary {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

func jitteredDelay(delay time.Duration) time.Duration {
	// Spread simultaneous cold-start requests without making the configured
	// interval an unbounded delay. The backoff remains within 75%-125%.
	var randomByte [1]byte
	if _, err := cryptorand.Read(randomByte[:]); err != nil {
		return delay
	}
	factor := 0.75 + (float64(randomByte[0])/255.0)*0.5
	return time.Duration(float64(delay) * factor)
}
