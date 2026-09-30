package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func RecoveryOperationID(recoveryJobID, stage, operationType, resourceKey string) string {
	parts := []string{recoveryJobID, stage, operationType, resourceKey}
	for i := range parts {
		parts[i] = strings.TrimSpace(strings.ToLower(parts[i]))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "op-" + hex.EncodeToString(sum[:16])
}

func RecoveryStages() []string {
	return []string{
		RecoveryStageRequested,
		RecoveryStagePreflight,
		RecoveryStageAdmission,
		RecoveryStageNetworkPrepare,
		RecoveryStageResourceProvision,
		RecoveryStageDiskMaterialize,
		RecoveryStageDiskAttach,
		RecoveryStagePowerOn,
		RecoveryStageWaitGuest,
		RecoveryStageValidateOS,
		RecoveryStageValidateApplication,
		RecoveryStageReadyForActivation,
	}
}

func TerminalRecoveryStatus(status string) bool {
	switch status {
	case "COMPLETED", "FAILED_FINAL", "CANCELLED_FINAL":
		return true
	default:
		return false
	}
}
