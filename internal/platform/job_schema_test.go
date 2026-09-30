package platform

import (
	"testing"

	"github.com/draas-platform/draas/pkg/contracts"
)

func TestJobSubjectSchemaVersions(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{kind: "backup", want: "draas.jobs.backup.v1"},
		{kind: "verify", want: "draas.jobs.verify.v1"},
		{kind: "real-recovery-test", want: "draas.jobs.real-recovery-test.v1"},
		{kind: "vm-power", want: "draas.jobs.vm-power.v2"},
	}
	for _, tt := range tests {
		if got := jobSubject(tt.kind); got != tt.want {
			t.Fatalf("jobSubject(%q)=%q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestWorkerSupportsRejectsWrongSchema(t *testing.T) {
	ev := contracts.Event{Version: 1, TenantID: "lab-tenant", ResourceID: "job-1", Kind: "vm-power"}
	if workerSupports(ev, "draas.jobs.vm-power.v1") {
		t.Fatal("worker accepted vm-power on v1 subject")
	}
	if !workerSupports(ev, "draas.jobs.vm-power.v2") {
		t.Fatal("worker rejected vm-power on v2 subject")
	}
}

func TestWorkerSupportsLegacyJobsOnV1(t *testing.T) {
	ev := contracts.Event{Version: 1, TenantID: "lab-tenant", ResourceID: "job-1", Kind: "backup"}
	if !workerSupports(ev, "draas.jobs.backup.v1") {
		t.Fatal("worker rejected legacy backup on v1 subject")
	}
	if workerSupports(ev, "draas.jobs.backup.v2") {
		t.Fatal("worker accepted legacy backup on v2 subject")
	}
}
