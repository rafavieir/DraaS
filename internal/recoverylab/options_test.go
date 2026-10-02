package recoverylab

import (
	"testing"

	"github.com/draas-platform/draas/internal/catalog"
)

func TestProvisionOperationIDUsesRecoveryJobWhenProvided(t *testing.T) {
	jobID := "1790000000000-aaaaaaaaaaaaaaaaaaaaaaaa"
	op := catalog.RecoveryOperationID(jobID, catalog.RecoveryStageResourceProvision, "PROVISION_VM", "recovery")
	opt := Options{RunID: "p3a-" + jobID + "-attempt-1", RecoveryJobID: jobID, ProvisionOperationID: op}
	if opt.ProvisionOperationID != op {
		t.Fatalf("provision operation ID changed: %q != %q", opt.ProvisionOperationID, op)
	}
}
