// draas-fixture runs INSIDE the disposable Linux VM, never in the control plane.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const dataDir = "/var/lib/draas-fixture"

func sql(query string) (string, error) {
	ctxArgs := []string{filepath.Join(dataDir, "app.db"), query}
	b, err := exec.Command("sqlite3", ctxArgs...).CombinedOutput()
	return strings.TrimSpace(string(b)), err
}
func state() (map[string]any, error) {
	rev, err := sql("PRAGMA integrity_check; SELECT version FROM app WHERE id=1;")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "marker.bin"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	osrelease, _ := os.ReadFile("/etc/os-release")
	interfaces, _ := net.InterfaceAddrs()
	ips := []string{}
	for _, ip := range interfaces {
		ips = append(ips, ip.String())
	}
	return map[string]any{"healthy": strings.HasPrefix(rev, "ok\n"), "database": rev, "marker_sha256": hex.EncodeToString(sum[:]), "marker_bytes": len(b), "boot_id": strings.TrimSpace(string(boot)), "os_release": string(osrelease), "addresses": ips}, nil
}
func main() {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "app.db")); os.IsNotExist(err) {
		b := make([]byte, 1048576)
		if _, err = rand.Read(b); err != nil {
			log.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dataDir, "marker.bin"), b, 0600); err != nil {
			log.Fatal(err)
		}
		if _, err = sql("CREATE TABLE app(id INTEGER PRIMARY KEY,version INTEGER NOT NULL); INSERT INTO app VALUES(1,1);"); err != nil {
			log.Fatal(err)
		}
	}
	token, err := os.ReadFile("/etc/draas-fixture-token")
	if err != nil {
		log.Fatal(err)
	}
	expected := sha256.Sum256([]byte(strings.TrimSpace(string(token))))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		s, err := state()
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		v, err := sql("SELECT version FROM app WHERE id=1")
		if err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		fmt.Fprintln(w, v)
	})
	mux.HandleFunc("POST /mutate", func(w http.ResponseWriter, r *http.Request) {
		received := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if subtle.ConstantTimeCompare(received[:], expected[:]) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if _, err := sql("BEGIN IMMEDIATE; UPDATE app SET version=version+1 WHERE id=1; COMMIT; PRAGMA wal_checkpoint(FULL);"); err != nil {
			http.Error(w, "mutation failed", 500)
			return
		}
		s, err := state()
		if err != nil {
			http.Error(w, "state unavailable", 500)
			return
		}
		json.NewEncoder(w).Encode(s)
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	log.Fatal(server.ListenAndServe())
}
