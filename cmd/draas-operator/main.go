package main

import (
	"context"
	"fmt"
	"github.com/draas-platform/draas/internal/catalog"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metrics "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"time"
)

var workloadGVK = schema.GroupVersionKind{Group: "dr.draas.local", Version: "v1alpha1", Kind: "ProtectedWorkload"}
var policyGVK = schema.GroupVersionKind{Group: "dr.draas.local", Version: "v1alpha1", Kind: "ProtectionPolicy"}

type reconciler struct {
	client client.Client
	db     *catalog.DB
	tenant string
}

func object(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(gvk)
	return o
}
func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	w := object(workloadGVK)
	if err := r.client.Get(ctx, req.NamespacedName, w); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	suspended, _, _ := unstructured.NestedBool(w.Object, "spec", "suspended")
	if suspended {
		return r.status(ctx, w, "Suspended", "ProteÃ§Ã£o suspensa declarativamente", "")
	}
	tenant, _, _ := unstructured.NestedString(w.Object, "spec", "tenantID")
	id, _, _ := unstructured.NestedString(w.Object, "spec", "workloadID")
	policy, _, _ := unstructured.NestedString(w.Object, "spec", "policyRef")
	if tenant != r.tenant {
		return r.status(ctx, w, "Rejected", "Tenant nÃ£o autorizado para este Operator", "")
	}
	p := object(policyGVK)
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: policy}, p); err != nil {
		return r.status(ctx, w, "Blocked", "ProtectionPolicy nÃ£o encontrada", "")
	}
	interval, _, _ := unstructured.NestedInt64(p.Object, "spec", "rpoSeconds")
	rto, _, _ := unstructured.NestedInt64(p.Object, "spec", "rtoSeconds")
	if interval < 60 || rto < 10 {
		return r.status(ctx, w, "Rejected", "RPO mÃ­nimo 60s; RTO mÃ­nimo 10s", "")
	}
	wl, err := r.db.Workload(ctx, tenant, id)
	if err != nil {
		return r.status(ctx, w, "Blocked", "Workload ausente no catÃ¡logo", "")
	}
	if !wl.Active {
		return r.status(ctx, w, "Blocked", "Origem simulada removida", "")
	}
	if _, err = r.db.Pool.Exec(ctx, "UPDATE workloads SET rpo_seconds=$3,rto_seconds=$4 WHERE tenant_id=$1 AND id=$2", tenant, id, interval, rto); err != nil {
		return ctrl.Result{}, err
	}
	var inflight bool
	if err = r.db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE tenant_id=$1 AND resource_id=$2 AND kind='backup' AND status IN ('QUEUED','RUNNING','RETRYING'))", tenant, id).Scan(&inflight); err != nil {
		return ctrl.Result{}, err
	}
	if inflight {
		return r.status(ctx, w, "Protecting", "Backup em andamento", "")
	}
	key := fmt.Sprintf("schedule-%s-%d", id, time.Now().Unix()/interval)
	j, err := r.db.Enqueue(ctx, tenant, "kubernetes-operator", "backup", id, key, wl)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.status(ctx, w, "Scheduled", "Backup persistido no catÃ¡logo", j.ID)
}
func (r *reconciler) status(ctx context.Context, w *unstructured.Unstructured, phase, message, job string) (ctrl.Result, error) {
	base := w.DeepCopy()
	status := map[string]any{"phase": phase, "message": message, "observedGeneration": w.GetGeneration()}
	if job != "" {
		status["lastJobID"] = job
	}
	w.Object["status"] = status
	err := r.client.Status().Patch(ctx, w, client.MergeFrom(base))
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}
func main() {
	ctx := ctrl.SetupSignalHandler()
	ns := os.Getenv("OPERATOR_NAMESPACE")
	if ns == "" {
		ns = "draas-system"
	}
	tenant := os.Getenv("OPERATOR_TENANT")
	if tenant == "" {
		panic("OPERATOR_TENANT is required; namespace is bound to one tenant")
	}
	db, err := catalog.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer db.Pool.Close()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}, Metrics: metrics.Options{BindAddress: "0"}, HealthProbeBindAddress: ":8081"})
	if err != nil {
		panic(err)
	}
	r := &reconciler{client: mgr.GetClient(), db: db, tenant: tenant}
	if err = ctrl.NewControllerManagedBy(mgr).For(object(workloadGVK)).Complete(r); err != nil {
		panic(err)
	}
	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(err)
	}
	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic(err)
	}
	if err = mgr.Start(ctx); err != nil {
		panic(err)
	}
}
