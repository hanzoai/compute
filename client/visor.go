// Package client — canonical client interface for Hanzo Visor.
//
//	import visor "github.com/hanzoai/vm/client"
//	var v visor.VM = visor.NewClient(cfg)

package client

import (
	"context"
	"time"
)

// VM is the cloud-VM-management surface across providers (Hetzner,
// AWS Lightsail, DigitalOcean, Azure, GCP, Proxmox, VMware, KVM).
type VM interface {
	// Kind reports the backend identifier (hanzo-visor).
	Kind() string

	// ListProviders returns the providers configured for the caller's
	// org with per-provider capability bits.
	ListProviders(ctx context.Context) ([]Provider, error)

	// CreateMachine provisions a VM at the named provider. The call
	// returns when the provider has accepted the request; the machine
	// transitions to "running" asynchronously and the caller polls
	// via GetMachine.
	CreateMachine(ctx context.Context, req CreateMachineRequest) (*Machine, error)

	// GetMachine returns the current state of a machine.
	GetMachine(ctx context.Context, machineID string) (*Machine, error)

	// ListMachines returns machines visible to the caller's org.
	ListMachines(ctx context.Context, filter MachineFilter) ([]Machine, error)

	// StartMachine boots a stopped machine. Idempotent.
	StartMachine(ctx context.Context, machineID string) error

	// StopMachine shuts down a running machine. Idempotent.
	StopMachine(ctx context.Context, machineID string) error

	// RestartMachine reboots a machine.
	RestartMachine(ctx context.Context, machineID string) error

	// DeleteMachine destroys a machine and frees its resources.
	DeleteMachine(ctx context.Context, machineID string) error

	// CreateVolume provisions a block volume. Some providers
	// (Lightsail) do not support volumes and return ErrUnsupported.
	CreateVolume(ctx context.Context, req CreateVolumeRequest) (*Volume, error)

	// AttachVolume binds a volume to a machine. Both must be in the
	// same provider region.
	AttachVolume(ctx context.Context, volumeID, machineID string) error

	// DetachVolume unbinds a volume.
	DetachVolume(ctx context.Context, volumeID string) error

	// DeleteVolume removes a volume. Attached volumes are detached
	// first.
	DeleteVolume(ctx context.Context, volumeID string) error

	// EstimateCost returns the per-hour and per-month cost for a
	// machine spec. Cost data comes from the billing package's plan
	// seed; vendors without published pricing return ErrPricingUnknown.
	EstimateCost(ctx context.Context, req CostEstimateRequest) (*CostEstimate, error)
}

// Provider names one cloud backend and its capability set.
type Provider struct {
	Name          string // hetzner | lightsail | digitalocean | azure | gcp | aliyun | proxmox | vmware | kvm
	DisplayName   string
	HasVolumes    bool
	HasFloatingIP bool
	Regions       []string
}

// CreateMachineRequest is the VM-create payload.
type CreateMachineRequest struct {
	Provider  string
	Region    string
	// PlanID names the size-class from the billing plan catalog
	// (e.g. hetzner-cx22, lightsail-medium-2gb).
	PlanID    string
	// Image is the OS image identifier (provider-specific).
	Image     string
	Hostname  string
	// SSHKeys are public keys to install on the new machine.
	SSHKeys   []string
	// UserData is cloud-init / startup-script content.
	UserData  string
	// Tags carry org + label metadata for billing + filtering.
	Tags      map[string]string
}

// Machine is the canonical VM record.
type Machine struct {
	ID            string
	OrgID         string
	Provider      string
	Region        string
	PlanID        string
	Image         string
	Hostname      string
	Status        string // provisioning | running | stopping | stopped | deleting | failed
	IPv4          string
	IPv6          string
	PrivateIPv4   string
	Tags          map[string]string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// LastError populated when Status=failed.
	LastError string
}

// MachineFilter scopes a ListMachines call.
type MachineFilter struct {
	Provider string
	Region   string
	Status   string
	Tag      string
	Page     int
	PerPage  int
}

// CreateVolumeRequest is the block-volume payload.
type CreateVolumeRequest struct {
	Provider  string
	Region    string
	SizeGB    int
	// Type is the provider-specific tier (ssd | hdd | nvme).
	Type      string
	Encrypted bool
	Tags      map[string]string
}

// Volume is the canonical block-volume record.
type Volume struct {
	ID         string
	OrgID      string
	Provider   string
	Region     string
	SizeGB     int
	Type       string
	Encrypted  bool
	Status     string // creating | available | in_use | deleting | deleted
	AttachedTo string // machine id when in_use
	CreatedAt  time.Time
}

// CostEstimateRequest names the spec to price.
type CostEstimateRequest struct {
	Provider  string
	Region    string
	PlanID    string
	VolumeGB  int    // total attached storage; 0 disables volume pricing
	VolumeType string
}

// CostEstimate is the response.
type CostEstimate struct {
	Currency     string
	PerHourUSD   float64
	PerMonthUSD  float64
	BreakdownBy  map[string]float64 // compute | storage | bandwidth | ipv4 | ...
}
