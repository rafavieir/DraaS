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
