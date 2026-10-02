package blockstorage

import "testing"

func TestTargetPVCName(t *testing.T) {
	got := TargetPVCName("tenant", "job")
	if got != "draas-tenant-job" {
		t.Fatalf("unexpected pvc name %q", got)
	}
	long := TargetPVCName("tenant", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz")
	if len(long) > 63 {
		t.Fatalf("pvc name must fit kubernetes DNS label, got %d", len(long))
	}
}

func TestPVCFromSnapshot(t *testing.T) {
	pvc := pvcFromSnapshot("ns", "pvc", "zfs-sc", "snap", 1024, "tenant", "job", "op")
	if pvc.GetName() != "pvc" || pvc.GetNamespace() != "ns" {
		t.Fatalf("unexpected identity %s/%s", pvc.GetNamespace(), pvc.GetName())
	}
	spec := pvc.Object["spec"].(map[string]any)
	if spec["storageClassName"] != "zfs-sc" {
		t.Fatalf("unexpected storage class %#v", spec["storageClassName"])
	}
	dataSource := spec["dataSource"].(map[string]any)
	if dataSource["name"] != "snap" || dataSource["kind"] != "VolumeSnapshot" {
		t.Fatalf("unexpected datasource %#v", dataSource)
	}
}
