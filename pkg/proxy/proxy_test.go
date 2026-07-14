package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

		})
	}
}

func TestReverseProxy_TargetURLUsesMachineHost(t *testing.T) {
	// Create a mock machine with a known host
	machine := machine.NewGceMachine()
	machine.SetHostForTesting("10.0.0.8")

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
	target, err := proxy.targetURL()
	if err != nil {
		t.Fatalf("targetURL() error = %v", err)
	}
	if target.String() != "http://10.0.0.8:8080" {
		t.Fatalf("targetURL() = %q, want http://10.0.0.8:8080", target)
	}
}

func TestReverseProxy_TargetURLUsesProxyTargetOverride(t *testing.T) {
	config := &config.Config{
		Scheme: "http",
		Port:   8080,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   "localhost",
			Port:   9000,
		},
		Machine: machine.NewGceMachine(),
	}

	proxy := New(config)
	target, err := proxy.targetURL()
	if err != nil {
		t.Fatalf("targetURL() error = %v", err)
	}

	if target.Host != "localhost:9000" {
		t.Errorf("Target.Host = %q, want localhost:9000", target.Host)
	}
	if target.Scheme != "http" {
		t.Errorf("Target.Scheme = %q, want http", target.Scheme)
	}
}

func TestRetryingDialerRetriesConnectionRefusalWithoutReplayingHTTP(t *testing.T) {
	t.Parallel()

	clientConnection, serverConnection := net.Pipe()
	t.Cleanup(func() {
		_ = clientConnection.Close()
		_ = serverConnection.Close()
	})

	var mu sync.Mutex
	attempts := 0
	dialer := &retryingDialer{
		totalTimeout:   time.Second,
		attemptTimeout: 100 * time.Millisecond,
		retryInterval:  time.Millisecond,
		dial: func(context.Context, string, string) (net.Conn, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts < 3 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			}
			return clientConnection, nil
		},
	}

	connection, err := dialer.DialContext(context.Background(), "tcp", "10.0.0.8:8080")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if connection != clientConnection {
		t.Fatal("DialContext() did not return the successful connection")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("dial attempts = %d, want 3", attempts)
	}
}

func TestRetryingDialerFailsFastForPermanentDNSFailure(t *testing.T) {
	t.Parallel()

	attempts := 0
	dialer := &retryingDialer{
		totalTimeout:   time.Second,
		attemptTimeout: 100 * time.Millisecond,
		retryInterval:  time.Millisecond,
		dial: func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, &net.DNSError{Err: "no such host", Name: "invalid", IsNotFound: true}
		},
	}

	_, err := dialer.DialContext(context.Background(), "tcp", "invalid:8080")
	if err == nil {
		t.Fatal("DialContext() unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("dial attempts = %d, want a fail-fast single attempt", attempts)
	}
}

func TestRetryingDialerFailsFastForPermissionError(t *testing.T) {
	t.Parallel()

	attempts := 0
	dialer := &retryingDialer{
		totalTimeout:   time.Second,
		attemptTimeout: 100 * time.Millisecond,
		retryInterval:  time.Millisecond,
		dial: func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES}
		},
	}

	_, err := dialer.DialContext(context.Background(), "tcp", "10.0.0.8:8080")
	if err == nil {
		t.Fatal("DialContext() unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("dial attempts = %d, want a fail-fast single attempt", attempts)
	}
}

func TestRetryingDialerBoundsConnectionWindow(t *testing.T) {
	t.Parallel()

	attempts := 0
	dialer := &retryingDialer{
		totalTimeout:   30 * time.Millisecond,
		attemptTimeout: 5 * time.Millisecond,
		retryInterval:  time.Millisecond,
		jitter:         func(delay time.Duration) time.Duration { return delay },
		dial: func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
	}
	started := time.Now()
	_, err := dialer.DialContext(context.Background(), "tcp", "10.0.0.8:8080")
	if err == nil {
		t.Fatal("DialContext() unexpectedly succeeded")
	}
	var exhausted *dialExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("DialContext() error = %T %v, want dialExhaustedError", err, err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("DialContext() elapsed = %s, want a bounded retry window", elapsed)
	}
	if attempts < 2 {
		t.Fatalf("dial attempts = %d, want retries within the bound", attempts)
	}
}

func TestReverseProxyMissingTargetReturnsRetryAfter(t *testing.T) {
	proxyHandler := New(&config.Config{
		Scheme:  "http",
		Port:    8080,
		Machine: machine.NewGceMachine(),
	})
	request := httptest.NewRequest(http.MethodPost, "http://site.example.test/", strings.NewReader("not-dispatched"))
	recorder := httptest.NewRecorder()

	proxyHandler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}

func TestReverseProxyDialExhaustionReturnsRetryAfter(t *testing.T) {
	proxyHandler := New(&config.Config{
		Scheme: "http",
		Port:   8080,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   "backend.example.test",
			Port:   8080,
		},
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           1,
			DialAttemptTimeout:    1,
			DialRetryInterval:     1,
			KeepAlive:             1,
			IdleConnTimeout:       1,
			TLSHandshakeTimeout:   1,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          10,
		},
		Machine: machine.NewGceMachine(),
	})
	attempts := 0
	proxyHandler.Transport.DialContext = (&retryingDialer{
		totalTimeout:   30 * time.Millisecond,
		attemptTimeout: 5 * time.Millisecond,
		retryInterval:  time.Millisecond,
		jitter:         func(delay time.Duration) time.Duration { return delay },
		dial: func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
	}).DialContext
	request := httptest.NewRequest(http.MethodPost, "http://site.example.test/", strings.NewReader("not-dispatched"))
	recorder := httptest.NewRecorder()

	proxyHandler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if attempts < 2 {
		t.Fatalf("dial attempts = %d, want a bounded retry sequence", attempts)
	}
}

func TestRetryingDialerHonorsRequestCancellation(t *testing.T) {
	t.Parallel()

	dialer := &retryingDialer{
		totalTimeout:   time.Second,
		attemptTimeout: time.Second,
		retryInterval:  time.Millisecond,
		dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dialer.DialContext(ctx, "tcp", "10.0.0.8:8080")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext() error = %v, want context cancellation", err)
	}
}

func TestReverseProxyKeepsForwardedHeadersRequestLocal(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s|%s|%s", r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Cloud-Trace-Context"))
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	backendHost, backendPortText, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatalf("net.SplitHostPort() error = %v", err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatalf("strconv.Atoi() error = %v", err)
	}

	proxyHandler := New(&config.Config{
		Scheme: "http",
		Port:   backendPort,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   backendHost,
			Port:   backendPort,
		},
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           2,
			DialAttemptTimeout:    1,
			DialRetryInterval:     1,
			KeepAlive:             1,
			IdleConnTimeout:       1,
			TLSHandshakeTimeout:   1,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          100,
		},
		Machine: machine.NewGceMachine(),
	})
	frontend := httptest.NewServer(proxyHandler)
	t.Cleanup(frontend.Close)

	const requestCount = 32
	errorsCh := make(chan error, requestCount)
	var wg sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("tenant-%d.example|192.0.2.%d|trace-%d", index, index+1, index)
			request, err := http.NewRequest(http.MethodGet, frontend.URL, nil)
			if err != nil {
				errorsCh <- err
				return
			}
			request.Host = fmt.Sprintf("tenant-%d.example", index)
			request.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", index+1))
			request.Header.Set("X-Cloud-Trace-Context", fmt.Sprintf("trace-%d", index))
			response, err := frontend.Client().Do(request)
			if err != nil {
				errorsCh <- err
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				errorsCh <- err
				return
			}
			if got := strings.TrimSpace(string(body)); got != want {
				errorsCh <- fmt.Errorf("forwarded headers = %q, want %q", got, want)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestReverseProxyDialRetrySendsPostBodyExactlyOnce(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	requestBody := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requestCount++
		requestBody += string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	backendHost, backendPortText, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatalf("net.SplitHostPort() error = %v", err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatalf("strconv.Atoi() error = %v", err)
	}

	proxyHandler := New(&config.Config{
		Scheme: "http",
		Port:   backendPort,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   backendHost,
			Port:   backendPort,
		},
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           2,
			DialAttemptTimeout:    1,
			DialRetryInterval:     1,
			KeepAlive:             1,
			IdleConnTimeout:       1,
			TLSHandshakeTimeout:   1,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          10,
		},
		Machine: machine.NewGceMachine(),
	})
	realDialer := &net.Dialer{}
	dialAttempts := 0
	proxyHandler.Transport.DialContext = (&retryingDialer{
		totalTimeout:   time.Second,
		attemptTimeout: 100 * time.Millisecond,
		retryInterval:  time.Millisecond,
		jitter:         func(delay time.Duration) time.Duration { return delay },
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialAttempts++
			if dialAttempts < 3 {
				return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
			}
			return realDialer.DialContext(ctx, network, address)
		},
	}).DialContext

	frontend := httptest.NewServer(proxyHandler)
	t.Cleanup(frontend.Close)
	response, err := http.Post(frontend.URL, "text/plain", strings.NewReader("one-copy"))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 || requestBody != "one-copy" {
		t.Fatalf("backend received count=%d body=%q, want one exact request", requestCount, requestBody)
	}
	if dialAttempts != 3 {
		t.Fatalf("dial attempts = %d, want 3", dialAttempts)
	}
}

func TestReverseProxyPostConnectResetOmitsRetryAfterAndDoesNotReplayPost(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	requestBody := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read backend request body: %v", err)
			return
		}
		mu.Lock()
		requestCount++
		requestBody += string(body)
		mu.Unlock()

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("backend response writer does not support hijacking")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack backend connection: %v", err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	backendHost, backendPortText, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := New(&config.Config{
		Scheme: "http",
		Port:   backendPort,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   backendHost,
			Port:   backendPort,
		},
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           2,
			DialAttemptTimeout:    1,
			DialRetryInterval:     1,
			KeepAlive:             1,
			IdleConnTimeout:       1,
			TLSHandshakeTimeout:   1,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          10,
		},
		Machine: machine.NewGceMachine(),
	})
	frontend := httptest.NewServer(proxyHandler)
	t.Cleanup(frontend.Close)

	response, err := http.Post(frontend.URL, "text/plain", strings.NewReader("one-copy"))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	if got := response.Header.Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want omitted after an ambiguous post-connect failure", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 || requestBody != "one-copy" {
		t.Fatalf("backend received count=%d body=%q, want one exact request", requestCount, requestBody)
	}
}

func TestReverseProxyDoesNotRetryHTTPStatus(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		http.Error(w, "try later", http.StatusServiceUnavailable)
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	backendHost, backendPortText, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatalf("net.SplitHostPort() error = %v", err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatalf("strconv.Atoi() error = %v", err)
	}

	proxyHandler := New(&config.Config{
		Scheme: "http",
		Port:   backendPort,
		ProxyTarget: &config.ProxyTarget{
			Scheme: "http",
			Host:   backendHost,
			Port:   backendPort,
		},
		ProxyTimeouts: config.ProxyTimeouts{
			DialTimeout:           2,
			DialAttemptTimeout:    1,
			DialRetryInterval:     1,
			KeepAlive:             1,
			IdleConnTimeout:       1,
			TLSHandshakeTimeout:   1,
			ExpectContinueTimeout: 1,
			MaxIdleConns:          10,
		},
		Machine: machine.NewGceMachine(),
	})
	frontend := httptest.NewServer(proxyHandler)
	t.Cleanup(frontend.Close)

	response, err := frontend.Client().Get(frontend.URL)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("backend request count = %d, want 1", requestCount)
	}
}
