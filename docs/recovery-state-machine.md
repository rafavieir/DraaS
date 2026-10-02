# Recovery State Machine P3D.1

Status: in progress. This document describes the persisted workflow foundation; it is not P3D.1 PASS evidence yet.

The worker is not the owner of long-running recovery state. PostgreSQL stores desired workflow state and operation intent, the provider stores real resources, and workers act as transient reconcilers.

## Persisted state

`recovery_jobs` now carries workflow fields for provider contract version, desired state, retry scheduling, worker heartbeat, lease owner, lease expiry and optimistic versioning.

`recovery_operations` records every external mutation before it is executed. The operation ID is deterministic from recovery job, stage, operation type and resource key. Retries therefore address the same logical operation instead of creating duplicate VMs, disks or networks.

`provider_resources` records resources discovered or created in the provider, including operation ID, provider resource ID, handle JSON and cleanup policy. This enables GET/OBSERVE before mutation when an operation is UNKNOWN.

`recovery_timeline` stores append-only technical events for stage and provider operation transitions. This is separate from user audit.

## Transaction boundary

The intended pattern is:

1. Persist operation intent as NOT_STARTED.
2. Commit.
3. Call provider.
4. Persist CONFIRMED, UNKNOWN or FAILED in a new transaction.

A PostgreSQL transaction must not stay open while a provider call waits for minutes.

## External action states

- NOT_STARTED: intent exists, mutation has not been attempted.
- UNKNOWN: call may have succeeded but confirmation was lost; reconciler must observe provider before mutation.
- CONFIRMED: provider resource/action is known to match desired state.
- FAILED: operation failed and needs retry/final classification.

## Safety rules

- READY_FOR_ACTIVATION is not terminal for reconciliation because resources remain live.
- ACTIVE_DR resources must not be cleaned without a deactivation workflow.
- TEST_EPHEMERAL resources require resumable cleanup.
- Disk materialization must promote only after hash verification; partial files must not be attached.

## Current implementation state

Implemented now: schema, deterministic operation IDs, provider resource inventory, operation status helpers and timeline append helpers.

Remaining for P3D.1 PASS: wire the real Linux recovery path into one-transition reconciliation, implement leases/takeover loop, add failpoints, execute crash matrix on Linux/libvirt, and rerun P3A/P3B/P3C regressions.

## Worker takeover implementation update

Implemented after the initial foundation: atomic lease acquisition, lease generation/fencing, heartbeat renewal, lease release, runnable recovery scanner, internal-stage reconciliation, and lab failpoints. The real LIBVIRT_LAB recovery path is still not fully decomposed into one-transition handlers, so the takeover harness exists but reports BLOCKED rather than PASS.

## Libvirt adoption and crash-safety update

Implemented after the lease/scanner foundation:

- LIBVIRT_LAB VM XML now persists `operation_id`, `recovery_job_id`, `tenant_id`, `recovery_point_id`, `environment_type` and `managed_by=draas` metadata.
- `CreateWithOperation` can define a VM with deterministic operation metadata.
- `VMFromDomain` and `ListManagedResources` can rediscover managed VMs from libvirt XML after worker death.
- `after_provider_call_before_persist` and `after_vm_create` failpoints are wired immediately after `virsh define` and before the VM record is saved.
- Real recovery now persists a `PROVISION_VM` operation as `UNKNOWN` before calling the lab runner, then confirms it and records `provider_resources` when the VM reaches the report.
- Disk restore writes to `recovery.qcow2.partial`, fsyncs, hashes, and only then renames to `recovery.qcow2`; `.partial` is deleted on failure and never attached.
- Additional failpoints are wired for disk materialization, power-on and app validation boundaries.
- `draas-lab-check-invariants` checks duplicate VM records and operation/resource consistency in the catalog.

Still missing for P3D.1 PASS: replace the remaining procedural sections of real recovery with one-transition handlers and execute the crash matrix on a real libvirt/KVM environment.

## Automatic UNKNOWN adoption update

Implemented next:

- The provision operation ID used by the worker is now passed into the real lab runner, so the database operation and libvirt XML metadata use the same deterministic ID.
- Libvirt XML now separates `recovery_job_id` from `run_id`, preserving the catalog job identity while keeping the lab run path stable.
- `ReconcileRecoveryJob` handles `RESOURCE_PROVISION` adoption for UNKNOWN/CONFIRMED operations: it searches libvirt by `operation_id`, validates tenant/operation metadata, records `provider_resources`, marks the operation CONFIRMED, emits `PROVIDER_RESOURCE_ADOPTED`, and advances to `DISK_MATERIALIZE`.
- The invariant checker can also report `.partial` disk artifacts when `DRAAS_CHECK_RUN_DIR` is supplied.

Remaining for P3D.1 PASS: implement stage handlers for DISK_MATERIALIZE, DISK_ATTACH, POWER_ON, WAIT_GUEST, VALIDATE_OS and VALIDATE_APPLICATION, then execute the real crash matrix.

## Reconciler stage handlers update

Implemented next:

- `CanTransition(from,to)` now guards the formal recovery stage order.
- `AdvanceRecoveryStage` reads the current persisted stage under the same lease/version and rejects arbitrary jumps.
- `DISK_ATTACH` is now handled as an idempotent stage: it refuses `.partial`, verifies the final disk path from the adopted VM handle, confirms `ATTACH_DISK`, and advances.
- `POWER_ON` observes provider power state first. If already running, it confirms without another start. Otherwise it persists UNKNOWN, starts through provider v2, observes RUNNING, confirms, and advances.
- `WAIT_GUEST` observes guest state only and advances when healthy.
- `VALIDATE_OS` runs read-only Linux/boot-id console validation.
- `VALIDATE_APPLICATION` runs read-only guest health and SQLite integrity/version validation, with before/after app validation failpoints.
- Make targets `test-worker-takeover-real` and `test-p3d1-crash-matrix` were added. They still depend on the real libvirt harness being executable in the lab environment.

Still missing for P3D.1 PASS: real crash matrix execution, duplicate-delivery scenario, two-worker race scenario, provider-timeout ambiguity, and P3A/P3B/P3C regression reruns in a supported Linux/libvirt runtime.
