package contracts

import (
	"context"
	"io"
	"time"
)

type Capabilities struct {
	Snapshot              bool `json:"snapshot"`
	ChangedBlocks         bool `json:"changed_blocks"`
	ApplicationConsistent bool `json:"application_consistent"`
}
type Workload struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Revision int    `json:"revision"`
	Size     int64  `json:"size"`
}
type Snapshot struct {
	Reader   io.ReadCloser
	Size     int64
	Metadata map[string]string
}
type SourceConnector interface {
	Capabilities() Capabilities
	Discover(context.Context) ([]Workload, error)
	PrepareSnapshot(context.Context, Workload) (Snapshot, error)
	FinalizeSnapshot(context.Context, Workload) error
}
type Validation struct {
	Provider     string `json:"provider"`
	Simulation   bool   `json:"simulation"`
	BootVerified bool   `json:"boot_verified"`
	FileValid    bool   `json:"file_valid"`
	ServiceValid bool   `json:"service_valid"`
	Isolated     bool   `json:"isolated"`
	Cleanup      bool   `json:"cleanup"`
	Detail       string `json:"detail"`
}

// Providers own isolated lifecycle and must clean up on success, error and cancellation.
// Production VM lifecycle will extend this narrow orchestration boundary.
type RecoveryProvider interface {
	Name() string
	Validate(context.Context, io.Reader) (Validation, error)
}

const RecoveryProviderContractV2 = "recovery-provider/v2"

type RecoveryProviderCapabilities struct {
	ContractVersion       string `json:"contract_version"`
	SupportsConsole       bool   `json:"supports_console"`
	SupportsHotAttach     bool   `json:"supports_hot_attach"`
	SupportsNetworkCreate bool   `json:"supports_network_create"`
	SupportsGuestAgent    bool   `json:"supports_guest_agent"`
	SupportsReboot        bool   `json:"supports_reboot"`
	SupportsForceStop     bool   `json:"supports_force_stop"`
	SupportsTags          bool   `json:"supports_tags"`
	SupportsAsyncTasks    bool   `json:"supports_async_tasks"`
}

type ResourceHandle struct {
	ProviderResourceID string            `json:"provider_resource_id"`
	ProviderTaskID     string            `json:"provider_task_id,omitempty"`
	ProviderCluster    string            `json:"provider_cluster,omitempty"`
	Metadata           map[string]string `json:"provider_metadata,omitempty"`
}

type WorkloadSpec struct {
	TenantID  string            `json:"tenant_id"`
	Workload  Workload          `json:"workload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Isolation string            `json:"isolation,omitempty"`
}

type NetworkSpec struct {
	Name     string            `json:"name"`
	Isolated bool              `json:"isolated"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DiskSpec struct {
	Name       string            `json:"name"`
	SizeBytes  int64             `json:"size_bytes"`
	SourceURI  string            `json:"source_uri,omitempty"`
	SHA256     string            `json:"sha256,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	ReadOnly   bool              `json:"read_only,omitempty"`
	Boot       bool              `json:"boot,omitempty"`
	DeviceHint string            `json:"device_hint,omitempty"`
}

type ProviderResource struct {
	Handle    ResourceHandle `json:"handle"`
	Kind      string         `json:"kind"`
	State     string         `json:"state"`
	ManagedBy string         `json:"managed_by"`
	TenantID  string         `json:"tenant_id,omitempty"`
}

type PowerState struct {
	State string `json:"state"`
}

type GuestState struct {
	Healthy bool              `json:"healthy"`
	BootID  string            `json:"boot_id,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

type ConsoleSession struct {
	URL       string    `json:"url,omitempty"`
	Protocol  string    `json:"protocol"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type RecoveryProviderV2 interface {
	Name() string
	Capabilities(context.Context) (RecoveryProviderCapabilities, error)
	Health(context.Context) (map[string]string, error)
	ValidateCredentials(context.Context) error
	Provision(context.Context, string, WorkloadSpec) (ResourceHandle, error)
	GetResource(context.Context, ResourceHandle) (ProviderResource, error)
	CreateNetwork(context.Context, string, NetworkSpec) (ResourceHandle, error)
	DeleteNetwork(context.Context, string, ResourceHandle) error
	AttachDisk(context.Context, string, ResourceHandle, DiskSpec) (ResourceHandle, error)
	ConfigureNetwork(context.Context, string, ResourceHandle, NetworkSpec) error
	Start(context.Context, string, ResourceHandle) error
	Shutdown(context.Context, string, ResourceHandle) error
	Reboot(context.Context, string, ResourceHandle) error
	ForceStop(context.Context, string, ResourceHandle) error
	GetPowerState(context.Context, ResourceHandle) (PowerState, error)
	GetGuestState(context.Context, ResourceHandle) (GuestState, error)
	OpenConsole(context.Context, ResourceHandle) (ConsoleSession, error)
	Delete(context.Context, string, ResourceHandle) error
	ListManagedResources(context.Context) ([]ProviderResource, error)
}

type Event struct {
	TraceParent   string    `json:"traceparent,omitempty"`
	EventID       string    `json:"event_id"`
	TenantID      string    `json:"tenant_id"`
	CorrelationID string    `json:"correlation_id"`
	Timestamp     time.Time `json:"timestamp"`
	Version       int       `json:"version"`
	ResourceID    string    `json:"resource_id"`
	Kind          string    `json:"kind"`
}
