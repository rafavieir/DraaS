# M2A / M2A+ execution ledger

Priority: real Linux recovery, followed by activation/operation/metering. Existing simulator acceptance must remain green.

- [x] Real KVM/libvirt guest boots from official verified image.
- [x] Real HTTP service, SQLite data and random marker captured before backup.
- [x] Full and changed-data deduplicated backup to existing encrypted S3 pipeline.
- [x] Original domain and disk deleted before restore.
- [x] Guest, OS, network, service, marker and modified database validated after restore.
- [x] Isolated recovery network with actual blocked host-canary and internet-egress checks.
- [x] Signed report independently verified; per-run evidence preserved.
- [ ] Explicit READY -> ACTIVE -> safe DEACTIVATE lifecycle.
- [ ] Authenticated console with tenant/expiry/single-use checks and real guest command.
- [ ] Append-only usage ledger, immutable rate snapshots, replay/outage tests.
- [ ] API/UI/CLI integration, simulator regression and raw evidence.

ZSvirt live qualification requires supplied endpoint and credentials. `production_ready` stays false.

Real-recovery core PASS: [evidence and limitations](../artifacts/20260928-202701-real-linux/acceptance.md).
This is **not** the expanded M2A+ activation/console/metering acceptance. The provider currently runs through the lab acceptance CLI; the existing API/UI/worker continue to expose simulator operations. Do not describe the new provider as integrated into those flows.
