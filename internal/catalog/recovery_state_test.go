package catalog

import "testing"

func TestRecoveryOperationIDDeterministic(t *testing.T) {
	a := RecoveryOperationID("job-123", RecoveryStageResourceProvision, "PROVISION_VM", "workload-1")
	b := RecoveryOperationID(" JOB-123 ", "resource_provision", "provision_vm", "workload-1")
	if a != b {
		t.Fatalf("operation ID is not normalized/deterministic: %q != %q", a, b)
	}
	if len(a) != len("op-")+32 {
		t.Fatalf("unexpected operation ID length: %q", a)
	}
}

func TestRecoveryOperationIDSeparatesStages(t *testing.T) {
	provision := RecoveryOperationID("job-123", RecoveryStageResourceProvision, "MUTATE", "vm")
	attach := RecoveryOperationID("job-123", RecoveryStageDiskAttach, "MUTATE", "vm")
	if provision == attach {
		t.Fatal("different stages produced the same operation ID")
	}
}

func TestRecoveryStagesOrder(t *testing.T) {
	stages := RecoveryStages()
	if stages[0] != RecoveryStageRequested {
		t.Fatalf("first stage=%q", stages[0])
	}
	if stages[len(stages)-1] != RecoveryStageReadyForActivation {
		t.Fatalf("last stage=%q", stages[len(stages)-1])
	}
	seen := map[string]bool{}
	for _, s := range stages {
		if seen[s] {
			t.Fatalf("duplicate stage %q", s)
		}
		seen[s] = true
	}
}

func TestTerminalRecoveryStatus(t *testing.T) {
	for _, status := range []string{"COMPLETED", "FAILED_FINAL", "CANCELLED_FINAL"} {
		if !TerminalRecoveryStatus(status) {
			t.Fatalf("%s should be terminal", status)
		}
	}
	for _, status := range []string{"READY_FOR_ACTIVATION", "RUNNING", "FAILED"} {
		if TerminalRecoveryStatus(status) {
			t.Fatalf("%s should not be terminal for reconciliation", status)
		}
	}
}

func TestCanTransition(t *testing.T) {
	if !CanTransition(RecoveryStageResourceProvision, RecoveryStageDiskMaterialize) {
		t.Fatal("expected RESOURCE_PROVISION -> DISK_MATERIALIZE")
	}
	if CanTransition(RecoveryStageResourceProvision, RecoveryStagePowerOn) {
		t.Fatal("unexpected RESOURCE_PROVISION -> POWER_ON")
	}
	if !CanTransition(RecoveryStagePowerOn, RecoveryStageFailed) {
		t.Fatal("failure transition should be allowed")
	}
}
