package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	source "github.com/draas-platform/draas/connectors/simulator"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/recoverylab"
	"github.com/draas-platform/draas/pkg/contracts"
	libvirtlab "github.com/draas-platform/draas/providers/libvirtlab"
	recovery "github.com/draas-platform/draas/providers/simulator"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"log/slog"
	"os"
	"time"
)

func (a *App) PublishOutbox(ctx context.Context) error {
	rows, err := a.DB.Pool.Query(ctx, "SELECT id,event FROM outbox WHERE published_at IS NULL ORDER BY created_at LIMIT 50")
	if err != nil {
		return err
	}
	type entry struct {
		id string
		b  []byte
	}
	var batch []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.b); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, e := range batch {
		var event contracts.Event
		if err = json.Unmarshal(e.b, &event); err != nil {
			return err
		}
		if _, err = a.JS.Publish(jobSubject(event.Kind), e.b, nats.MsgId(e.id), nats.Context(ctx)); err != nil {
			return err
		}
		if _, err = a.DB.Pool.Exec(ctx, "UPDATE outbox SET published_at=now() WHERE id=$1", e.id); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) OutboxLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if err := a.PublishOutbox(ctx); err != nil && ctx.Err() == nil {
			slog.Error("outbox publish", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (a *App) RunWorker(ctx context.Context) error {
	sub, err := a.JS.PullSubscribe("draas.jobs.*.*", "draas-workers-v2", nats.BindStream("DRAAS_JOBS"), nats.ManualAck(), nats.AckExplicit(), nats.AckWait(45*time.Second), nats.MaxDeliver(10), nats.MaxAckPending(16))
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	for ctx.Err() == nil {
		msgs, err := sub.Fetch(1, nats.MaxWait(time.Second))
		if errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, m := range msgs {
			if err = a.process(ctx, m); err != nil {
				slog.Error("job delivery failed", "error", err)
				_ = m.NakWithDelay(3 * time.Second)
			}
		}
	}
	return nil
}
func (a *App) process(ctx context.Context, msg *nats.Msg) error {
	var ev contracts.Event
	if err := json.Unmarshal(msg.Data, &ev); err != nil || !workerSupports(ev, msg.Subject) {
		return msg.Term()
	}
	conn, err := a.DB.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", ev.TenantID+"/"+ev.ResourceID).Scan(&locked)
	if err != nil {
		return err
	}
	if !locked {
		return msg.NakWithDelay(2 * time.Second)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1,0))", ev.TenantID+"/"+ev.ResourceID).Scan(&unlocked); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	j, err := a.DB.Job(ctx, ev.TenantID, ev.ResourceID)
	if err != nil {
		return err
	}
	if j.Kind != ev.Kind {
		return msg.Term()
	}
	if j.Status == "COMPLETED" || j.Status == "FAILED" || j.Status == "CANCELLED" {
		return msg.AckSync()
	}
	if j.Attempts >= 3 {
		if err = a.DB.Finish(ctx, j, "FAILED", nil, "retry budget exhausted after worker interruption"); err != nil {
			return err
		}
		return msg.AckSync()
	}
	_, err = conn.Exec(ctx, "UPDATE jobs SET status='RUNNING',attempts=attempts+1,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND NOT cancel_requested", j.Tenant, j.ID)
	if err != nil {
		return err
	}
	j.Attempts++
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": ev.TraceParent})
	timeout := 3 * time.Minute
	if j.Kind == "real-recovery-test" {
		timeout = 20 * time.Minute
	}
	if j.Kind == "vm-power" {
		timeout = 8 * time.Minute
	}
	workCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workCtx, span := otel.Tracer("draas.worker").Start(workCtx, j.Kind)
	defer span.End()
	span.SetAttributes(attribute.String("tenant.id", j.Tenant), attribute.String("correlation.id", j.ID))
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-tick.C:
				// A lost lock session must stop its execution before redelivery proceeds.
				if e := conn.Ping(workCtx); e != nil {
					cancel()
					return
				}
				var requested bool
				e := a.DB.Pool.QueryRow(workCtx, "SELECT cancel_requested FROM jobs WHERE tenant_id=$1 AND id=$2", j.Tenant, j.ID).Scan(&requested)
				if e != nil || requested {
					cancel()
					return
				}
				if e = msg.InProgress(); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	slog.Info("job started", "kind", j.Kind, "tenant", j.Tenant, "correlation_id", j.ID, "attempt", j.Attempts)
	result, runErr := a.execute(workCtx, j)
	cancel()
	<-done
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	if runErr != nil {
		span.RecordError(runErr)
		state := "RETRYING"
		if j.Attempts >= 3 {
			state = "FAILED"
		}
		if err = a.DB.Finish(finishCtx, j, state, nil, runErr.Error()); err != nil {
			return err
		}
		slog.Error("job execution", "correlation_id", j.ID, "error", runErr)
		if state == "FAILED" {
			return msg.AckSync()
		}
		return msg.NakWithDelay(time.Duration(j.Attempts*2) * time.Second)
	}
	if err = a.DB.Finish(finishCtx, j, "COMPLETED", result, ""); err != nil {
		return err
	}
	slog.Info("job completed", "kind", j.Kind, "correlation_id", j.ID)
	return msg.AckSync()
}
func (a *App) execute(ctx context.Context, j catalog.Job) (any, error) {
	progress := func(n int64) {
		_, err := a.DB.Pool.Exec(ctx, "UPDATE jobs SET progress_bytes=$3,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND NOT cancel_requested", j.Tenant, j.ID, n)
		if err != nil {
			slog.Error("persist progress", "correlation_id", j.ID, "error", err)
		}
	}
	switch j.Kind {
	case "backup":
		var w catalog.Workload
		if err := json.Unmarshal(j.Payload, &w); err != nil {
			return nil, err
		}
		src := source.Source{}
		snapshot, err := src.PrepareSnapshot(ctx, w.Workload)
		if err != nil {
			return nil, err
		}
		defer snapshot.Reader.Close()
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := src.FinalizeSnapshot(cleanup, w.Workload); err != nil {
				slog.Error("snapshot cleanup", "error", err, "correlation_id", j.ID)
			}
		}()
		m, stats, err := a.Engine.Ingest(ctx, j.Tenant, j.ID, w.ID, w.Source, snapshot.Metadata, snapshot.Reader, progress)
		if err != nil {
			return nil, err
		}
		if err = a.DB.SavePoint(ctx, m, stats); err != nil {
			return nil, err
		}
		// Auditing is independently queued; a stored point is not counted as verified yet.
		if _, err = a.DB.Enqueue(ctx, j.Tenant, "worker", "verify", m.ID, "auto-verify-"+m.ID, map[string]string{"point_id": m.ID}); err != nil {
			return nil, err
		}
		return map[string]any{"recovery_point": m.ID, "stats": stats, "state": "STORED", "audit": "QUEUED"}, nil
	case "verify":
		m, s, err := a.Engine.Verify(ctx, j.Tenant, j.ResourceID)
		if err != nil {
			_ = a.DB.SetPointStatus(ctx, j.Tenant, j.ResourceID, "INVALID")
			return nil, err
		}
		if err = a.DB.SetPointStatus(ctx, j.Tenant, m.ID, "VERIFIED"); err != nil {
			return nil, err
		}
		return map[string]any{"manifest_hash": m.StreamHash, "merkle_root": m.MerkleRoot, "signature": "VERIFIED", "immutability": "NOT_CONFIGURED", "anchor": "NOT_CONFIGURED", "stats": s}, nil
	case "test", "restore":
		started := time.Now().UTC()
		status, err := a.DB.PointStatus(ctx, j.Tenant, j.ResourceID)
		if err != nil {
			return nil, err
		}
		if status != "VERIFIED" {
			return nil, errors.New("recovery requires a VERIFIED point; run verification first")
		}
		m, err := a.Engine.Load(ctx, j.Tenant, j.ResourceID)
		if err != nil {
			return nil, err
		}
		f, err := os.CreateTemp("", "draas-recovery-*.img")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		stats, err := a.Engine.Restore(ctx, m, j.Tenant, j.ResourceID, f, progress)
		if err != nil {
			_ = a.DB.SetPointStatus(ctx, j.Tenant, j.ResourceID, "INVALID")
			return nil, err
		}
		if _, err = f.Seek(0, 0); err != nil {
			return nil, err
		}
		provider := recovery.Provider{}
		v, err := provider.Validate(ctx, f)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		if err = os.Remove(f.Name()); err != nil {
			return nil, fmt.Errorf("recovery cleanup: %w", err)
		}
		w, err := a.DB.Workload(ctx, j.Tenant, m.Workload)
		if err != nil {
			return nil, err
		}
		report := map[string]any{"job_id": j.ID, "tenant_id": j.Tenant, "workload_id": m.Workload, "recovery_point": m.ID, "started_at": started, "finished_at": time.Now().UTC(), "duration_seconds": time.Since(started).Seconds(), "rto_contracted_seconds": w.RTO, "rto_scope": "simulator-only", "result": "SIMULATION_PASS", "validation": v, "restore": stats, "stream_hash": m.StreamHash, "merkle_root": m.MerkleRoot, "production_ready": false}
		sig, err := a.Engine.Sign(report)
		if err != nil {
			return nil, err
		}
		signed := map[string]any{"report": report, "signature": sig, "algorithm": "Ed25519", "encoding": "json-map-v1"}
		b, _ := json.Marshal(signed)
		key := "reports/" + j.Tenant + "/" + j.ID + ".json"
		created, err := a.Store.PutIfAbsent(ctx, key, b)
		if err != nil {
			return nil, err
		}
		if !created {
			b, err = a.Store.Get(ctx, key, 1<<20)
			if err != nil {
				return nil, err
			}
			var prior map[string]any
			if err = json.Unmarshal(b, &prior); err != nil {
				return nil, err
			}
			var envelope struct {
				Report    any    `json:"report"`
				Signature []byte `json:"signature"`
			}
			if err = json.Unmarshal(b, &envelope); err != nil || !backup.VerifySigned(envelope.Report, envelope.Signature, a.Engine.Trusted) {
				return nil, errors.New("stored recovery report signature invalid")
			}
			return prior, nil
		}
		return signed, nil
	case "vm-power":
		var req struct {
			ActivationSessionID string `json:"activation_session_id"`
			VMID                string `json:"vm_id"`
			Action              string `json:"action"`
		}
		if err := json.Unmarshal(j.Payload, &req); err != nil {
			return nil, err
		}
		session, err := a.DB.VMInActiveSession(ctx, j.Tenant, req.ActivationSessionID, req.VMID)
		if err != nil {
			return nil, err
		}
		if session.Provider != "LIBVIRT_LAB" {
			return nil, fmt.Errorf("unsupported power provider %q", session.Provider)
		}
		if os.Getenv("LIBVIRT_LAB_ROOT") == "" {
			return nil, errors.New("LIBVIRT_LAB_ROOT is not configured for this worker; VM power control must run on the lab hypervisor worker")
		}
		report, err := a.DB.RecoveryJobReport(ctx, j.Tenant, session.RecoveryJobID)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			RecoveredVM libvirtlab.VM `json:"recovered_vm"`
		}
		if err = json.Unmarshal(report, &envelope); err != nil {
			return nil, err
		}
		vm := envelope.RecoveredVM
		if vm.ID != req.VMID {
			return nil, errors.New("activation session VM is not present in the signed recovery report")
		}
		provider, err := libvirtlab.New(os.Getenv("LIBVIRT_LAB_ROOT"), os.Getenv("LIBVIRT_FIXTURE_BINARY"))
		if err != nil {
			return nil, err
		}
		before, _ := provider.State(ctx, vm)
		result := map[string]any{"activation_session_id": session.ID, "vm_id": vm.ID, "action": req.Action, "state_before": before, "billing_mode": session.BillingMode, "billable": session.Billable}
		var guest any
		switch req.Action {
		case "state":
		case "start":
			if err = provider.Start(ctx, vm); err == nil {
				guestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				guest, err = provider.WaitGuest(guestCtx, vm)
				cancel()
			}
		case "shutdown":
			stopCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err = provider.Stop(stopCtx, vm)
			cancel()
		case "force-stop":
			err = provider.ForceStop(ctx, vm)
		case "reboot":
			if err = provider.Reboot(ctx, vm); err == nil {
				guestCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				guest, err = provider.WaitGuest(guestCtx, vm)
				cancel()
			}
		default:
			err = fmt.Errorf("unsupported power action %q", req.Action)
		}
		after, stateErr := provider.State(ctx, vm)
		if stateErr == nil {
			result["state_after"] = after
		}
		if guest != nil {
			result["guest"] = guest
		}
		if err != nil {
			result["error"] = err.Error()
			_, _ = a.DB.RecordPowerAction(ctx, j.Tenant, "worker", session.ID, vm.ID, req.Action, session.Provider, "FAILED", j.ID, result, err.Error())
			return result, err
		}
		action, err := a.DB.RecordPowerAction(ctx, j.Tenant, "worker", session.ID, vm.ID, req.Action, session.Provider, "COMPLETED", j.ID, result, "")
		if err != nil {
			return nil, err
		}
		result["power_action_id"] = action.ID
		return result, nil
	case "real-recovery-test":
		var req struct {
			PointID       string `json:"point_id"`
			ProviderType  string `json:"provider_type"`
			KeepResources bool   `json:"keep_resources"`
		}
		if err := json.Unmarshal(j.Payload, &req); err != nil {
			return nil, err
		}
		if req.ProviderType != "LIBVIRT_LAB" {
			return nil, fmt.Errorf("unsupported real recovery provider %q", req.ProviderType)
		}
		if os.Getenv("LIBVIRT_LAB_ROOT") == "" {
			return nil, errors.New("LIBVIRT_LAB_ROOT is not configured for this worker; real recovery must run on the lab hypervisor worker")
		}
		runID := fmt.Sprintf("p3a-%s-attempt-%d", j.ID, j.Attempts)
		_ = a.DB.UpdateRecoveryJob(ctx, j.Tenant, j.ID, "RUNNING", "PREPARING", "", "", nil, nil)
		signed, err := recoverylab.Run(ctx, recoverylab.Options{
			Tenant: j.Tenant, RunID: runID, Root: os.Getenv("LIBVIRT_LAB_ROOT"),
			FixtureBinary: os.Getenv("LIBVIRT_FIXTURE_BINARY"), Engine: a.Engine,
			KeepResources: req.KeepResources,
		}, func(stage string, n int64) {
			progress(n)
			if e := a.DB.UpdateRecoveryJob(ctx, j.Tenant, j.ID, "RUNNING", stage, "", "", nil, nil); e != nil {
				slog.Error("persist recovery stage", "correlation_id", j.ID, "error", e)
			}
		})
		if err != nil {
			_ = a.DB.UpdateRecoveryJob(ctx, j.Tenant, j.ID, "FAILED", "FAILED", "", err.Error(), nil, nil)
			return nil, err
		}
		b, err := json.Marshal(signed)
		if err != nil {
			return nil, err
		}
		key := "reports/" + j.Tenant + "/" + j.ID + "-real-recovery.json"
		if _, err = a.Store.PutIfAbsent(ctx, key, b); err != nil {
			return nil, err
		}
		status := "TEST_PASSED"
		resources := signed.Report["provider_resource_ids"]
		if req.KeepResources {
			status = "READY_FOR_ACTIVATION"
		}
		if err = a.DB.UpdateRecoveryJob(ctx, j.Tenant, j.ID, status, "PASS", key, "", signed.Report, resources); err != nil {
			return nil, err
		}
		return map[string]any{"recovery_job_status": status, "billing_mode": "TEST", "billable": false, "signed_report_key": key, "report": signed.Report}, nil
	default:
		return nil, fmt.Errorf("unsupported job kind %q", j.Kind)
	}
}
