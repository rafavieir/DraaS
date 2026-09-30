// Verify a real recovery artifact against an independently obtained signing key.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"os"
)

func main() {
	reportPath := flag.String("report", "", "signed-report.json path")
	keyPath := flag.String("trusted-key", "", "file containing trusted base64 Ed25519 public key")
	flag.Parse()
	if err := verify(*reportPath, *keyPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: trusted signature, real guest/data checks, source destruction and cleanup")
}
func verify(reportPath, keyPath string) error {
	b, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	var envelope struct {
		Report    map[string]any `json:"report"`
		Signature []byte         `json:"signature"`
		Encoding  string         `json:"signature_encoding"`
	}
	if err = json.Unmarshal(b, &envelope); err != nil {
		return err
	}
	keyText, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(string(keyText))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid trusted public key")
	}
	if envelope.Encoding != "json-map-v1" || !backup.VerifySigned(envelope.Report, envelope.Signature, ed25519.PublicKey(key)) {
		return fmt.Errorf("invalid report signature")
	}
	r := envelope.Report
	if r["status"] != "PASS" || r["simulation"] != false || r["source_destroyed"] != true || r["cleanup_verified"] != true {
		return fmt.Errorf("real recovery acceptance incomplete")
	}
	validation, ok := r["validation"].(map[string]any)
	if !ok {
		return fmt.Errorf("validation absent")
	}
	for _, field := range []string{"boot_verified", "service_verified", "data_verified"} {
		if validation[field] != true {
			return fmt.Errorf("%s not verified", field)
		}
	}
	return nil
}
