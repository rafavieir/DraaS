package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/web"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type principalKey struct{}
type correlationKey struct{}
type limiter struct {
	mu      sync.Mutex
	windows map[string]window
}
type window struct {
	start time.Time
	count int
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[key]
	if time.Since(w.start) > time.Minute {
		w = window{start: time.Now()}
	}
	w.count++
	l.windows[key] = w
	return w.count <= 180
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"error_code": code, "message": message, "component": "api", "operation": r.Method + " " + r.URL.Path, "retryable": status >= 500, "correlation_id": r.Context().Value(correlationKey{})})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON object")
	}
	return nil
}
func principal(r *http.Request) Principal { return r.Context().Value(principalKey{}).(Principal) }
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	limit := &limiter{windows: map[string]window{}}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.DB.Pool.Ping(ctx); err != nil || !a.NC.IsConnected() {
			failure(w, r, 503, "DEPENDENCY_UNAVAILABLE", "CatÃ¡logo ou fila indisponÃ­vel.")
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/branding", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"name": a.Config.Brand, "mode": "LAB / REAL RECOVERY CORE", "accent": "#137965"})
	})
	private := http.NewServeMux()
	private.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		jsonResponse(w, 200, map[string]string{"tenant": p.Tenant, "role": p.Role, "actor": p.Actor})
	})
	private.HandleFunc("GET /api/v1/overview", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.DB.Overview(r.Context(), principal(r).Tenant)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 200, v)
	})
	private.HandleFunc("GET /api/v1/infrastructure", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		db := "HEALTHY"
		if a.DB.Pool.Ping(ctx) != nil {
			db = "UNAVAILABLE"
		}
		queue := "HEALTHY"
		if !a.NC.IsConnected() {
			queue = "UNAVAILABLE"
		}
		storageStatus := "HEALTHY"
		storageBackend := strings.ToUpper(a.Config.StorageBackend)
		protectionBackend := strings.ToUpper(a.Config.ProtectionBackend)
		if a.Config.StorageBackend == "s3" {
			if a.Store == nil {
				storageStatus = "UNAVAILABLE"
			} else if ok, err := a.Store.Client.BucketExists(ctx, a.Store.Bucket); err != nil || !ok {
				storageStatus = "UNAVAILABLE"
			}
		}
		if a.Config.ProtectionBackend == "zfs" {
			if os.Getenv("DRAAS_ZFS_POOL") == "" {
				storageStatus = "UNAVAILABLE"
			}
		} else if a.Config.ProtectionBackend == "kubernetes-zfs" {
			if os.Getenv("DRAAS_K8S_ZFS_STORAGE_CLASS") == "" {
				storageStatus = "UNAVAILABLE"
			}
		} else if a.Config.ProtectionBackend == "velero" {
			if os.Getenv("DRAAS_VELERO_NAMESPACE") == "" {
				storageStatus = "UNAVAILABLE"
			}
		}
		provider := "SIMULATOR"
		if os.Getenv("LIBVIRT_LAB_ROOT") != "" {
			provider = "LIBVIRT_LAB"
		}
		jsonResponse(w, 200, map[string]any{"postgresql": db, "nats": queue, "storage_backend": storageBackend, "protection_backend": protectionBackend, "storage": storageStatus, "zfs_pool": os.Getenv("DRAAS_ZFS_POOL"), "kubernetes_zfs_storage_class": os.Getenv("DRAAS_K8S_ZFS_STORAGE_CLASS"), "kubernetes_recovery_namespace": env("DRAAS_K8S_RECOVERY_NAMESPACE", "draas-recovery"), "velero_namespace": env("DRAAS_VELERO_NAMESPACE", "velero"), "recovery_provider": provider, "kubernetes": "DEPLOYMENT_MANAGED", "zsvirt": "NOT_CONFIGURED", "object_lock": "NOT_CONFIGURED"})
	})
	private.HandleFunc("GET /api/v1/signing-key", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"algorithm": "Ed25519", "public_key": a.Engine.Trusted})
	})
	private.HandleFunc("GET /api/v1/metrics", a.metrics)
	private.HandleFunc("GET /api/v1/debug/pprof/{profile}", func(w http.ResponseWriter, r *http.Request) {
		if principal(r).Role != "admin" || os.Getenv("ENABLE_PPROF") != "true" {
			failure(w, r, 403, "PROFILING_DISABLED", "Profiling requer administrador e ENABLE_PPROF=true.")
			return
		}
		switch name := r.PathValue("profile"); name {
		case "heap", "allocs", "goroutine":
			pprof.Handler(name).ServeHTTP(w, r)
		default:
			failure(w, r, 404, "PROFILE_NOT_ALLOWED", "Perfis disponÃ­veis: heap, allocs e goroutine.")
		}
	})
	private.HandleFunc("GET /api/v1/{collection}", func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("collection")
		if kind != "workloads" && kind != "recovery-points" && kind != "recovery-jobs" && kind != "activation-sessions" && kind != "power-actions" && kind != "jobs" && kind != "tests" && kind != "audit" {
			failure(w, r, 404, "NOT_FOUND", "Recurso nÃ£o encontrado.")
			return
		}
		n := 25
		if raw := r.URL.Query().Get("limit"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > 100 {
				failure(w, r, 400, "INVALID_LIMIT", "Use limit entre 1 e 100.")
				return
			}
			n = v
		}
		cursor := r.URL.Query().Get("cursor")
		if len(cursor) > 100 {
			failure(w, r, 400, "INVALID_CURSOR", "Cursor invÃ¡lido.")
			return
		}
		if kind == "audit" && cursor != "" {
			if _, err := strconv.ParseInt(cursor, 10, 64); err != nil {
				failure(w, r, 400, "INVALID_CURSOR", "Cursor invÃ¡lido.")
				return
			}
		}
		items, err := a.DB.List(r.Context(), principal(r).Tenant, kind, cursor, n+1)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		next := ""
		if len(items) > n {
			items = items[:n]
			var last map[string]any
			_ = json.Unmarshal(items[len(items)-1], &last)
			if kind == "audit" {
				next = fmt.Sprintf("%.0f", last["sequence"])
			} else {
				next, _ = last["id"].(string)
			}
		}
		jsonResponse(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	private.HandleFunc("POST /api/v1/workloads", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string `json:"name"`
			SizeMiB int64  `json:"size_mib"`
		}
		if err := decode(w, r, &body); err != nil || len(strings.TrimSpace(body.Name)) < 1 || len(body.Name) > 120 || body.SizeMiB < 1 || body.SizeMiB > 256 {
			failure(w, r, 400, "INVALID_WORKLOAD", "Informe nome e tamanho entre 1 e 256 MiB para o simulador.")
			return
		}
		p := principal(r)
		v, err := a.DB.CreateWorkload(r.Context(), p.Tenant, p.Actor, strings.TrimSpace(body.Name), body.SizeMiB<<20)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 201, v)
	})
	private.HandleFunc("POST /api/v1/workloads/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		id, action := r.PathValue("id"), r.PathValue("action")
		if !backup.ValidID(id) {
			failure(w, r, 400, "INVALID_ID", "Identificador invÃ¡lido.")
			return
		}
		wl, err := a.DB.Workload(r.Context(), p.Tenant, id)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		if action == "backup" {
			if !wl.Active {
				failure(w, r, 409, "SOURCE_REMOVED", "A origem simulada foi removida. Use um recovery point existente.")
				return
			}
			a.enqueue(w, r, "backup", id, wl)
			return
		}
		if action != "mutate" && action != "delete-source" {
			failure(w, r, 404, "UNKNOWN_ACTION", "AÃ§Ã£o indisponÃ­vel.")
			return
		}
		if action == "delete-source" {
			var b struct {
				Confirm string `json:"confirm"`
			}
			if err = decode(w, r, &b); err != nil || b.Confirm != id {
				failure(w, r, 400, "CONFIRM_REQUIRED", "Confirme o ID da origem simulada a remover.")
				return
			}
		}
		if err = a.DB.ChangeWorkload(r.Context(), p.Tenant, p.Actor, id, action); err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "OK"})
	})
	private.HandleFunc("POST /api/v1/recovery-points/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, action := r.PathValue("id"), r.PathValue("action")
		if action != "verify" && action != "test" && action != "restore" {
			failure(w, r, 404, "UNSUPPORTED_ACTION", "AÃ§Ã£o nÃ£o implementada neste laboratÃ³rio.")
			return
		}
		status, err := a.DB.PointStatus(r.Context(), principal(r).Tenant, id)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		if action != "verify" && status != "VERIFIED" {
			failure(w, r, 409, "POINT_NOT_VERIFIED", "Verifique a integridade antes de recuperar.")
			return
		}
		a.enqueue(w, r, action, id, map[string]string{"point_id": id})
	})
	private.HandleFunc("POST /api/v1/recovery-jobs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RecoveryPointID string `json:"recovery_point_id"`
			ProviderType    string `json:"provider_type"`
			KeepResources   bool   `json:"keep_resources"`
		}
		if err := decode(w, r, &body); err != nil || !backup.ValidID(body.RecoveryPointID) {
			failure(w, r, 400, "INVALID_RECOVERY_JOB", "Informe recovery_point_id valido.")
			return
		}
		if body.ProviderType == "" {
			body.ProviderType = "LIBVIRT_LAB"
		}
		if body.ProviderType != "LIBVIRT_LAB" {
			failure(w, r, 400, "PROVIDER_UNSUPPORTED", "Provider real disponivel nesta fase: LIBVIRT_LAB.")
			return
		}
		status, err := a.DB.PointStatus(r.Context(), principal(r).Tenant, body.RecoveryPointID)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		if status != "VERIFIED" {
			failure(w, r, 409, "POINT_NOT_VERIFIED", "TEST RECOVERY real exige recovery point VERIFIED.")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !backup.ValidID(key) {
			failure(w, r, 400, "IDEMPOTENCY_REQUIRED", "Envie Idempotency-Key valido.")
			return
		}
		p := principal(r)
		rj, j, err := a.DB.CreateRecoveryJob(r.Context(), p.Tenant, p.Actor, body.RecoveryPointID, body.ProviderType, key, body.KeepResources)
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 202, map[string]any{"recovery_job": rj, "job": j, "billing_mode": "TEST", "billable": false})
	})
	private.HandleFunc("POST /api/v1/recovery-jobs/{id}/activate", func(w http.ResponseWriter, r *http.Request) {
		if !backup.ValidID(r.PathValue("id")) {
			failure(w, r, 400, "INVALID_ID", "Identificador invalido.")
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if err := decode(w, r, &body); err != nil {
			failure(w, r, 400, "INVALID_ACTIVATION", "Informe um JSON valido para ativacao.")
			return
		}
		p := principal(r)
		session, err := a.DB.ActivateRecoveryJob(r.Context(), p.Tenant, p.Actor, r.PathValue("id"), strings.TrimSpace(body.Reason))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				failure(w, r, 409, "RECOVERY_NOT_ACTIVATABLE", "ACTIVATE DR exige recovery persistente em READY_FOR_ACTIVATION.")
				return
			}
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 201, map[string]any{"activation_session": session, "billing_active": session.Billable && session.Status == "ACTIVE"})
	})
	private.HandleFunc("POST /api/v1/activation-sessions/{session}/vms/{vm}/{action}", func(w http.ResponseWriter, r *http.Request) {
		sessionID, vmID, action := r.PathValue("session"), r.PathValue("vm"), r.PathValue("action")
		if !backup.ValidID(sessionID) || !backup.ValidID(vmID) {
			failure(w, r, 400, "INVALID_ID", "Identificador invalido.")
			return
		}
		if action != "start" && action != "shutdown" && action != "reboot" && action != "force-stop" && action != "state" {
			failure(w, r, 404, "UNKNOWN_POWER_ACTION", "Use start, shutdown, reboot, force-stop ou state.")
			return
		}
		p := principal(r)
		session, err := a.DB.VMInActiveSession(r.Context(), p.Tenant, sessionID, vmID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				failure(w, r, 409, "DR_SESSION_NOT_ACTIVE", "Power control exige sessao DR ativa e VM pertencente a sessao.")
				return
			}
			a.dbError(w, r, err)
			return
		}
		a.enqueue(w, r, "vm-power", sessionID, map[string]string{"activation_session_id": session.ID, "vm_id": vmID, "action": action})
	})
	private.HandleFunc("GET /api/v1/recovery-points/{id}/manifest", func(w http.ResponseWriter, r *http.Request) {
		if a.Config.StorageBackend == "zfs" {
			raw, _, _, err := a.DB.RecoveryPointManifest(r.Context(), principal(r).Tenant, r.PathValue("id"))
			if err != nil {
				a.dbError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write(raw)
			return
		}
		m, err := a.Engine.Load(r.Context(), principal(r).Tenant, r.PathValue("id"))
		if err != nil {
			if errors.Is(err, backup.ErrNotFound) {
				failure(w, r, 404, "NOT_FOUND", "Manifest nÃ£o encontrado.")
			} else {
				failure(w, r, 409, "MANIFEST_INVALID", "NÃ£o foi possÃ­vel validar o manifest. Consulte o job de auditoria.")
			}
			return
		}
		jsonResponse(w, 200, m)
	})
	private.HandleFunc("GET /api/v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, err := a.DB.Job(r.Context(), principal(r).Tenant, r.PathValue("id"))
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 200, j)
	})
	private.HandleFunc("POST /api/v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if err := a.DB.Cancel(r.Context(), p.Tenant, p.Actor, r.PathValue("id")); err != nil {
			a.dbError(w, r, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "CANCELLED"})
	})
	mux.Handle("/api/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		hash := sha256.Sum256([]byte(raw))
		var p Principal
		found := false
		for _, candidate := range a.Config.Principals {
			expected := sha256.Sum256([]byte(candidate.Token))
			if subtle.ConstantTimeCompare(hash[:], expected[:]) == 1 {
				p = candidate
				p.Token = ""
				found = true
			}
		}
		if !found {
			failure(w, r, 401, "UNAUTHORIZED", "Credencial invÃ¡lida ou ausente. Entre novamente.")
			return
		}
		if !limit.allow(p.Actor + "/" + p.Tenant) {
			failure(w, r, 429, "RATE_LIMIT", "Limite de requisiÃ§Ãµes atingido. Aguarde um minuto.")
			return
		}
		if r.Method != "GET" && p.Role == "viewer" {
			failure(w, r, 403, "FORBIDDEN", "Seu perfil permite apenas leitura.")
			return
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), principalKey{}, p), 30*time.Second)
		defer cancel()
		private.ServeHTTP(w, r.WithContext(ctx))
	}))
	mux.Handle("/", http.FileServer(http.FS(web.Assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := catalog.ID()
		ctx, span := otel.Tracer("draas.api").Start(context.WithValue(r.Context(), correlationKey{}, id), r.Method+" "+r.URL.Path)
		defer span.End()
		span.SetAttributes(attribute.String("correlation.id", id))
		w.Header().Set("X-Correlation-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (a *App) enqueue(w http.ResponseWriter, r *http.Request, kind, id string, payload any) {
	key := r.Header.Get("Idempotency-Key")
	if !backup.ValidID(key) {
		failure(w, r, 400, "IDEMPOTENCY_REQUIRED", "Envie Idempotency-Key de 1 a 100 caracteres alfanumÃ©ricos, hÃ­fen ou sublinhado.")
		return
	}
	p := principal(r)
	j, err := a.DB.Enqueue(r.Context(), p.Tenant, p.Actor, kind, id, key, payload)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	jsonResponse(w, 202, j)
}
func (a *App) dbError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, r, 404, "NOT_FOUND", "Recurso nÃ£o encontrado neste tenant ou aÃ§Ã£o incompatÃ­vel com o estado atual.")
		return
	}
	if errors.Is(err, catalog.ErrConflict) {
		failure(w, r, 409, "IDEMPOTENCY_CONFLICT", err.Error())
		return
	}
	slog.Error("catalog operation", "error", err, "correlation_id", r.Context().Value(correlationKey{}))
	failure(w, r, 503, "CATALOG_UNAVAILABLE", "NÃ£o foi possÃ­vel consultar ou atualizar o catÃ¡logo. Tente novamente e informe o cÃ³digo de correlaÃ§Ã£o ao suporte.")
}
func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	v, err := a.DB.Overview(r.Context(), principal(r).Tenant)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE draas_verified_recovery_points gauge\ndraas_verified_recovery_points %v\n# TYPE draas_failed_jobs gauge\ndraas_failed_jobs %v\n# TYPE draas_logical_backup_bytes gauge\ndraas_logical_backup_bytes %v\n", v["verified_points"], v["failed_jobs"], v["logical_bytes"])
}
