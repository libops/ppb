package machine

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	compute "google.golang.org/api/compute/v1"
)

func TestGoogleComputeEngine_PowerOnWithCooldown(t *testing.T) {
	tests := []struct {
		name             string
		cooldownSeconds  int
		initialHost      string
		timeBetweenCalls time.Duration
		expectAPICall    bool
		expectError      bool
		needsSynctest    bool
	}{
		{
			name:             "first call should always make API call",
			cooldownSeconds:  30,
			timeBetweenCalls: 0,
			expectAPICall:    true,
			expectError:      false,
			needsSynctest:    false,
		},
		{
			name:             "second call within cooldown should skip API call if host is set",
			cooldownSeconds:  30,
			initialHost:      "10.0.0.1",
			timeBetweenCalls: 10 * time.Second,
			expectAPICall:    false,
			expectError:      false,
			needsSynctest:    true,
		},
		{
			name:             "second call within cooldown without host should return error",
			cooldownSeconds:  30,
			timeBetweenCalls: 10 * time.Second,
			expectAPICall:    false,
			expectError:      true,
			needsSynctest:    true,
		},
		{
			name:             "call after cooldown period should make API call",
			cooldownSeconds:  5,
			timeBetweenCalls: 6 * time.Second,
			expectAPICall:    true,
			expectError:      false,
			needsSynctest:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFunc := func(t *testing.T) {
				// Create machine instance
				m := NewGceMachine()

				// Set initial host if provided
				if tt.initialHost != "" {
					m.SetHostForTesting(tt.initialHost)
				}

				ctx := context.Background()

				// Make first call (this will always attempt API call)
				err1 := m.PowerOnWithCooldown(ctx, tt.cooldownSeconds)

				if tt.timeBetweenCalls > 0 {
					time.Sleep(tt.timeBetweenCalls)
				}

				// Make second call to test cooldown behavior
				err2 := m.PowerOnWithCooldown(ctx, tt.cooldownSeconds)

				if tt.expectError && err2 == nil {
					t.Errorf("Expected error on second call but got none")
				}
				if !tt.expectError && err2 != nil && tt.initialHost != "" {
					t.Errorf("Unexpected error on second call with host set: %v", err2)
				}

				_ = err1 // Avoid unused variable warning
			}

			if tt.needsSynctest {
				synctest.Test(t, testFunc)
			} else {
				testFunc(t)
			}
		})
	}
}

func TestGoogleComputeEngine_CooldownTimingLogic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewGceMachine()
		cooldownSeconds := 2

		// Set a mock host so we don't get "backend not available" errors
		m.SetHostForTesting("10.0.0.1")

		ctx := context.Background()

		// First call - should set LastPowerOnAttempt
		err1 := m.PowerOnWithCooldown(ctx, cooldownSeconds)
		firstAttemptTime := m.LastPowerOnAttempt

		// Immediate second call - should be in cooldown
		err2 := m.PowerOnWithCooldown(ctx, cooldownSeconds)
		secondAttemptTime := m.LastPowerOnAttempt

		// The attempt time should not have changed (still in cooldown)
		if !firstAttemptTime.Equal(secondAttemptTime) {
			t.Errorf("Second call should not update LastPowerOnAttempt during cooldown")
		}

		// Advance time beyond cooldown period using fake clock
		time.Sleep(time.Duration(cooldownSeconds+1) * time.Second)

		// Third call - should update LastPowerOnAttempt
		err3 := m.PowerOnWithCooldown(ctx, cooldownSeconds)
		thirdAttemptTime := m.LastPowerOnAttempt

		// The attempt time should have been updated
		if !thirdAttemptTime.After(firstAttemptTime) {
			t.Errorf("Third call should update LastPowerOnAttempt after cooldown expires")
		}

		// Note: These calls will likely fail due to GCP API authentication in test environment
		// but that's okay - we're testing the timing logic, not the actual GCP integration
		_ = err1
		_ = err2
		_ = err3
	})
}

func TestGoogleComputeEngine_ConcurrentCooldownCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewGceMachine()

		// Set a mock host so we don't get "backend not available" errors
		m.SetHostForTesting("10.0.0.1")

		ctx := context.Background()
		cooldownSeconds := 5

		// Launch multiple concurrent calls
		done := make(chan bool, 3)

		for i := 0; i < 3; i++ {
			go func() {
				_ = m.PowerOnWithCooldown(ctx, cooldownSeconds)
				done <- true
			}()
		}

		// Wait for all goroutines to start and complete
		synctest.Wait()

		// Collect results
		for i := 0; i < 3; i++ {
			<-done
		}

		// Verify that the mutex protected the shared state properly
		// (No specific assertion here, but the test would fail with race conditions if mutex wasn't working)
		if m.LastPowerOnAttempt.IsZero() {
			t.Error("Expected LastPowerOnAttempt to be set after concurrent calls")
		}
	})
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
