package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/storage"
	"github.com/minio/minio-go/v7"
	"io"
	"os"
	"testing"
	"time"
)

func TestS3ConditionalCreationAndCorruption(t *testing.T) {
	endpoint := os.Getenv("DRAAS_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires isolated lab S3 configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := storage.New(endpoint, os.Getenv("DRAAS_S3_TEST_ACCESS"), os.Getenv("DRAAS_S3_TEST_SECRET"), "draas-backups", false)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "contract-tests/" + catalog.ID()
	key := prefix + "/conditional"
	created, err := s.PutIfAbsent(ctx, key, []byte("original"))
	if err != nil || !created {
		t.Fatal("create", created, err)
	}
	created, err = s.PutIfAbsent(ctx, key, []byte("replacement"))
	if err != nil || created {
		t.Fatal("conditional put did not prevent overwrite", created, err)
	}
	got, err := s.Get(ctx, key, 1024)
	if err != nil || string(got) != "original" {
		t.Fatal("original overwritten", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	rand.Read(master)
	e := &backup.Engine{Store: s, Master: master, Signer: priv, Trusted: pub}
	tenant := "contract-" + catalog.ID()
	data := bytes.Repeat([]byte("isolated corruption contract"), 1000)
	m, _, err := e.Ingest(ctx, tenant, "point", "workload", "test", nil, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Restore(ctx, m, tenant, "point", io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	// This unique test tenant owns this chunk exclusively; no platform point is altered.
	chunkKey := "chunks/" + tenant + "/v1/" + m.Chunks[0].Hash
	original, err := s.Get(ctx, chunkKey, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.Clone(original)
	broken[len(broken)-1] ^= 0xff
	if _, err = s.Client.PutObject(ctx, s.Bucket, chunkKey, bytes.NewReader(broken), int64(len(broken)), minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Restore(ctx, m, tenant, "point", io.Discard, nil); err == nil {
		t.Fatal("corrupt S3 data accepted")
	}
	for _, k := range []string{key, chunkKey, backup.ManifestKey(tenant, "point")} {
		if err = s.Client.RemoveObject(ctx, s.Bucket, k, minio.RemoveObjectOptions{}); err != nil {
			t.Fatal("test-owned object cleanup", err)
		}
	}
	t.Log("S3 conditional-create, ciphertext corruption rejection and isolated object cleanup: PASS")
}
