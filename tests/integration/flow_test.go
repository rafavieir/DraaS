package integration

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type harness struct {
	t           *testing.T
	base, token string
	client      *http.Client
}

func (h harness) request(method, path string, payload any, key string, want int) []byte {
	h.t.Helper()
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.base+"/api/v1/"+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		h.t.Fatal(err)
	}
	if res.StatusCode != want {
		h.t.Fatalf("%s %s got %d want %d: %s", method, path, res.StatusCode, want, b)
	}
	return b
}
func (h harness) enqueue(path, key string) catalog.Job {
	h.t.Helper()
	b := h.request("POST", path, map[string]any{}, key, 202)
	var j catalog.Job
	if err := json.Unmarshal(b, &j); err != nil {
		h.t.Fatal(err)
	}
	return j
}
func (h harness) wait(id string) catalog.Job {
	h.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var j catalog.Job
		b := h.request("GET", "jobs/"+id, nil, "", 200)
		if err := json.Unmarshal(b, &j); err != nil {
			h.t.Fatal(err)
		}
		switch j.Status {
		case "COMPLETED":
			return j
		case "FAILED", "CANCELLED":
			h.t.Fatalf("job %s %s: %s", id, j.Status, j.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}
	h.t.Fatal("job timeout", id)
	return catalog.Job{}
}
func (h harness) verified(id string) {
	h.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var response struct{ Items []struct{ ID, Status string } }
		b := h.request("GET", "recovery-points?limit=100", nil, "", 200)
		if err := json.Unmarshal(b, &response); err != nil {
			h.t.Fatal(err)
		}
		for _, p := range response.Items {
			if p.ID == id && p.Status == "VERIFIED" {
				return
			}
			if p.ID == id && p.Status == "INVALID" {
				h.t.Fatal("point invalid", id)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	h.t.Fatal("verification timeout", id)
}
func TestKubernetesVerticalSlice(t *testing.T) {
	base := os.Getenv("DRAAS_TEST_URL")
	token := os.Getenv("DRAAS_TEST_TOKEN")
	if base == "" || token == "" {
		t.Skip("requires running Kubernetes lab and DRAAS_TEST_URL/TOKEN")
	}
	h := harness{t, base, token, &http.Client{Timeout: 35 * time.Second}}
	started := time.Now()
	name := "e2e-linux-" + catalog.ID()
	var workload catalog.Workload
	b := h.request("POST", "workloads", map[string]any{"name": name, "size_mib": 16}, "", 201)
	if err := json.Unmarshal(b, &workload); err != nil {
		t.Fatal(err)
	}
	key := catalog.ID()
	full := h.enqueue("workloads/"+workload.ID+"/backup", key)
	replay := h.enqueue("workloads/"+workload.ID+"/backup", key)
	if replay.ID != full.ID {
		t.Fatal("duplicate request created a second job")
	}
	full = h.wait(full.ID)
	h.verified(full.ID)
	h.request("POST", "workloads/"+workload.ID+"/mutate", map[string]any{}, "", 200)
	h.request("POST", "workloads/"+workload.ID+"/backup", map[string]any{}, key, 409)
	incremental := h.wait(h.enqueue("workloads/"+workload.ID+"/backup", catalog.ID()).ID)
	h.verified(incremental.ID)
	var incResult struct {
		Stats backup.Stats `json:"stats"`
	}
	if err := json.Unmarshal(incremental.Result, &incResult); err != nil {
		t.Fatal(err)
	}
	if incResult.Stats.Uploaded != 1 || incResult.Stats.Reused != 15 {
		t.Fatalf("dedup changed-source mismatch: %+v", incResult.Stats)
	}
	h.request("POST", "workloads/"+workload.ID+"/delete-source", map[string]string{"confirm": workload.ID}, "", 200)
	h.request("POST", "workloads/"+workload.ID+"/backup", map[string]any{}, catalog.ID(), 409)
	test := h.wait(h.enqueue("recovery-points/"+incremental.ID+"/test", catalog.ID()).ID)
	restore := h.wait(h.enqueue("recovery-points/"+incremental.ID+"/restore", catalog.ID()).ID)
	var result struct {
		Report    json.RawMessage `json:"report"`
		Signature []byte          `json:"signature"`
	}
	if err := json.Unmarshal(test.Result, &result); err != nil {
		t.Fatal(err)
	}
	var keyInfo struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(h.request("GET", "signing-key", nil, "", 200), &keyInfo); err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(keyInfo.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var canonical any
	if err = json.Unmarshal(result.Report, &canonical); err != nil {
		t.Fatal(err)
	}
	canonicalBytes, _ := json.Marshal(canonical)
	if !ed25519.Verify(pub, canonicalBytes, result.Signature) {
		t.Fatal("recovery report signature invalid")
	}
	var report struct {
		Result     string `json:"result"`
		Validation struct {
			Simulation bool `json:"simulation"`
			Boot       bool `json:"boot_verified"`
			File       bool `json:"file_valid"`
			Service    bool `json:"service_valid"`
			Cleanup    bool `json:"cleanup"`
		} `json:"validation"`
	}
	if err = json.Unmarshal(result.Report, &report); err != nil {
		t.Fatal(err)
	}
	if report.Result != "SIMULATION_PASS" || !report.Validation.Simulation || report.Validation.Boot || !report.Validation.File || !report.Validation.Service || !report.Validation.Cleanup {
		t.Fatalf("invalid report: %s", result.Report)
	}
	if other := os.Getenv("DRAAS_OTHER_TOKEN"); other != "" {
		oh := h
		oh.token = other
		oh.request("GET", "jobs/"+full.ID, nil, "", 404)
		oh.request("GET", "recovery-points/"+full.ID+"/manifest", nil, "", 404)
		oh.request("POST", "recovery-points/"+full.ID+"/test", map[string]any{}, catalog.ID(), 404)
	} else {
		t.Fatal("DRAAS_OTHER_TOKEN required for isolation test")
	}
	if viewer := os.Getenv("DRAAS_VIEWER_TOKEN"); viewer != "" {
		vh := h
		vh.token = viewer
		vh.request("POST", "workloads", map[string]any{"name": "forbidden", "size_mib": 1}, "", 403)
	} else {
		t.Fatal("DRAAS_VIEWER_TOKEN required")
	}
	h.request("GET", "jobs?limit=101", nil, "", 400)
	h.request("GET", "audit?cursor=bad", nil, "", 400)
	h.request("POST", "workloads/"+workload.ID+"/backup", map[string]any{}, "", 409)
	evidence := map[string]any{"test": "KubernetesVerticalSlice", "completed_at": time.Now().UTC(), "elapsed_seconds": time.Since(started).Seconds(), "workload": workload, "full": full, "incremental": incremental, "test_recovery": test, "restore": restore, "report_signature_verified": true, "tenant_isolation": "PASS", "rbac": "PASS", "idempotency": "PASS", "source_removed_before_restore": true, "real_vm_boot": false}
	if dir := os.Getenv("DRAAS_EVIDENCE_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(evidence, "", "  ")
		if err = os.WriteFile(filepath.Join(dir, "vertical-slice.json"), out, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("PASS full=%s incremental=%s test=%s restore=%s; real_vm_boot=false", full.ID, incremental.ID, test.ID, restore.ID)
}
func Example_scope() {
	fmt.Println("Simulation validates data recovery; it does not validate a guest boot.")
	// Output: Simulation validates data recovery; it does not validate a guest boot.
}
