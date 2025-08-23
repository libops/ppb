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
	powerOnMutex       sync.Mutex
}

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

	if vm.Status == "RUNNING" {
		slog.Debug("VM is running")
		return m.setIp(vm)
	}

	slog.Debug("Instance status", "status", vm.Status, "instance", m.Name)
	if vm.Status != "TERMINATED" && vm.Status != "SUSPENDED" {
		return nil
	}

	err = m.powerOn(ctx, vm.Status)
	if err != nil {
		return fmt.Errorf("could not power on: %v", err)
	}

	return m.waitForInstanceRunning(ctx)
}

func (m *GoogleComputeEngine) getInstanceMetadata(ctx context.Context) (*compute.Instance, error) {
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
func (m *GoogleComputeEngine) waitForInstanceRunning(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for instance to start")
		case <-ticker.C:
			vm, err := m.getInstanceMetadata(ctx)
			if err != nil {
				slog.Warn("Failed to fetch instance status, retrying...", "error", err)
				continue
			}

			slog.Debug("Polling instance status", "status", vm.Status, "instance", m.Name)

			if vm.Status == "RUNNING" {
				return m.setIp(vm)
			}
		}
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
	m.powerOnMutex.Lock()
	defer m.powerOnMutex.Unlock()

	// Check if we're still in cooldown period
	if time.Since(m.LastPowerOnAttempt) < time.Duration(cooldownSeconds)*time.Second {
		slog.Debug("Power-on attempt skipped due to cooldown", "instance", m.Name, "cooldown", cooldownSeconds)
		// Still check if we have a host IP set from previous attempts
		if m.Host() == "" {
			return fmt.Errorf("backend not available and still in cooldown period")
		}
		return nil
	}

	// Update the last attempt time before making the API call
	m.LastPowerOnAttempt = time.Now()

	slog.Debug("Attempting power-on check", "instance", m.Name)
	return m.PowerOn(ctx)
}
