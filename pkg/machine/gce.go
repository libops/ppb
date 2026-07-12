package machine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

type GoogleComputeEngine struct {
	ProjectId          string `yaml:"project_id"`
	Zone               string `yaml:"zone"`
	Name               string `yaml:"name"`
	UsePrivateIp       bool   `yaml:"usePrivateIp"`
	Lock               *semaphore.Weighted
	host               string
	hostMutex          sync.RWMutex
	LastPowerOnAttempt time.Time
	getInstanceHook    func(context.Context) (*compute.Instance, error)
	powerOnHook        func(context.Context, string) error
	pollInterval       time.Duration
	joinTimeout        time.Duration
	now                func() time.Time
}

type instanceStatusAction int

const (
	instanceReady instanceStatusAction = iota
	instanceStart
	instanceWait
)

func NewGceMachine() *GoogleComputeEngine {
	return &GoogleComputeEngine{
		Lock: semaphore.NewWeighted(1),
	}
}

func (m *GoogleComputeEngine) Host() string {
	m.hostMutex.RLock()
	defer m.hostMutex.RUnlock()
	return m.host
}

// SetHostForTesting sets the host IP for testing purposes
func (m *GoogleComputeEngine) SetHostForTesting(host string) {
	m.hostMutex.Lock()
	defer m.hostMutex.Unlock()
	m.host = host
}

func (m *GoogleComputeEngine) PowerOn(ctx context.Context) error {
	// get machine metadata
	vm, err := m.getInstanceMetadata(ctx)
	if err != nil {
		return fmt.Errorf("could not fetch instance metadata: %v", err)
	}

	slog.Debug("Instance status", "status", vm.Status, "instance", m.Name)
	action, err := classifyInstanceStatus(vm.Status)
	if err != nil {
		return err
	}
	startRequested := false
	switch action {
	case instanceReady:
		return m.setIp(vm)
	case instanceStart:
		if err := m.powerOn(ctx, vm.Status); err != nil {
			return m.joinAfterPowerOnError(ctx, fmt.Errorf("could not power on: %w", err))
		}
		startRequested = true
	case instanceWait:
		// Another request or operator has already initiated a state change.
		// Continue through the bounded state loop instead of returning without
		// a usable target IP.
	}

	return m.waitForInstanceRunning(ctx, startRequested)
}

func (m *GoogleComputeEngine) getInstanceMetadata(ctx context.Context) (*compute.Instance, error) {
	if m.getInstanceHook != nil {
		return m.getInstanceHook(ctx)
	}
	computeService, err := compute.NewService(ctx, option.WithScopes(compute.CloudPlatformScope))
	if err != nil {
		return nil, fmt.Errorf("failed to create compute service: %v", err)
	}

	// Fetch instance metadata
	instance, err := computeService.Instances.Get(m.ProjectId, m.Zone, m.Name).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get instance metadata: %v", err)
	}

	return instance, nil
}

func (m *GoogleComputeEngine) powerOn(ctx context.Context, status string) error {
	if m.powerOnHook != nil {
		return m.powerOnHook(ctx, status)
	}
	computeService, err := compute.NewService(ctx, option.WithScopes(compute.CloudPlatformScope))
	if err != nil {
		return fmt.Errorf("failed to create compute service: %v", err)
	}
	switch status {
	case "TERMINATED":
		_, err := computeService.Instances.Start(m.ProjectId, m.Zone, m.Name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to start instance: %v", err)
		}
	case "SUSPENDED":
		_, err := computeService.Instances.Resume(m.ProjectId, m.Zone, m.Name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to start instance: %v", err)
		}
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	slog.Info("Power button pressed", "currentStatus", status, "instance", m.Name)

	return nil
}

// Polls instance metadata until the VM is in the RUNNING state
func (m *GoogleComputeEngine) waitForInstanceRunning(ctx context.Context, startRequested bool) error {
	ticker := time.NewTicker(m.effectivePollInterval())
	defer ticker.Stop()

	seenTransition := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			vm, err := m.getInstanceMetadata(ctx)
			if err != nil {
				slog.Warn("Failed to fetch instance status, retrying...", "error", err)
				continue
			}

			slog.Debug("Polling instance status", "status", vm.Status, "instance", m.Name)

			action, err := classifyInstanceStatus(vm.Status)
			if err != nil {
				return err
			}
			switch action {
			case instanceReady:
				return m.setIp(vm)
			case instanceStart:
				if startRequested && !seenTransition {
					// The start/resume API can become visible before the instance
					// status changes. Avoid issuing the same mutation twice.
					continue
				}
				if err := m.powerOn(ctx, vm.Status); err != nil {
					return m.joinAfterPowerOnError(ctx, fmt.Errorf("could not power on after transitional state: %w", err))
				}
				startRequested = true
				seenTransition = false
			case instanceWait:
				seenTransition = true
				continue
			}
		}
	}
}

// joinAfterPowerOnError handles the cross-process race where another Cloud
// Run revision starts or resumes the same VM after both observed a terminal
// state. A conflicting mutation is successful from PPB's perspective once the
// instance is observed transitioning or running. Permanent failures still
// return within a short bounded window.
func (m *GoogleComputeEngine) joinAfterPowerOnError(ctx context.Context, powerErr error) error {
	timer := time.NewTimer(m.effectiveJoinTimeout())
	defer timer.Stop()
	ticker := time.NewTicker(m.effectivePollInterval())
	defer ticker.Stop()

	for {
		vm, err := m.getInstanceMetadata(ctx)
		if err == nil {
			action, classifyErr := classifyInstanceStatus(vm.Status)
			if classifyErr != nil {
				return classifyErr
			}
			switch action {
			case instanceReady:
				return m.setIp(vm)
			case instanceWait:
				return m.waitForInstanceRunning(ctx, false)
			case instanceStart:
				// The competing operation might not be visible yet.
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return powerErr
		case <-ticker.C:
		}
	}
}

func (m *GoogleComputeEngine) effectivePollInterval() time.Duration {
	if m.pollInterval > 0 {
		return m.pollInterval
	}
	return 2 * time.Second
}

func (m *GoogleComputeEngine) effectiveJoinTimeout() time.Duration {
	if m.joinTimeout > 0 {
		return m.joinTimeout
	}
	return 10 * time.Second
}

func (m *GoogleComputeEngine) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func classifyInstanceStatus(status string) (instanceStatusAction, error) {
	switch status {
	case "RUNNING":
		return instanceReady, nil
	case "TERMINATED", "SUSPENDED":
		return instanceStart, nil
	case "PROVISIONING", "STAGING", "STOPPING", "SUSPENDING", "REPAIRING":
		return instanceWait, nil
	default:
		return instanceWait, fmt.Errorf("unsupported instance status %q", status)
	}
}

func (m *GoogleComputeEngine) setIp(vm *compute.Instance) error {
	if len(vm.NetworkInterfaces) == 0 {
		return fmt.Errorf("no network interfaces found for instance %s", m.Name)
	}

	if m.UsePrivateIp && vm.NetworkInterfaces[0].NetworkIP == "" {
		return fmt.Errorf("no private IP found for instance %s", m.Name)
	}

	m.hostMutex.Lock()
	defer m.hostMutex.Unlock()

	if m.UsePrivateIp {
		m.host = vm.NetworkInterfaces[0].NetworkIP
		slog.Debug("Found private IP", "ip", m.host)
		return nil
	}

	for _, nic := range vm.NetworkInterfaces {
		if len(nic.AccessConfigs) > 0 && nic.AccessConfigs[0].NatIP != "" {
			m.host = nic.AccessConfigs[0].NatIP
			slog.Debug("Found public IP", "ip", m.host)
			return nil
		}
	}

	return fmt.Errorf("no public IP found for instance %s", m.Name)
}

// PowerOnWithCooldown attempts to power on the machine if enough time has elapsed since the last attempt
func (m *GoogleComputeEngine) PowerOnWithCooldown(ctx context.Context, cooldownSeconds int) error {
	if m.Lock == nil {
		return fmt.Errorf("machine power-on lock is not initialized")
	}
	if err := m.Lock.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("wait for concurrent power-on attempt: %w", err)
	}
	defer m.Lock.Release(1)

	now := m.currentTime()
	retryAt := m.LastPowerOnAttempt.Add(time.Duration(cooldownSeconds) * time.Second)
	if !m.LastPowerOnAttempt.IsZero() && now.Before(retryAt) {
		slog.Debug("Power-on attempt skipped due to cooldown", "instance", m.Name, "cooldown", cooldownSeconds)
		if m.Host() == "" {
			return m.waitForCooldownTransition(ctx, retryAt)
		}
		return nil
	}

	// Update the last attempt time before making the API call
	m.LastPowerOnAttempt = now

	slog.Debug("Attempting power-on check", "instance", m.Name)
	return m.PowerOn(ctx)
}

// waitForCooldownTransition lets a caller join an accepted start whose state
// change was not visible before the previous request was cancelled. It polls
// without issuing another mutation until the cooldown expires, then makes one
// fresh power-on attempt if the VM is still terminal.
func (m *GoogleComputeEngine) waitForCooldownTransition(ctx context.Context, retryAt time.Time) error {
	ticker := time.NewTicker(m.effectivePollInterval())
	defer ticker.Stop()

	for {
		vm, err := m.getInstanceMetadata(ctx)
		if err == nil {
			action, classifyErr := classifyInstanceStatus(vm.Status)
			if classifyErr != nil {
				return classifyErr
			}
			switch action {
			case instanceReady:
				return m.setIp(vm)
			case instanceWait:
				return m.waitForInstanceRunning(ctx, false)
			case instanceStart:
				// Wait out the mutation cooldown before retrying.
			}
		}

		if !m.currentTime().Before(retryAt) {
			m.LastPowerOnAttempt = m.currentTime()
			return m.PowerOn(ctx)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
