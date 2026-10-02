.PHONY: test-provider-contract test-provider-contract-compile test-worker-takeover test-worker-takeover-real test-p3d1-crash-matrix test-storage-failover test-catalog-rebuild test-scheduled-dr test-admission test-chaos bootstrap-p3d1-lab check-p3d1-lab

test-provider-contract:
	go test ./pkg/contracts ./providers/simulator ./providers/libvirtlab ./tests/providercontract ./internal/platform ./internal/catalog ./internal/failpoint -run 'TestJobSubjectSchemaVersions|TestWorkerSupports|Test.*ProviderContractV2|TestRecovery|TestTrigger'

test-provider-contract-compile:
	go test -exec ./.local/tools/test-exec-ok.cmd ./internal/catalog ./tests/providercontract ./internal/platform ./internal/failpoint ./internal/recoverylab ./cmd/draas-lab-check-invariants

test-worker-takeover:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1

test-worker-takeover-real:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_provider_call_before_persist

test-p3d1-crash-matrix:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_provider_call_before_persist
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_vm_create
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint during_disk_materialize
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_disk_materialize
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_disk_attach
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_power_on
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint before_app_validation
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-recovery-takeover.ps1 -Failpoint after_app_validation

test-storage-failover:
	@echo "P3D.2 storage failover harness is not implemented yet; do not mark PASS."
	@exit 1

test-catalog-rebuild:
	@echo "P3D.3 catalog rebuild harness is not implemented yet; do not mark PASS."
	@exit 1

test-scheduled-dr:
	@echo "P3D.4 scheduled DR harness is not implemented yet; do not mark PASS."
	@exit 1

test-admission:
	@echo "P3D.5 admission harness is not implemented yet; do not mark PASS."
	@exit 1

test-chaos:
	@echo "P3D.6 chaos harness is not implemented yet; do not mark PASS."
	@exit 1

check-p3d1-lab:
	wsl -e sh -lc 'cd /mnt/c/Users/rafael/Desktop/DR && chmod +x scripts/check-p3d1-lab.sh && scripts/check-p3d1-lab.sh'

bootstrap-p3d1-lab:
	wsl -e sh -lc 'cd /mnt/c/Users/rafael/Desktop/DR && chmod +x scripts/bootstrap-p3d1-lab.sh scripts/check-p3d1-lab.sh && scripts/bootstrap-p3d1-lab.sh'
