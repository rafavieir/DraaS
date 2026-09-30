package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func main() {
	mib := flag.Int("mib", 64, "temporary file size, 1..1024 MiB")
	dir := flag.String("dir", "", "scratch directory; only the generated temporary file is removed")
	flag.Parse()
	if *mib < 1 || *mib > 1024 {
		fmt.Fprintln(os.Stderr, "mib must be 1..1024")
		os.Exit(2)
	}
	if err := run(*mib, *dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(mib int, dir string) error {
	f, err := os.CreateTemp(dir, "draas-bench-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	buf := make([]byte, 1<<20)
	if _, err = rand.Read(buf); err != nil {
		return err
	}
	start := time.Now()
	for i := 0; i < mib; i++ {
		if _, err = f.Write(buf); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	write := time.Since(start).Seconds()
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	h := sha256.New()
	start = time.Now()
	if _, err = io.CopyBuffer(h, f, buf); err != nil {
		return err
	}
	read := time.Since(start).Seconds()
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"scope": "local scratch file; OS cache may affect reads; not storage SLA qualification", "os": runtime.GOOS, "cpus": runtime.NumCPU(), "mib": mib, "write_mib_per_second": float64(mib) / write, "read_hash_mib_per_second": float64(mib) / read, "sha256": fmt.Sprintf("%x", h.Sum(nil)), "zsvirt_provisioning": "not_measured", "network": "not_measured", "production_rto": "not_estimated"})
}
