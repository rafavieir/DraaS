package recoverylab

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/failpoint"
	"github.com/draas-platform/draas/providers/libvirtlab"
	"os"
	"path/filepath"
	"time"
)

type Options struct {
	Tenant        string
	RunID         string
	Root          string
	FixtureBinary string
	Engine        *backup.Engine
	KeepResources bool
}

type SignedReport struct {
	Report            map[string]any `json:"report"`
	Signature         []byte         `json:"signature"`
	PublicKey         []byte         `json:"public_key"`
	SignatureEncoding string         `json:"signature_encoding"`
}

func Run(ctx context.Context, opt Options, progress func(stage string, bytes int64)) (SignedReport, error) {
	if opt.Root == "" {
		opt.Root = "/var/lib/draas-lab"
	}
	if opt.FixtureBinary == "" {
		opt.FixtureBinary = "/usr/local/bin/draas-fixture"
	}
	if opt.Tenant == "" || opt.RunID == "" || opt.Engine == nil {
		return SignedReport{}, fmt.Errorf("tenant, run id and engine are required")
	}
	p, err := libvirtlab.New(opt.Root, opt.FixtureBinary)
	if err != nil {
		return SignedReport{}, err
	}
	dir, err := p.Dir(opt.RunID)
	if err != nil {
		return SignedReport{}, err
	}
	report := map[string]any{
		"run_id": opt.RunID, "started_at": time.Now().UTC(),
		"production_ready": false, "billing_mode": "TEST", "billable": false,
		"simulation": false, "status": "RUNNING", "provider_type": "LIBVIRT_LAB",
		"environment_type": "TEST", "keep_resources": opt.KeepResources,
	}
	var owned []libvirtlab.VM
	save := func(name string, v any) error {
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(dir, name), b, 0600)
	}
	step := func(name string) {
		report["stage"] = name
		if progress != nil {
			progress(name, 0)
		}
		_ = save("report.json", report)
	}
	cleanupOnError := func(runErr error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cleanupErrors := []string{}
		for _, vm := range owned {
			if err := p.Delete(cleanupCtx, vm); err != nil {
				cleanupErrors = append(cleanupErrors, err.Error())
			}
		}
		report["cleanup_errors"] = cleanupErrors
		report["cleanup_verified"] = len(cleanupErrors) == 0
		report["status"] = "FAILED"
		report["error"] = runErr.Error()
		report["finished_at"] = time.Now().UTC()
		_ = save("report.json", report)
	}
	stageProgress := func(stage string) func(int64) {
		return func(n int64) {
			if progress != nil {
				progress(stage, n)
			}
		}
	}
	health, err := p.Health(ctx)
	if err != nil {
		return SignedReport{}, err
	}
	report["hypervisor"] = health
	step("PREPARE_SOURCE")
	source, sum, err := p.PrepareSource(ctx, opt.Tenant, opt.RunID)
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	owned = append(owned, source)
	report["cloud_image_sha512"] = sum
	report["source_vm"] = source.ID
	step("BOOT_SOURCE")
	if err = p.Start(ctx, source); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	guestCtx, guestCancel := context.WithTimeout(ctx, 6*time.Minute)
	original, err := p.WaitGuest(guestCtx, source)
	guestCancel()
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["original_guest"] = original
	console, err := p.Command(ctx, source, "uname -s; cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["original_console"] = console
	capture := func(point string) (backup.Manifest, backup.Stats, error) {
		stopCtx, stopCancel := context.WithTimeout(ctx, 90*time.Second)
		defer stopCancel()
		if e := p.Stop(stopCtx, source); e != nil {
			return backup.Manifest{}, backup.Stats{}, e
		}
		f, e := os.Open(source.Disk)
		if e != nil {
			return backup.Manifest{}, backup.Stats{}, e
		}
		defer f.Close()
		return opt.Engine.Ingest(ctx, opt.Tenant, point, source.ID, "libvirt-kvm-lab", map[string]string{"disk_format": "qcow2", "consistency": "COLD_FILESYSTEM", "capture": "FULL_SCAN_DEDUP_NOT_CBT"}, f, stageProgress("DISK_CAPTURE"))
	}
	step("FULL_BACKUP")
	full, fullStats, err := capture(opt.RunID + "-full")
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["full"] = fullStats
	_ = save("full-manifest.json", full)
	step("MUTATE_SOURCE")
	if err = p.Start(ctx, source); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if _, err = p.WaitGuest(ctx, source); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	expected, err := p.Mutate(ctx, source)
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if expected.Database == original.Database {
		err = fmt.Errorf("source mutation did not change database")
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["expected_guest"] = expected
	step("INCREMENTAL_BACKUP")
	incremental, incrementalStats, err := capture(opt.RunID + "-incremental")
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["incremental"] = incrementalStats
	_ = save("incremental-manifest.json", incremental)
	if incrementalStats.Reused == 0 || incrementalStats.Uploaded == 0 {
		err = fmt.Errorf("incremental change and reuse not proven")
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if _, _, err = opt.Engine.Verify(ctx, opt.Tenant, incremental.ID); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	step("DESTROY_SOURCE")
	if err = p.Delete(ctx, source); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	owned = removeVM(owned, source.ID)
	report["source_destroyed"] = true
	step("RESTORE_DISK")
	started := time.Now()
	target := filepath.Join(dir, "recovery.qcow2")
	partial := target + ".partial"
	if err = os.Remove(partial); err != nil && !os.IsNotExist(err) {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	stats, err := opt.Engine.Restore(ctx, incremental, opt.Tenant, incremental.ID, f, stageProgress("DISK_RESTORE"))
	if e := failpoint.Trigger(ctx, "during_disk_materialize"); e != nil && err == nil {
		err = e
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(partial)
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if closeErr != nil {
		_ = os.Remove(partial)
		cleanupOnError(closeErr)
		return SignedReport{}, closeErr
	}
	hash, err := libvirtlab.HashFile(partial)
	if err != nil {
		_ = os.Remove(partial)
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if hash != incremental.StreamHash {
		_ = os.Remove(partial)
		err = fmt.Errorf("restored disk hash mismatch")
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if err = os.Rename(partial, target); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if err = failpoint.Trigger(ctx, "after_disk_materialize"); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["restore"] = stats
	report["disk_sha256"] = hash
	step("BOOT_RECOVERY")
	provisionOp := catalog.RecoveryOperationID(opt.RunID, catalog.RecoveryStageResourceProvision, "PROVISION_VM", "recovery")
	recovered, err := p.CreateWithOperation(ctx, opt.Tenant, opt.RunID, "recovery", target, "", true, provisionOp, incremental.ID, "TEST")
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	owned = append(owned, recovered)
	if err = p.Start(ctx, recovered); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if err = failpoint.Trigger(ctx, "after_power_on"); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	validationCtx, validationCancel := context.WithTimeout(ctx, 3*time.Minute)
	if err = failpoint.Trigger(ctx, "before_app_validation"); err != nil {
		validationCancel()
		cleanupOnError(err)
		return SignedReport{}, err
	}
	validation, err := p.Validate(validationCtx, recovered, expected)
	validationCancel()
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	if err = failpoint.Trigger(ctx, "after_app_validation"); err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["rto_seconds"] = time.Since(started).Seconds()
	report["validation"] = validation
	step("VERIFY_NETWORK")
	egress, err := p.CheckEgress(ctx, recovered)
	if err != nil {
		cleanupOnError(err)
		return SignedReport{}, err
	}
	report["egress_check"] = egress
	if opt.KeepResources {
		report["resources_preserved"] = true
		report["cleanup_verified"] = false
		report["recovered_vm"] = recovered
		report["provider_resource_ids"] = []string{recovered.ID}
		report["stage"] = "READY_FOR_ACTIVATION"
	} else {
		step("CLEANUP_TEST")
		if err = p.Delete(ctx, recovered); err != nil {
			cleanupOnError(err)
			return SignedReport{}, err
		}
		owned = removeVM(owned, recovered.ID)
		report["resources_preserved"] = false
		report["cleanup_verified"] = true
	}
	report["status"] = "PASS"
	report["finished_at"] = time.Now().UTC()
	signature, err := opt.Engine.Sign(report)
	if err != nil {
		return SignedReport{}, err
	}
	if !backup.VerifySigned(report, signature, opt.Engine.Trusted) {
		return SignedReport{}, fmt.Errorf("report signature validation failed")
	}
	envelope := SignedReport{Report: report, Signature: signature, PublicKey: opt.Engine.Trusted, SignatureEncoding: "json-map-v1"}
	if err = save("signed-report.json", envelope); err != nil {
		return SignedReport{}, err
	}
	return envelope, nil
}

func removeVM(in []libvirtlab.VM, id string) []libvirtlab.VM {
	out := in[:0]
	for _, v := range in {
		if v.ID != id {
			out = append(out, v)
		}
	}
	return out
}
