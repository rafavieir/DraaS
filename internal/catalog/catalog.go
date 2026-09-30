package catalog

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/pkg/contracts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/propagation"
	"time"
)

//go:embed migrations/001_init.sql
var migration string
var ErrConflict = errors.New("idempotency key already used for a different request")

type DB struct{ Pool *pgxpool.Pool }

func ID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%013d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &DB{p}, nil
}
func (d *DB) Migrate(ctx context.Context) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724319)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, migration); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (d *DB) SeedTenant(ctx context.Context, id string) error {
	_, err := d.Pool.Exec(ctx, "INSERT INTO tenants(id,partner_id,name) VALUES($1,'lab-partner',$1) ON CONFLICT DO NOTHING", id)
	return err
}

type Workload struct {
	contracts.Workload
	Active bool `json:"active"`
	RPO    int  `json:"rpo_seconds"`
	RTO    int  `json:"rto_seconds"`
}

func (d *DB) CreateWorkload(ctx context.Context, tenant, actor, name string, size int64) (Workload, error) {
	w := Workload{Workload: contracts.Workload{ID: ID(), Name: name, Source: "simulator", Revision: 1, Size: size}, Active: true, RPO: 3600, RTO: 120}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO workloads(tenant_id,id,name,source,size) VALUES($1,$2,$3,'simulator',$4)", tenant, w.ID, name, size)
	if err != nil {
		return w, err
	}
	if err = audit(ctx, tx, tenant, actor, "workload.created", w.ID, w.ID, nil); err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}
func (d *DB) Workload(ctx context.Context, tenant, id string) (Workload, error) {
	var w Workload
	err := d.Pool.QueryRow(ctx, "SELECT id,name,source,revision,size,active,rpo_seconds,rto_seconds FROM workloads WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&w.ID, &w.Name, &w.Source, &w.Revision, &w.Size, &w.Active, &w.RPO, &w.RTO)
	return w, err
}
func (d *DB) ChangeWorkload(ctx context.Context, tenant, actor, id, action string) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := "UPDATE workloads SET revision=revision+1 WHERE tenant_id=$1 AND id=$2 AND active"
	if action == "delete-source" {
		q = "UPDATE workloads SET active=false WHERE tenant_id=$1 AND id=$2 AND active"
	}
	tag, err := tx.Exec(ctx, q, tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err = audit(ctx, tx, tenant, actor, "simulator."+action, id, id, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Job struct {
	ID         string          `json:"id"`
	Tenant     string          `json:"tenant_id"`
	Kind       string          `json:"kind"`
	ResourceID string          `json:"resource_id"`
	Status     string          `json:"status"`
	Payload    json.RawMessage `json:"payload"`
	Result     json.RawMessage `json:"result"`
	Error      string          `json:"error"`
	Progress   int64           `json:"progress_bytes"`
	Attempts   int             `json:"attempts"`
	Cancel     bool            `json:"cancel_requested"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type RecoveryJob struct {
	ID                string          `json:"id"`
	JobID             string          `json:"job_id"`
	Tenant            string          `json:"tenant_id"`
	RecoveryPointID   string          `json:"recovery_point_id"`
	ProviderType      string          `json:"provider_type"`
	Status            string          `json:"status"`
	Stage             string          `json:"stage"`
	BillingMode       string          `json:"billing_mode"`
	Billable          bool            `json:"billable"`
	Simulation        bool            `json:"simulation"`
	ProductionReady   bool            `json:"production_ready"`
	KeepResources     bool            `json:"keep_resources"`
	ProviderResources json.RawMessage `json:"provider_resource_ids"`
	Report            json.RawMessage `json:"report"`
	SignedReportKey   string          `json:"signed_report_key"`
	Error             string          `json:"error"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type ActivationSession struct {
	ID                      string          `json:"id"`
	Tenant                  string          `json:"tenant_id"`
	RecoveryJobID           string          `json:"recovery_job_id"`
	RecoveryPointID         string          `json:"recovery_point_id"`
	Provider                string          `json:"provider"`
	ProviderResources       json.RawMessage `json:"provider_resource_ids"`
	BillingMode             string          `json:"billing_mode"`
	Billable                bool            `json:"billable"`
	RatePlanID              string          `json:"rate_plan_id"`
	RatePlanVersion         int             `json:"rate_plan_version"`
	RequestedAt             time.Time       `json:"requested_at"`
	ActivatedAt             *time.Time      `json:"activated_at"`
	DeactivationRequestedAt *time.Time      `json:"deactivation_requested_at"`
	DeactivatedAt           *time.Time      `json:"deactivated_at"`
	CreatedBy               string          `json:"created_by"`
	EndedBy                 string          `json:"ended_by"`
	Reason                  string          `json:"reason"`
	DataMayHaveChanged      bool            `json:"data_may_have_changed"`
	Status                  string          `json:"status"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type PowerAction struct {
	ID                  string          `json:"id"`
	Tenant              string          `json:"tenant_id"`
	ActivationSessionID string          `json:"activation_session_id"`
	VMID                string          `json:"vm_id"`
	Action              string          `json:"action"`
	Provider            string          `json:"provider"`
	Status              string          `json:"status"`
	JobID               string          `json:"job_id"`
	RequestedBy         string          `json:"requested_by"`
	ProviderResponse    json.RawMessage `json:"provider_response"`
	Error               string          `json:"error"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

const (
	RecoveryStageRequested           = "REQUESTED"
	RecoveryStagePreflight           = "PREFLIGHT"
	RecoveryStageAdmission           = "ADMISSION"
	RecoveryStageNetworkPrepare      = "NETWORK_PREPARE"
	RecoveryStageResourceProvision   = "RESOURCE_PROVISION"
	RecoveryStageDiskMaterialize     = "DISK_MATERIALIZE"
	RecoveryStageDiskAttach          = "DISK_ATTACH"
	RecoveryStagePowerOn             = "POWER_ON"
	RecoveryStageWaitGuest           = "WAIT_GUEST"
	RecoveryStageValidateOS          = "VALIDATE_OS"
	RecoveryStageValidateApplication = "VALIDATE_APPLICATION"
	RecoveryStageReadyForActivation  = "READY_FOR_ACTIVATION"
	RecoveryStageFailed              = "FAILED"
	RecoveryStageCleanup             = "CLEANUP"
	RecoveryStageCompleted           = "COMPLETED"
)

const (
	OperationNotStarted = "NOT_STARTED"
	OperationUnknown    = "UNKNOWN"
	OperationConfirmed  = "CONFIRMED"
	OperationFailed     = "FAILED"
)

type RecoveryOperation struct {
	Tenant        string     `json:"tenant_id"`
	OperationID   string     `json:"operation_id"`
	RecoveryJobID string     `json:"recovery_job_id"`
	Stage         string     `json:"stage"`
	OperationType string     `json:"operation_type"`
	Provider      string     `json:"provider"`
	ResourceType  string     `json:"resource_type"`
	ResourceID    string     `json:"resource_id"`
	ProviderTask  string     `json:"provider_task_id"`
	DesiredState  string     `json:"desired_state"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempt_count"`
	StartedAt     *time.Time `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	LastError     string     `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type ProviderResource struct {
	ID                  string          `json:"id"`
	Tenant              string          `json:"tenant_id"`
	RecoveryJobID       string          `json:"recovery_job_id"`
	ActivationSessionID string          `json:"activation_session_id"`
	Provider            string          `json:"provider"`
	ResourceType        string          `json:"resource_type"`
	ProviderResourceID  string          `json:"provider_resource_id"`
	OperationID         string          `json:"operation_id"`
	Status              string          `json:"status"`
	Handle              json.RawMessage `json:"handle"`
	CleanupPolicy       string          `json:"cleanup_policy"`
	CreatedAt           time.Time       `json:"created_at"`
	LastSeenAt          time.Time       `json:"last_seen_at"`
}

type RecoveryLease struct {
	Tenant         string     `json:"tenant_id"`
	RecoveryJobID  string     `json:"recovery_job_id"`
	Stage          string     `json:"stage"`
	Status         string     `json:"status"`
	Owner          string     `json:"lease_owner"`
	ExpiresAt      *time.Time `json:"lease_expires_at"`
	Generation     int64      `json:"lease_generation"`
	Version        int64      `json:"version"`
	WorkerLastSeen *time.Time `json:"worker_last_seen"`
	DesiredState   string     `json:"desired_state"`
	NextRetryAt    *time.Time `json:"next_retry_at"`
}

func (d *DB) Enqueue(ctx context.Context, tenant, actor, kind, resource, key string, payload any) (Job, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Job{}, err
	}
	hash := backup.Sum(append([]byte(kind+"/"+resource+"/"), b...))
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback(ctx)
	id := ID()
	tag, err := tx.Exec(ctx, "INSERT INTO jobs(tenant_id,id,kind,resource_id,idempotency_key,request_hash,payload) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,idempotency_key) DO NOTHING", tenant, id, kind, resource, key, hash, b)
	if err != nil {
		return Job{}, err
	}
	if tag.RowsAffected() == 0 {
		var existingHash string
		err = tx.QueryRow(ctx, "SELECT id,request_hash FROM jobs WHERE tenant_id=$1 AND idempotency_key=$2", tenant, key).Scan(&id, &existingHash)
		if err != nil {
			return Job{}, err
		}
		if existingHash != hash {
			return Job{}, ErrConflict
		}
	} else {
		carrier := propagation.MapCarrier{}
		propagation.TraceContext{}.Inject(ctx, carrier)
		ev := contracts.Event{EventID: id, TenantID: tenant, CorrelationID: id, Timestamp: time.Now().UTC(), Version: 1, ResourceID: id, Kind: kind, TraceParent: carrier.Get("traceparent")}
		eb, _ := json.Marshal(ev)
		if _, err = tx.Exec(ctx, "INSERT INTO outbox(id,tenant_id,job_id,event) VALUES($1,$2,$1,$3)", id, tenant, eb); err != nil {
			return Job{}, err
		}
		if err = audit(ctx, tx, tenant, actor, kind+".requested", resource, id, nil); err != nil {
			return Job{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Job{}, err
	}
	return d.Job(ctx, tenant, id)
}

func (d *DB) CreateRecoveryJob(ctx context.Context, tenant, actor, pointID, providerType, key string, keepResources bool) (RecoveryJob, Job, error) {
	payload := map[string]any{"point_id": pointID, "provider_type": providerType, "keep_resources": keepResources}
	j, err := d.Enqueue(ctx, tenant, actor, "real-recovery-test", pointID, key, payload)
	if err != nil {
		return RecoveryJob{}, j, err
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return RecoveryJob{}, j, err
	}
	defer tx.Rollback(ctx)
	id := ID()
	desired := RecoveryStageCompleted
	if keepResources {
		desired = RecoveryStageReadyForActivation
	}
	_, err = tx.Exec(ctx, `INSERT INTO recovery_jobs(tenant_id,id,job_id,recovery_point_id,provider_type,status,stage,keep_resources,provider_version,desired_state)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,'recovery-provider/v2',$9) ON CONFLICT(tenant_id,job_id) DO NOTHING`, tenant, id, j.ID, pointID, providerType, j.Status, RecoveryStageRequested, keepResources, desired)
	if err != nil {
		return RecoveryJob{}, j, err
	}
	if err = audit(ctx, tx, tenant, actor, "recovery.test.requested", pointID, j.ID, map[string]any{"provider_type": providerType, "billing_mode": "TEST", "keep_resources": keepResources}); err != nil {
		return RecoveryJob{}, j, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RecoveryJob{}, j, err
	}
	rj, err := d.RecoveryJobByJob(ctx, tenant, j.ID)
	return rj, j, err
}

func (d *DB) RecoveryJobByJob(ctx context.Context, tenant, jobID string) (RecoveryJob, error) {
	var r RecoveryJob
	err := d.Pool.QueryRow(ctx, `SELECT id,job_id,tenant_id,recovery_point_id,provider_type,status,stage,billing_mode,billable,simulation,production_ready,keep_resources,provider_resource_ids,report,signed_report_key,error,created_at,updated_at
FROM recovery_jobs WHERE tenant_id=$1 AND job_id=$2`, tenant, jobID).Scan(&r.ID, &r.JobID, &r.Tenant, &r.RecoveryPointID, &r.ProviderType, &r.Status, &r.Stage, &r.BillingMode, &r.Billable, &r.Simulation, &r.ProductionReady, &r.KeepResources, &r.ProviderResources, &r.Report, &r.SignedReportKey, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (d *DB) UpdateRecoveryJob(ctx context.Context, tenant, jobID, status, stage, signedKey, jobErr string, report any, resources any) error {
	var b []byte
	var err error
	if report != nil {
		b, err = json.Marshal(report)
		if err != nil {
			return err
		}
	}
	var rb []byte
	if resources != nil {
		rb, err = json.Marshal(resources)
		if err != nil {
			return err
		}
	}
	_, err = d.Pool.Exec(ctx, `UPDATE recovery_jobs SET status=$3,stage=$4,report=COALESCE($5,report),signed_report_key=COALESCE(NULLIF($6,''),signed_report_key),error=$7,provider_resource_ids=COALESCE($8,provider_resource_ids),updated_at=now()
WHERE tenant_id=$1 AND job_id=$2`, tenant, jobID, status, stage, b, signedKey, jobErr, rb)
	return err
}

func (d *DB) ActivateRecoveryJob(ctx context.Context, tenant, actor, jobID, reason string) (ActivationSession, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return ActivationSession{}, err
	}
	defer tx.Rollback(ctx)
	var r RecoveryJob
	err = tx.QueryRow(ctx, `SELECT id,job_id,tenant_id,recovery_point_id,provider_type,status,stage,billing_mode,billable,simulation,production_ready,keep_resources,provider_resource_ids,report,signed_report_key,error,created_at,updated_at
FROM recovery_jobs WHERE tenant_id=$1 AND job_id=$2 FOR UPDATE`, tenant, jobID).Scan(&r.ID, &r.JobID, &r.Tenant, &r.RecoveryPointID, &r.ProviderType, &r.Status, &r.Stage, &r.BillingMode, &r.Billable, &r.Simulation, &r.ProductionReady, &r.KeepResources, &r.ProviderResources, &r.Report, &r.SignedReportKey, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return ActivationSession{}, err
	}
	if r.Status != "READY_FOR_ACTIVATION" || !r.KeepResources || len(r.ProviderResources) < 3 || string(r.ProviderResources) == "[]" {
		return ActivationSession{}, pgx.ErrNoRows
	}
	var existing string
	err = tx.QueryRow(ctx, "SELECT id FROM activation_sessions WHERE tenant_id=$1 AND recovery_job_id=$2 AND status IN ('ACTIVATING','ACTIVE','DEACTIVATING') LIMIT 1", tenant, jobID).Scan(&existing)
	if err == nil {
		return d.activationSessionTx(ctx, tx, tenant, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ActivationSession{}, err
	}
	id := ID()
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO activation_sessions(tenant_id,id,recovery_job_id,recovery_point_id,provider,provider_resource_ids,billing_mode,billable,rate_plan_id,rate_plan_version,requested_at,activated_at,created_by,reason,data_may_have_changed,status)
VALUES($1,$2,$3,$4,$5,$6,'DISASTER',true,'lab-default-vm-hour',1,$7,$7,$8,$9,true,'ACTIVE')`, tenant, id, jobID, r.RecoveryPointID, r.ProviderType, r.ProviderResources, now, actor, reason)
	if err != nil {
		return ActivationSession{}, err
	}
	if err = audit(ctx, tx, tenant, actor, "dr.activated", id, jobID, map[string]any{"recovery_job_id": jobID, "billing_mode": "DISASTER", "billable": true, "rate_plan_id": "lab-default-vm-hour", "rate_plan_version": 1}); err != nil {
		return ActivationSession{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ActivationSession{}, err
	}
	return d.ActivationSession(ctx, tenant, id)
}

func (d *DB) activationSessionTx(ctx context.Context, tx pgx.Tx, tenant, id string) (ActivationSession, error) {
	var s ActivationSession
	err := tx.QueryRow(ctx, `SELECT id,tenant_id,recovery_job_id,recovery_point_id,provider,provider_resource_ids,billing_mode,billable,rate_plan_id,rate_plan_version,requested_at,activated_at,deactivation_requested_at,deactivated_at,created_by,ended_by,reason,data_may_have_changed,status,created_at,updated_at
FROM activation_sessions WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&s.ID, &s.Tenant, &s.RecoveryJobID, &s.RecoveryPointID, &s.Provider, &s.ProviderResources, &s.BillingMode, &s.Billable, &s.RatePlanID, &s.RatePlanVersion, &s.RequestedAt, &s.ActivatedAt, &s.DeactivationRequestedAt, &s.DeactivatedAt, &s.CreatedBy, &s.EndedBy, &s.Reason, &s.DataMayHaveChanged, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (d *DB) ActivationSession(ctx context.Context, tenant, id string) (ActivationSession, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return ActivationSession{}, err
	}
	defer tx.Rollback(ctx)
	return d.activationSessionTx(ctx, tx, tenant, id)
}
func (d *DB) VMInActiveSession(ctx context.Context, tenant, sessionID, vmID string) (ActivationSession, error) {
	s, err := d.ActivationSession(ctx, tenant, sessionID)
	if err != nil {
		return ActivationSession{}, err
	}
	if s.Status != "ACTIVE" {
		return ActivationSession{}, pgx.ErrNoRows
	}
	var ids []string
	if err = json.Unmarshal(s.ProviderResources, &ids); err != nil {
		return ActivationSession{}, err
	}
	for _, id := range ids {
		if id == vmID {
			return s, nil
		}
	}
	return ActivationSession{}, pgx.ErrNoRows
}

func (d *DB) RecordPowerAction(ctx context.Context, tenant, actor, sessionID, vmID, action, provider, status, jobID string, response any, actionErr string) (PowerAction, error) {
	b, err := json.Marshal(response)
	if err != nil {
		return PowerAction{}, err
	}
	if b == nil {
		b = []byte("{}")
	}
	id := ID()
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return PowerAction{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO power_actions(tenant_id,id,activation_session_id,vm_id,action,provider,status,job_id,requested_by,provider_response,error)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, tenant, id, sessionID, vmID, action, provider, status, jobID, actor, b, actionErr)
	if err != nil {
		return PowerAction{}, err
	}
	if err = audit(ctx, tx, tenant, actor, "dr.power."+action+"."+status, vmID, jobID, map[string]any{"activation_session_id": sessionID, "provider": provider, "error": actionErr}); err != nil {
		return PowerAction{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PowerAction{}, err
	}
	var p PowerAction
	err = d.Pool.QueryRow(ctx, `SELECT id,tenant_id,activation_session_id,vm_id,action,provider,status,job_id,requested_by,provider_response,error,created_at,updated_at
FROM power_actions WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&p.ID, &p.Tenant, &p.ActivationSessionID, &p.VMID, &p.Action, &p.Provider, &p.Status, &p.JobID, &p.RequestedBy, &p.ProviderResponse, &p.Error, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (d *DB) RecoveryJobReport(ctx context.Context, tenant, jobID string) (json.RawMessage, error) {
	var b json.RawMessage
	err := d.Pool.QueryRow(ctx, "SELECT report FROM recovery_jobs WHERE tenant_id=$1 AND job_id=$2", tenant, jobID).Scan(&b)
	return b, err
}

func (d *DB) EnsureRecoveryOperation(ctx context.Context, tenant, recoveryJobID, stage, operationType, provider, resourceType, resourceKey, desired string) (RecoveryOperation, error) {
	opID := RecoveryOperationID(recoveryJobID, stage, operationType, resourceKey)
	_, err := d.Pool.Exec(ctx, `INSERT INTO recovery_operations(tenant_id,operation_id,recovery_job_id,stage,operation_type,provider,resource_type,desired_state,status)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(tenant_id,operation_id) DO NOTHING`, tenant, opID, recoveryJobID, stage, operationType, provider, resourceType, desired, OperationNotStarted)
	if err != nil {
		return RecoveryOperation{}, err
	}
	return d.RecoveryOperation(ctx, tenant, opID)
}

func (d *DB) RecoveryOperation(ctx context.Context, tenant, operationID string) (RecoveryOperation, error) {
	var op RecoveryOperation
	err := d.Pool.QueryRow(ctx, `SELECT tenant_id,operation_id,recovery_job_id,stage,operation_type,provider,resource_type,resource_id,provider_task_id,desired_state,status,attempt_count,started_at,completed_at,last_error,created_at,updated_at
FROM recovery_operations WHERE tenant_id=$1 AND operation_id=$2`, tenant, operationID).Scan(&op.Tenant, &op.OperationID, &op.RecoveryJobID, &op.Stage, &op.OperationType, &op.Provider, &op.ResourceType, &op.ResourceID, &op.ProviderTask, &op.DesiredState, &op.Status, &op.Attempts, &op.StartedAt, &op.CompletedAt, &op.LastError, &op.CreatedAt, &op.UpdatedAt)
	return op, err
}

func (d *DB) MarkRecoveryOperationStarted(ctx context.Context, tenant, operationID string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE recovery_operations SET status=$3,attempt_count=attempt_count+1,started_at=COALESCE(started_at,now()),updated_at=now()
WHERE tenant_id=$1 AND operation_id=$2 AND status IN ($4,$5)`, tenant, operationID, OperationUnknown, OperationNotStarted, OperationUnknown)
	return err
}

func (d *DB) ConfirmRecoveryOperation(ctx context.Context, tenant, operationID, resourceID, providerTaskID string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE recovery_operations SET status=$3,resource_id=COALESCE(NULLIF($4,''),resource_id),provider_task_id=COALESCE(NULLIF($5,''),provider_task_id),completed_at=now(),updated_at=now(),last_error=''
WHERE tenant_id=$1 AND operation_id=$2`, tenant, operationID, OperationConfirmed, resourceID, providerTaskID)
	return err
}

func (d *DB) FailRecoveryOperation(ctx context.Context, tenant, operationID, status, lastErr string) error {
	if status == "" {
		status = OperationFailed
	}
	_, err := d.Pool.Exec(ctx, `UPDATE recovery_operations SET status=$3,last_error=$4,updated_at=now()
WHERE tenant_id=$1 AND operation_id=$2`, tenant, operationID, status, lastErr)
	return err
}

func (d *DB) UpsertProviderResource(ctx context.Context, tenant, recoveryJobID, activationSessionID, provider, resourceType, providerResourceID, operationID, status, cleanupPolicy string, handle any) (ProviderResource, error) {
	b, err := json.Marshal(handle)
	if err != nil {
		return ProviderResource{}, err
	}
	if cleanupPolicy == "" {
		cleanupPolicy = "TEST_EPHEMERAL"
	}
	id := ID()
	_, err = d.Pool.Exec(ctx, `INSERT INTO provider_resources(tenant_id,id,recovery_job_id,activation_session_id,provider,resource_type,provider_resource_id,operation_id,status,handle,cleanup_policy)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT(tenant_id,provider,provider_resource_id) DO UPDATE SET
 recovery_job_id=COALESCE(NULLIF(EXCLUDED.recovery_job_id,''),provider_resources.recovery_job_id),
 activation_session_id=COALESCE(NULLIF(EXCLUDED.activation_session_id,''),provider_resources.activation_session_id),
 operation_id=COALESCE(NULLIF(EXCLUDED.operation_id,''),provider_resources.operation_id),
 status=EXCLUDED.status, handle=EXCLUDED.handle, cleanup_policy=EXCLUDED.cleanup_policy, last_seen_at=now()`, tenant, id, recoveryJobID, activationSessionID, provider, resourceType, providerResourceID, operationID, status, b, cleanupPolicy)
	if err != nil {
		return ProviderResource{}, err
	}
	var r ProviderResource
	err = d.Pool.QueryRow(ctx, `SELECT id,tenant_id,recovery_job_id,activation_session_id,provider,resource_type,provider_resource_id,operation_id,status,handle,cleanup_policy,created_at,last_seen_at
FROM provider_resources WHERE tenant_id=$1 AND provider=$2 AND provider_resource_id=$3`, tenant, provider, providerResourceID).Scan(&r.ID, &r.Tenant, &r.RecoveryJobID, &r.ActivationSessionID, &r.Provider, &r.ResourceType, &r.ProviderResourceID, &r.OperationID, &r.Status, &r.Handle, &r.CleanupPolicy, &r.CreatedAt, &r.LastSeenAt)
	return r, err
}

func (d *DB) ProviderResourceByOperation(ctx context.Context, tenant, operationID string) (ProviderResource, error) {
	var r ProviderResource
	err := d.Pool.QueryRow(ctx, `SELECT id,tenant_id,recovery_job_id,activation_session_id,provider,resource_type,provider_resource_id,operation_id,status,handle,cleanup_policy,created_at,last_seen_at
FROM provider_resources WHERE tenant_id=$1 AND operation_id=$2 ORDER BY last_seen_at DESC LIMIT 1`, tenant, operationID).Scan(&r.ID, &r.Tenant, &r.RecoveryJobID, &r.ActivationSessionID, &r.Provider, &r.ResourceType, &r.ProviderResourceID, &r.OperationID, &r.Status, &r.Handle, &r.CleanupPolicy, &r.CreatedAt, &r.LastSeenAt)
	return r, err
}

func (d *DB) AppendRecoveryTimeline(ctx context.Context, tenant, recoveryJobID, eventType, stage, operationID string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := d.Pool.Exec(ctx, `INSERT INTO recovery_timeline(tenant_id,id,recovery_job_id,event_type,stage,operation_id,detail)
VALUES($1,$2,$3,$4,$5,$6,$7)`, tenant, ID(), recoveryJobID, eventType, stage, operationID, b)
	return err
}

func (d *DB) RunnableRecoveryJobs(ctx context.Context, limit int) ([]RecoveryLease, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := d.Pool.Query(ctx, `SELECT tenant_id,job_id,stage,status,lease_owner,lease_expires_at,lease_generation,version,worker_last_seen,desired_state,next_retry_at
FROM recovery_jobs
WHERE status NOT IN ('COMPLETED','FAILED_FINAL','CANCELLED_FINAL')
 AND (next_retry_at IS NULL OR next_retry_at <= now())
 AND (lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at <= now())
ORDER BY updated_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecoveryLease
	for rows.Next() {
		var l RecoveryLease
		if err = rows.Scan(&l.Tenant, &l.RecoveryJobID, &l.Stage, &l.Status, &l.Owner, &l.ExpiresAt, &l.Generation, &l.Version, &l.WorkerLastSeen, &l.DesiredState, &l.NextRetryAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (d *DB) AcquireRecoveryLease(ctx context.Context, tenant, recoveryJobID, owner string, ttl time.Duration) (RecoveryLease, bool, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	var l RecoveryLease
	err := d.Pool.QueryRow(ctx, `UPDATE recovery_jobs
SET lease_owner=$3, lease_expires_at=now()+($4::text)::interval, lease_generation=lease_generation+1, worker_last_seen=now(), version=version+1, updated_at=now()
WHERE tenant_id=$1 AND job_id=$2
 AND status NOT IN ('COMPLETED','FAILED_FINAL','CANCELLED_FINAL')
 AND (lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at <= now() OR lease_owner=$3)
RETURNING tenant_id,job_id,stage,status,lease_owner,lease_expires_at,lease_generation,version,worker_last_seen,desired_state,next_retry_at`, tenant, recoveryJobID, owner, ttl.String()).Scan(&l.Tenant, &l.RecoveryJobID, &l.Stage, &l.Status, &l.Owner, &l.ExpiresAt, &l.Generation, &l.Version, &l.WorkerLastSeen, &l.DesiredState, &l.NextRetryAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveryLease{}, false, nil
	}
	if err != nil {
		return RecoveryLease{}, false, err
	}
	_ = d.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "LEASE_ACQUIRED", l.Stage, "", map[string]any{"owner": owner, "generation": l.Generation})
	return l, true, nil
}

func (d *DB) RenewRecoveryLease(ctx context.Context, tenant, recoveryJobID, owner string, generation int64, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	tag, err := d.Pool.Exec(ctx, `UPDATE recovery_jobs
SET lease_expires_at=now()+($5::text)::interval, worker_last_seen=now(), updated_at=now()
WHERE tenant_id=$1 AND job_id=$2 AND lease_owner=$3 AND lease_generation=$4`, tenant, recoveryJobID, owner, generation, ttl.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (d *DB) ReleaseRecoveryLease(ctx context.Context, tenant, recoveryJobID, owner string, generation int64) error {
	tag, err := d.Pool.Exec(ctx, `UPDATE recovery_jobs
SET lease_owner='', lease_expires_at=NULL, updated_at=now()
WHERE tenant_id=$1 AND job_id=$2 AND lease_owner=$3 AND lease_generation=$4`, tenant, recoveryJobID, owner, generation)
	if err == nil && tag.RowsAffected() == 1 {
		_ = d.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "LEASE_RELEASED", "", "", map[string]any{"owner": owner, "generation": generation})
	}
	return err
}

func (d *DB) AdvanceRecoveryStage(ctx context.Context, tenant, recoveryJobID, owner string, generation int64, expectedVersion int64, stage, status string) (bool, error) {
	tag, err := d.Pool.Exec(ctx, `UPDATE recovery_jobs
SET stage=$6, status=$7, version=version+1, updated_at=now()
WHERE tenant_id=$1 AND job_id=$2 AND lease_owner=$3 AND lease_generation=$4 AND version=$5`, tenant, recoveryJobID, owner, generation, expectedVersion, stage, status)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		_ = d.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "STAGE_COMPLETED", stage, "", map[string]any{"status": status, "generation": generation})
		return true, nil
	}
	return false, nil
}

func (d *DB) Job(ctx context.Context, tenant, id string) (Job, error) {
	var j Job
	err := d.Pool.QueryRow(ctx, "SELECT id,tenant_id,kind,resource_id,status,payload,result,error,progress_bytes,attempts,cancel_requested,created_at,updated_at FROM jobs WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&j.ID, &j.Tenant, &j.Kind, &j.ResourceID, &j.Status, &j.Payload, &j.Result, &j.Error, &j.Progress, &j.Attempts, &j.Cancel, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}
func audit(ctx context.Context, tx pgx.Tx, tenant, actor, action, resource, correlation string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, "INSERT INTO audit_log(tenant_id,actor,action,resource_id,correlation_id,detail) VALUES($1,$2,$3,$4,$5,$6)", tenant, actor, action, resource, correlation, b)
	return err
}
func (d *DB) Cancel(ctx context.Context, tenant, actor, id string) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE jobs SET cancel_requested=true,status='CANCELLED',updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status IN ('QUEUED','RUNNING','RETRYING')", tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if err = audit(ctx, tx, tenant, actor, "job.cancelled", id, id, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (d *DB) Finish(ctx context.Context, j Job, status string, result any, jobErr string) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE jobs SET status=$3,result=$4,error=$5,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND NOT cancel_requested", j.Tenant, j.ID, status, b, jobErr)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err = audit(ctx, tx, j.Tenant, "worker", j.Kind+"."+status, j.ResourceID, j.ID, map[string]string{"error": jobErr}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (d *DB) SavePoint(ctx context.Context, m backup.Manifest, s backup.Stats) error {
	b, _ := json.Marshal(m)
	stats, _ := json.Marshal(s)
	_, err := d.Pool.Exec(ctx, "INSERT INTO recovery_points(tenant_id,id,workload_id,size,manifest,stats,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,id) DO NOTHING", m.Tenant, m.ID, m.Workload, m.Size, b, stats, m.CreatedAt)
	return err
}
func (d *DB) SetPointStatus(ctx context.Context, tenant, id, status string) error {
	tag, err := d.Pool.Exec(ctx, "UPDATE recovery_points SET status=$3,verified_at=CASE WHEN $3='VERIFIED' THEN now() ELSE NULL END WHERE tenant_id=$1 AND id=$2", tenant, id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (d *DB) PointStatus(ctx context.Context, tenant, id string) (string, error) {
	var s string
	err := d.Pool.QueryRow(ctx, "SELECT status FROM recovery_points WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&s)
	return s, err
}

// List uses a bounded cursor over indexed identifiers. No unbounded API catalog scans.
func (d *DB) List(ctx context.Context, tenant, kind, cursor string, limit int) ([]json.RawMessage, error) {
	if limit < 1 || limit > 101 {
		limit = 25
	}
	var q string
	switch kind {
	case "workloads":
		q = "SELECT to_jsonb(w) - 'tenant_id' FROM workloads w WHERE tenant_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3"
	case "recovery-points":
		q = "SELECT to_jsonb(p) - 'manifest' - 'tenant_id' FROM recovery_points p WHERE tenant_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3"
	case "jobs", "tests":
		q = "SELECT to_jsonb(j) - 'payload' - 'tenant_id' FROM jobs j WHERE tenant_id=$1 AND id<$2"
		if kind == "tests" {
			q += " AND kind IN ('test','restore')"
		}
		q += " ORDER BY id DESC LIMIT $3"
	case "recovery-jobs":
		q = "SELECT to_jsonb(r) - 'tenant_id' FROM recovery_jobs r WHERE tenant_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3"
	case "activation-sessions":
		q = "SELECT to_jsonb(s) - 'tenant_id' FROM activation_sessions s WHERE tenant_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3"
	case "power-actions":
		q = "SELECT to_jsonb(p) - 'tenant_id' FROM power_actions p WHERE tenant_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3"
	case "audit":
		q = "SELECT to_jsonb(a) FROM audit_log a WHERE tenant_id=$1 AND sequence < $2::bigint ORDER BY sequence DESC LIMIT $3"
		if cursor == "" {
			cursor = "9223372036854775807"
		}
	default:
		return nil, errors.New("unknown collection")
	}
	if cursor == "" {
		cursor = "~"
	}
	rows, err := d.Pool.Query(ctx, q, tenant, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var b json.RawMessage
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}
func (d *DB) Overview(ctx context.Context, tenant string) (map[string]any, error) {
	var workloads, points, verified, failed, rpo int
	var bytes int64
	err := d.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM workloads WHERE tenant_id=$1),
 (SELECT count(*) FROM recovery_points WHERE tenant_id=$1),
 (SELECT count(*) FROM recovery_points WHERE tenant_id=$1 AND status='VERIFIED'),
 (SELECT count(*) FROM jobs WHERE tenant_id=$1 AND status='FAILED'),
 (SELECT COALESCE(sum(size),0) FROM recovery_points WHERE tenant_id=$1),
 (SELECT count(*) FROM workloads w WHERE tenant_id=$1 AND EXISTS(SELECT 1 FROM recovery_points p WHERE p.tenant_id=w.tenant_id AND p.workload_id=w.id AND p.status='VERIFIED' AND p.created_at>now()-make_interval(secs=>w.rpo_seconds)))`, tenant).Scan(&workloads, &points, &verified, &failed, &bytes, &rpo)
	health := "UNPROTECTED"
	if workloads > 0 && rpo == workloads {
		health = "SIMULATION_VERIFIED"
	}
	if failed > 0 {
		health = "WARNING"
	}
	return map[string]any{"health": health, "workloads": workloads, "recovery_points": points, "verified_points": verified, "failed_jobs": failed, "logical_bytes": bytes, "rpo_compliant": rpo, "production_ready": false, "boot_verified": false, "immutability": "NOT_CONFIGURED", "capacity": "NOT_MEASURED", "rto_validated": nil, "simulation": true}, err
}
