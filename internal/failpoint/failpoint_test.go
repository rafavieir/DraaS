package failpoint

import (
	"context"
	"errors"
	"testing"
)

func TestTriggerDisabled(t *testing.T) {
	t.Setenv("DRAAS_FAILPOINT", "")
	if err := Trigger(context.Background(), "after_vm_create"); err != nil {
		t.Fatalf("disabled failpoint returned error: %v", err)
	}
}

func TestTriggerMatching(t *testing.T) {
	t.Setenv("DRAAS_FAILPOINT", "after_network_create,after_vm_create")
	err := Trigger(context.Background(), "after_vm_create")
	if !errors.Is(err, ErrTriggered) {
		t.Fatalf("expected ErrTriggered, got %v", err)
	}
}

func TestTriggerNonMatching(t *testing.T) {
	t.Setenv("DRAAS_FAILPOINT", "after_network_create")
	if err := Trigger(context.Background(), "after_vm_create"); err != nil {
		t.Fatalf("non-matching failpoint returned error: %v", err)
	}
}
