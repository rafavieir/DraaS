package blockstorage

import "testing"

func TestTargetDataset(t *testing.T) {
	got, err := TargetDataset("tank", "tenant-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tank/draas/tenants/tenant-a/recoveries/job-1" {
		t.Fatalf("unexpected dataset %q", got)
	}
}

func TestDecodeRecoveryPoint(t *testing.T) {
	rp, ok := DecodeRecoveryPoint([]byte(`{"storage_backend":"zfs","tenant":"t","workload_id":"w","recovery_point":"p","zfs_snapshot":"tank/draas/t/w@p"}`))
	if !ok || rp.ZFSSnapshot == "" {
		t.Fatalf("expected zfs recovery point, got ok=%v rp=%+v", ok, rp)
	}
	rp, ok = DecodeRecoveryPoint([]byte(`{"storage_backend":"kubernetes-zfs","tenant":"t","workload_id":"w","recovery_point":"p","size_bytes":1073741824,"metadata":{"kubernetes_volume_snapshot":"snap-p"}}`))
	if !ok || rp.Metadata["kubernetes_volume_snapshot"] != "snap-p" {
		t.Fatalf("expected kubernetes zfs recovery point, got ok=%v rp=%+v", ok, rp)
	}
	_, ok = DecodeRecoveryPoint([]byte(`{"storage_backend":"s3"}`))
	if ok {
		t.Fatal("s3 recovery point must not be accepted as zfs")
	}
}

func TestUnsafeDatasetRejected(t *testing.T) {
	bad := []string{"", "/tank/x", "tank/../x", "tank/x;rm", "tank/x y", "tank/x`whoami`"}
	for _, v := range bad {
		if safeDataset(v) {
			t.Fatalf("expected unsafe dataset %q to be rejected", v)
		}
	}
}
