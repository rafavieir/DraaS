package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/recoverylab"
	"github.com/draas-platform/draas/internal/storage"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	id := "m2a-" + time.Now().UTC().Format("20060102-150405")
	fmt.Println("RUN_ID=" + id)
	master, err := hex.DecodeString(os.Getenv("MASTER_KEY"))
	if err != nil || len(master) != 32 {
		return fmt.Errorf("invalid master key")
	}
	seed, err := hex.DecodeString(os.Getenv("SIGNING_SEED"))
	if err != nil || len(seed) != 32 {
		return fmt.Errorf("invalid signing seed")
	}
	signer := ed25519.NewKeyFromSeed(seed)
	store, err := storage.New(os.Getenv("S3_ENDPOINT"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_BUCKET"), false)
	if err != nil {
		return err
	}
	if err = store.Ensure(ctx); err != nil {
		return err
	}
	engine := &backup.Engine{Store: store, Master: master, Signer: signer, Trusted: signer.Public().(ed25519.PublicKey)}
	signed, err := recoverylab.Run(ctx, recoverylab.Options{
		Tenant: "lab-tenant", RunID: id, Root: os.Getenv("LIBVIRT_LAB_ROOT"),
		FixtureBinary: os.Getenv("LIBVIRT_FIXTURE_BINARY"), Engine: engine,
	}, func(stage string, _ int64) {
		fmt.Println(time.Now().UTC().Format(time.RFC3339), stage)
	})
	if err != nil {
		return err
	}
	fmt.Println("PASS", signed.Report["run_id"], "signature_verified=true")
	return nil
}
