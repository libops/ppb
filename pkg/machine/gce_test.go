package machine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	compute "google.golang.org/api/compute/v1"
)

func TestGoogleComputeEnginePowerOnFollowsStateSequence(t *testing.T) {
	t.Parallel()

	statuses := []string{"TERMINATED", "STAGING", "RUNNING"}
	var statusMu sync.Mutex
	statusIndex := 0
	mutations := 0
	m := NewGceMachine()
	m.UsePrivateIp = true
	m.pollInterval = time.Millisecond
	m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
		statusMu.Lock()
		defer statusMu.Unlock()
		index := statusIndex
		if index < len(statuses)-1 {
			statusIndex++
		}
		return testInstance(statuses[index]), nil
	}
	m.powerOnHook = func(_ context.Context, status string) error {
		mutations++
		if status != "TERMINATED" {
			t.Fatalf("powerOn status = %q, want TERMINATED", status)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.PowerOn(ctx); err != nil {
		t.Fatalf("PowerOn() error = %v", err)
	}
	if mutations != 1 {
		t.Fatalf("power mutations = %d, want 1", mutations)
	}
	if host := m.Host(); host != "10.42.0.8" {
		t.Fatalf("Host() = %q, want 10.42.0.8", host)
	}
}

func TestGoogleComputeEngineJoinsConflictingMutation(t *testing.T) {
	t.Parallel()

	statuses := []string{"TERMINATED", "STAGING", "RUNNING"}
	statusIndex := 0
	mutations := 0
	m := NewGceMachine()
	m.UsePrivateIp = true
	m.pollInterval = time.Millisecond
	m.joinTimeout = 50 * time.Millisecond
	m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
		index := statusIndex
		if index < len(statuses)-1 {
			statusIndex++
		}
		return testInstance(statuses[index]), nil
	}
	m.powerOnHook = func(context.Context, string) error {
		mutations++
		return errors.New("instance is already starting")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.PowerOn(ctx); err != nil {
		t.Fatalf("PowerOn() should join the competing transition: %v", err)
	}
	if mutations != 1 {
		t.Fatalf("power mutations = %d, want 1", mutations)
	}
}

func TestGoogleComputeEngineReturnsPermanentMutationError(t *testing.T) {
	t.Parallel()

	m := NewGceMachine()
	m.pollInterval = time.Millisecond
	m.joinTimeout = 5 * time.Millisecond
	m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
		return testInstance("TERMINATED"), nil
	}
	m.powerOnHook = func(context.Context, string) error {
		return errors.New("permission denied")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.PowerOn(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("PowerOn() error = %v, want permanent mutation failure", err)
	}
}

func TestGoogleComputeEngineCooldownJoinsAcceptedTransition(t *testing.T) {
	t.Parallel()

	statuses := []string{"STAGING", "RUNNING"}
	statusIndex := 0
	mutations := 0
	m := NewGceMachine()
	m.UsePrivateIp = true
	m.LastPowerOnAttempt = time.Now()
	m.pollInterval = time.Millisecond
	m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
		index := statusIndex
		if index < len(statuses)-1 {
			statusIndex++
		}
		return testInstance(statuses[index]), nil
	}
	m.powerOnHook = func(context.Context, string) error {
		mutations++
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.PowerOnWithCooldown(ctx, 30); err != nil {
		t.Fatalf("PowerOnWithCooldown() error = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("power mutations = %d, want no duplicate mutation", mutations)
	}
}

func TestGoogleComputeEngineCooldownRetriesTerminalStateAfterWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := false
		mutations := 0
		m := NewGceMachine()
		m.UsePrivateIp = true
		m.LastPowerOnAttempt = time.Now()
		m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
			if started {
				return testInstance("RUNNING"), nil
			}
			return testInstance("TERMINATED"), nil
		}
		m.powerOnHook = func(context.Context, string) error {
			mutations++
			started = true
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.PowerOnWithCooldown(ctx, 2); err != nil {
			t.Fatalf("PowerOnWithCooldown() error = %v", err)
		}
		if mutations != 1 {
			t.Fatalf("power mutations = %d, want one retry after cooldown", mutations)
		}
	})
}

func TestGoogleComputeEngineWaitUsesCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewGceMachine()
		m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
			return testInstance("STAGING"), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()

		err := m.PowerOn(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("PowerOn() error = %v, want caller deadline", err)
		}
	})
}

func TestGoogleComputeEngineConcurrentCooldownCallsUseCachedHost(t *testing.T) {
	t.Parallel()

	m := NewGceMachine()
	m.SetHostForTesting("10.42.0.8")
	m.LastPowerOnAttempt = time.Now()
	var metadataCalls atomic.Int64
	m.getInstanceHook = func(context.Context) (*compute.Instance, error) {
		metadataCalls.Add(1)
		return testInstance("RUNNING"), nil
	}

	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsCh <- m.PowerOnWithCooldown(context.Background(), 30)
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Errorf("PowerOnWithCooldown() error = %v", err)
		}
	}
	if calls := metadataCalls.Load(); calls != 0 {
		t.Fatalf("metadata calls = %d, want cached host fast path", calls)
	}
}

func testInstance(status string) *compute.Instance {
	return &compute.Instance{
		Name:   "test-instance",
		Status: status,
		NetworkInterfaces: []*compute.NetworkInterface{{
			NetworkIP: "10.42.0.8",
		}},
	}
}

func TestGoogleComputeEngine_setIp(t *testing.T) {
	tests := []struct {
		name         string
		usePrivateIp bool
		instance     *compute.Instance
		expectedHost string
		expectError  bool
	}{
		{
			name:         "private IP success",
			usePrivateIp: true,
			instance: &compute.Instance{
				Name: "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{
					{
						NetworkIP: "10.0.0.5",
					},
				},
			},
			expectedHost: "10.0.0.5",
			expectError:  false,
		},
		{
			name:         "private IP missing",
			usePrivateIp: true,
			instance: &compute.Instance{
				Name: "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{
					{
						NetworkIP: "",
					},
				},
			},
			expectedHost: "",
			expectError:  true,
		},
		{
			name:         "public IP success",
			usePrivateIp: false,
			instance: &compute.Instance{
				Name: "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{
					{
						NetworkIP: "10.0.0.5",
						AccessConfigs: []*compute.AccessConfig{
							{
								NatIP: "203.0.113.1",
							},
						},
					},
				},
			},
			expectedHost: "203.0.113.1",
			expectError:  false,
		},
		{
			name:         "public IP missing",
			usePrivateIp: false,
			instance: &compute.Instance{
				Name: "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{
					{
						NetworkIP: "10.0.0.5",
						AccessConfigs: []*compute.AccessConfig{
							{
								NatIP: "",
							},
						},
					},
				},
			},
			expectedHost: "",
			expectError:  true,
		},
		{
			name:         "no network interfaces",
			usePrivateIp: false,
			instance: &compute.Instance{
				Name:              "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{},
			},
			expectedHost: "",
			expectError:  true,
		},
		{
			name:         "no access configs for public IP",
			usePrivateIp: false,
			instance: &compute.Instance{
				Name: "test-instance",
				NetworkInterfaces: []*compute.NetworkInterface{
					{
						NetworkIP:     "10.0.0.5",
						AccessConfigs: []*compute.AccessConfig{},
					},
				},
			},
			expectedHost: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &GoogleComputeEngine{
				Name:         "test-instance",
				UsePrivateIp: tt.usePrivateIp,
			}

			err := m.setIp(tt.instance)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if m.host != tt.expectedHost {
				t.Errorf("Expected host %s, got %s", tt.expectedHost, m.host)
			}
		})
	}
}

func TestGoogleComputeEngine_Host(t *testing.T) {
	m := &GoogleComputeEngine{
		host: "192.168.1.1",
	}

	result := m.Host()
	expected := "192.168.1.1"

	if result != expected {
		t.Errorf("Host() = %v, want %v", result, expected)
	}
}

func TestNewGceMachine(t *testing.T) {
	m := NewGceMachine()

	if m == nil {
		t.Fatal("NewGceMachine() returned nil")
	}

	if m.Lock == nil {
		t.Fatal("NewGceMachine() did not initialize Lock")
	}

	// Test that the semaphore is weighted with 1
	if !m.Lock.TryAcquire(1) {
		t.Error("NewGceMachine() semaphore should allow acquiring 1")
	}

	// Should not be able to acquire another
	if m.Lock.TryAcquire(1) {
		t.Error("NewGceMachine() semaphore should not allow acquiring more than 1")
	}
}

func TestClassifyInstanceStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status  string
		action  instanceStatusAction
		wantErr bool
	}{
		{status: "RUNNING", action: instanceReady},
		{status: "TERMINATED", action: instanceStart},
		{status: "SUSPENDED", action: instanceStart},
		{status: "PROVISIONING", action: instanceWait},
		{status: "STAGING", action: instanceWait},
		{status: "STOPPING", action: instanceWait},
		{status: "SUSPENDING", action: instanceWait},
		{status: "REPAIRING", action: instanceWait},
		{status: "UNKNOWN", action: instanceWait, wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.status, func(t *testing.T) {
			t.Parallel()
			action, err := classifyInstanceStatus(test.status)
			if (err != nil) != test.wantErr {
				t.Fatalf("classifyInstanceStatus(%q) error = %v, wantErr %v", test.status, err, test.wantErr)
			}
			if action != test.action {
				t.Fatalf("classifyInstanceStatus(%q) = %v, want %v", test.status, action, test.action)
			}
		})
	}
}
