package platform

import "github.com/draas-platform/draas/pkg/contracts"

const (
	jobSchemaV1 = "v1"
	jobSchemaV2 = "v2"
)

var jobSchemaByKind = map[string]string{
	"vm-power": jobSchemaV2,
}

func jobSchemaVersion(kind string) string {
	if v := jobSchemaByKind[kind]; v != "" {
		return v
	}
	return jobSchemaV1
}

func jobSubject(kind string) string {
	return "draas.jobs." + kind + "." + jobSchemaVersion(kind)
}

func workerSupports(ev contracts.Event, subject string) bool {
	if ev.Version != 1 || ev.TenantID == "" || ev.ResourceID == "" || ev.Kind == "" {
		return false
	}
	return subject == jobSubject(ev.Kind)
}
