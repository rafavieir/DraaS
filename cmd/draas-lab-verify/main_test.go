package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/draas-platform/draas/internal/backup"
	"os"
	"path/filepath"
	"testing"
)

func TestReportTrustAndTampering(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	path := filepath.Join(dir, "report.json")
	if err = os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(pub)), 0600); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "PASS", "simulation": false, "source_destroyed": true, "cleanup_verified": true, "validation": map[string]any{"boot_verified": true, "service_verified": true, "data_verified": true}}
	engine := backup.Engine{Signer: priv}
	sig, err := engine.Sign(report)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{"report": report, "signature": sig, "signature_encoding": "json-map-v1"}
	write := func() {
		t.Helper()
		b, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if err = verify(path, key); err != nil {
		t.Fatal(err)
	}
	report["source_destroyed"] = false
	write()
	if verify(path, key) == nil {
		t.Fatal("tampering accepted")
	}
	sig, err = engine.Sign(report)
	if err != nil {
		t.Fatal(err)
	}
	envelope["signature"] = sig
	write()
	if verify(path, key) == nil {
		t.Fatal("signed incomplete recovery accepted")
	}
	report["source_destroyed"] = true
	sig, _ = engine.Sign(report)
	envelope["signature"] = sig
	write()
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(other)), 0600)
	if verify(path, key) == nil {
		t.Fatal("untrusted signer accepted")
	}
}
