package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/platform"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	base := flag.String("url", "http://127.0.0.1:8080", "API URL")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: draasctl [-url URL] status | workload list | recovery-point list | activation-session list | power-action list | job ID | backup ID | recovery test ID | recovery start ID | vm power SESSION_ID VM_ID start|shutdown|reboot|force-stop|state | catalog-rebuild")
	}
	if args[0] == "catalog-rebuild" {
		return rebuild()
	}
	path, method, body := "", "GET", ""
	switch args[0] {
	case "status":
		path = "overview"
	case "workload":
		path = "workloads"
	case "recovery-point":
		path = "recovery-points"
	case "activation-session":
		path = "activation-sessions"
	case "power-action":
		path = "power-actions"
	case "job":
		if len(args) != 2 {
			return fmt.Errorf("job ID required")
		}
		path = "jobs/" + args[1]
	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("backup WORKLOAD_ID required")
		}
		path = "workloads/" + args[1] + "/backup"
		method = "POST"
		body = "{}"
	case "vm":
		if len(args) != 5 || args[1] != "power" {
			return fmt.Errorf("usage: vm power SESSION_ID VM_ID start|shutdown|reboot|force-stop|state")
		}
		action := args[4]
		if action != "start" && action != "shutdown" && action != "reboot" && action != "force-stop" && action != "state" {
			return fmt.Errorf("unknown power action")
		}
		path = "activation-sessions/" + args[2] + "/vms/" + args[3] + "/" + action
		method = "POST"
		body = "{}"
	case "recovery":
		if len(args) != 3 {
			return fmt.Errorf("recovery test|start POINT_ID required")
		}
		action := "test"
		if args[1] == "start" {
			action = "restore"
		} else if args[1] != "test" {
			return fmt.Errorf("unknown recovery action")
		}
		path = "recovery-points/" + args[2] + "/" + action
		method = "POST"
		body = "{}"
	default:
		return fmt.Errorf("unsupported command")
	}
	token := os.Getenv("DRAAS_TOKEN")
	if token == "" {
		return fmt.Errorf("DRAAS_TOKEN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(*base, "/")+"/api/v1/"+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", catalog.ID())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	if res.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}
func rebuild() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	app, err := platform.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()
	tenant := os.Getenv("REBUILD_TENANT")
	if !backup.ValidID(tenant) {
		return fmt.Errorf("REBUILD_TENANT required")
	}
	allowed := false
	for _, p := range cfg.Principals {
		if p.Tenant == tenant && p.Role == "admin" {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("configured admin principal required for tenant")
	}
	keys, err := app.Store.List(ctx, "manifests/"+tenant+"/")
	if err != nil {
		return err
	}
	count := 0
	for _, key := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(key, "manifests/"+tenant+"/"), ".json")
		m, _, err := app.Engine.Verify(ctx, tenant, id)
		if err != nil {
			return fmt.Errorf("verify %s: %w", id, err)
		}
		_, err = app.DB.Pool.Exec(ctx, "INSERT INTO workloads(tenant_id,id,name,source,size,active) VALUES($1,$2,$2,'simulator',$3,false) ON CONFLICT DO NOTHING", tenant, m.Workload, m.Size)
		if err != nil {
			return err
		}
		if err = app.DB.SavePoint(ctx, m, backup.Stats{Bytes: m.Size}); err != nil {
			return err
		}
		if err = app.DB.SetPointStatus(ctx, tenant, id, "VERIFIED"); err != nil {
			return err
		}
		count++
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"tenant": tenant, "reconstructed_points": count})
}
