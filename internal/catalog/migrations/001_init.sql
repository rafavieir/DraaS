CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS tenants(id text PRIMARY KEY, partner_id text NOT NULL, name text NOT NULL);
CREATE TABLE IF NOT EXISTS workloads(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL, name text NOT NULL,
 source text NOT NULL CHECK(source='simulator'), revision integer NOT NULL DEFAULT 1,
 size bigint NOT NULL CHECK(size BETWEEN 1048576 AND 268435456), active boolean NOT NULL DEFAULT true,
 rpo_seconds integer NOT NULL DEFAULT 3600, rto_seconds integer NOT NULL DEFAULT 120,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS jobs(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL, kind text NOT NULL,
 resource_id text NOT NULL, status text NOT NULL DEFAULT 'QUEUED',
 idempotency_key text NOT NULL, request_hash text NOT NULL, payload jsonb NOT NULL,
 result jsonb, error text NOT NULL DEFAULT '', progress_bytes bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0, cancel_requested boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,idempotency_key)
);
CREATE TABLE IF NOT EXISTS outbox(
 id text PRIMARY KEY, tenant_id text NOT NULL, job_id text NOT NULL, event jsonb NOT NULL,
 published_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,job_id) REFERENCES jobs(tenant_id,id)
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(created_at) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS jobs_tenant_status ON jobs(tenant_id,status,created_at DESC,id);
CREATE INDEX IF NOT EXISTS jobs_tenant_cursor ON jobs(tenant_id,id DESC);
CREATE TABLE IF NOT EXISTS recovery_points(
 tenant_id text NOT NULL, id text NOT NULL, workload_id text NOT NULL,
 status text NOT NULL DEFAULT 'STORED', size bigint NOT NULL, manifest jsonb NOT NULL, stats jsonb NOT NULL,
 created_at timestamptz NOT NULL, verified_at timestamptz,
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,workload_id) REFERENCES workloads(tenant_id,id)
);
CREATE INDEX IF NOT EXISTS points_workload_created ON recovery_points(tenant_id,workload_id,created_at DESC);
CREATE INDEX IF NOT EXISTS points_tenant_cursor ON recovery_points(tenant_id,id DESC);
CREATE INDEX IF NOT EXISTS points_valid ON recovery_points(tenant_id,workload_id,created_at DESC) WHERE status='VERIFIED';
CREATE TABLE IF NOT EXISTS recovery_jobs(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL,
 job_id text NOT NULL, recovery_point_id text NOT NULL, provider_type text NOT NULL,
 status text NOT NULL DEFAULT 'QUEUED', stage text NOT NULL DEFAULT 'QUEUED',
 billing_mode text NOT NULL DEFAULT 'TEST', billable boolean NOT NULL DEFAULT false,
 simulation boolean NOT NULL DEFAULT false, production_ready boolean NOT NULL DEFAULT false,
 keep_resources boolean NOT NULL DEFAULT false, provider_resource_ids jsonb NOT NULL DEFAULT '[]',
 report jsonb, signed_report_key text NOT NULL DEFAULT '', error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,job_id)
);
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS keep_resources boolean NOT NULL DEFAULT false;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS provider_resource_ids jsonb NOT NULL DEFAULT '[]';
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS provider_version text NOT NULL DEFAULT 'recovery-provider/v1';
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS desired_state text NOT NULL DEFAULT 'COMPLETED';
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS retryable boolean NOT NULL DEFAULT true;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS next_retry_at timestamptz;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS started_at timestamptz;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS completed_at timestamptz;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS worker_last_seen timestamptz;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS lease_owner text NOT NULL DEFAULT '';
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS lease_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE recovery_jobs ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS recovery_jobs_tenant_cursor ON recovery_jobs(tenant_id,id DESC);
CREATE INDEX IF NOT EXISTS recovery_jobs_unfinished ON recovery_jobs(tenant_id,next_retry_at,status,stage) WHERE status NOT IN ('COMPLETED','FAILED_FINAL','CANCELLED_FINAL','READY_FOR_ACTIVATION');
CREATE TABLE IF NOT EXISTS recovery_operations(
 tenant_id text NOT NULL REFERENCES tenants(id), operation_id text NOT NULL,
 recovery_job_id text NOT NULL, stage text NOT NULL, operation_type text NOT NULL,
 provider text NOT NULL, resource_type text NOT NULL DEFAULT '',
 resource_id text NOT NULL DEFAULT '', provider_task_id text NOT NULL DEFAULT '',
 desired_state text NOT NULL, status text NOT NULL DEFAULT 'NOT_STARTED',
 attempt_count integer NOT NULL DEFAULT 0, started_at timestamptz,
 completed_at timestamptz, last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,operation_id), FOREIGN KEY(tenant_id,recovery_job_id) REFERENCES recovery_jobs(tenant_id,job_id)
);
CREATE INDEX IF NOT EXISTS recovery_operations_job ON recovery_operations(tenant_id,recovery_job_id,stage,operation_type);
CREATE INDEX IF NOT EXISTS recovery_operations_status ON recovery_operations(tenant_id,status,updated_at);
CREATE TABLE IF NOT EXISTS provider_resources(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL,
 recovery_job_id text NOT NULL DEFAULT '', activation_session_id text NOT NULL DEFAULT '',
 provider text NOT NULL, resource_type text NOT NULL, provider_resource_id text NOT NULL,
 operation_id text NOT NULL, status text NOT NULL, handle jsonb NOT NULL DEFAULT '{}',
 cleanup_policy text NOT NULL DEFAULT 'TEST_EPHEMERAL',
 created_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,provider,provider_resource_id)
);
CREATE INDEX IF NOT EXISTS provider_resources_job ON provider_resources(tenant_id,recovery_job_id,resource_type);
CREATE INDEX IF NOT EXISTS provider_resources_operation ON provider_resources(tenant_id,operation_id);
CREATE TABLE IF NOT EXISTS recovery_timeline(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL,
 recovery_job_id text NOT NULL, event_type text NOT NULL, stage text NOT NULL DEFAULT '',
 operation_id text NOT NULL DEFAULT '', detail jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,recovery_job_id) REFERENCES recovery_jobs(tenant_id,job_id)
);
CREATE INDEX IF NOT EXISTS recovery_timeline_job ON recovery_timeline(tenant_id,recovery_job_id,created_at);
CREATE TABLE IF NOT EXISTS activation_sessions(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL,
 recovery_job_id text NOT NULL, recovery_point_id text NOT NULL, provider text NOT NULL,
 provider_resource_ids jsonb NOT NULL DEFAULT '[]',
 billing_mode text NOT NULL DEFAULT 'DISASTER', billable boolean NOT NULL DEFAULT true,
 rate_plan_id text NOT NULL, rate_plan_version integer NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT now(), activated_at timestamptz,
 deactivation_requested_at timestamptz, deactivated_at timestamptz,
 created_by text NOT NULL, ended_by text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '',
 data_may_have_changed boolean NOT NULL DEFAULT false,
 status text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,recovery_job_id) REFERENCES recovery_jobs(tenant_id,job_id)
);
CREATE INDEX IF NOT EXISTS activation_sessions_tenant_cursor ON activation_sessions(tenant_id,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS activation_one_active_recovery ON activation_sessions(tenant_id,recovery_job_id) WHERE status IN ('ACTIVATING','ACTIVE','DEACTIVATING');
CREATE TABLE IF NOT EXISTS power_actions(
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL,
 activation_session_id text NOT NULL, vm_id text NOT NULL, action text NOT NULL, provider text NOT NULL,
 status text NOT NULL, job_id text NOT NULL DEFAULT '', requested_by text NOT NULL,
 provider_response jsonb NOT NULL DEFAULT '{}', error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,activation_session_id) REFERENCES activation_sessions(tenant_id,id)
);
ALTER TABLE power_actions ADD COLUMN IF NOT EXISTS job_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS power_actions_tenant_cursor ON power_actions(tenant_id,id DESC);
CREATE INDEX IF NOT EXISTS power_actions_session ON power_actions(tenant_id,activation_session_id,created_at DESC);
CREATE TABLE IF NOT EXISTS audit_log(
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id text NOT NULL,
 actor text NOT NULL, action text NOT NULL, resource_id text NOT NULL, correlation_id text NOT NULL,
 detail jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_tenant_cursor ON audit_log(tenant_id,sequence DESC);
INSERT INTO schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
