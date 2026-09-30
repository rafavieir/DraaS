package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
)

type memoryStore struct {
	mu        sync.Mutex
	objects   map[string][]byte
	failAfter int
	writes    int
}

func (s *memoryStore) PutIfAbsent(ctx context.Context, k string, b []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.writes++
	if s.failAfter > 0 && s.writes > s.failAfter {
		return false, errors.New("injected storage outage")
	}
	if _, ok := s.objects[k]; ok {
		return false, nil
	}
	s.objects[k] = bytes.Clone(b)
	return true, nil
}
func (s *memoryStore) Get(ctx context.Context, k string, max int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, ok := s.objects[k]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(b)) > max {
		return nil, errors.New("too large")
	}
	return bytes.Clone(b), nil
}
func (s *memoryStore) List(ctx context.Context, prefix string) ([]string, error) {
	return nil, ctx.Err()
}
func fixture(t testing.TB) (*Engine, *memoryStore) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	s := &memoryStore{objects: map[string][]byte{}}
	return &Engine{Store: s, Master: key, Signer: priv, Trusted: pub}, s
}
func TestRoundTripDedupAndTenantIsolation(t *testing.T) {
	e, s := fixture(t)
	ctx := context.Background()
	data := make([]byte, 3*ChunkSize+127)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	m, stats, err := e.Ingest(ctx, "tenant-a", "point1", "workload", "test", nil, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 4 {
		t.Fatal(stats)
	}
	var out bytes.Buffer
	if _, err = e.Restore(ctx, m, "tenant-a", "point1", &out, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("restore bytes differ")
	}
	data[ChunkSize+19] ^= 0xff
	m2, s2, err := e.Ingest(ctx, "tenant-a", "point2", "workload", "test", nil, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Uploaded != 1 || s2.Reused != 3 {
		t.Fatal("dedup mismatch", s2)
	}
	out.Reset()
	if _, err = e.Restore(ctx, m2, "tenant-a", "point2", &out, nil); err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("incremental restore failed", err)
	}
	if _, err = e.Load(ctx, "tenant-b", "point1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant isolation", err)
	}
	if _, err = e.Restore(ctx, m, "tenant-b", "point1", io.Discard, nil); err == nil {
		t.Fatal("cross-tenant restore accepted")
	}
	if _, _, err = e.Ingest(ctx, "tenant-b", "point3", "workload", "test", nil, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(s.objects[chunkKey("tenant-a", "v1", m.Chunks[0].Hash)], s.objects[chunkKey("tenant-b", "v1", m.Chunks[0].Hash)]) {
		t.Fatal("ciphertexts must be isolated")
	}
}
func TestCorruptMissingAndForgedManifest(t *testing.T) {
	for _, scenario := range []string{"corrupt", "missing", "signature", "merkle", "offset", "wrong-key"} {
		t.Run(scenario, func(t *testing.T) {
			e, s := fixture(t)
			ctx := context.Background()
			m, _, err := e.Ingest(ctx, "tenant", "point", "workload", "test", nil, bytes.NewReader(bytes.Repeat([]byte("x"), ChunkSize)), nil)
			if err != nil {
				t.Fatal(err)
			}
			key := chunkKey("tenant", "v1", m.Chunks[0].Hash)
			switch scenario {
			case "corrupt":
				s.objects[key][20] ^= 0xff
			case "missing":
				delete(s.objects, key)
			case "signature":
				m.Signature[0] ^= 0xff
			case "merkle":
				m.MerkleRoot = "invalid"
			case "offset":
				m.Chunks[0].Offset = 1
				b, _ := signingBytes(m)
				m.Signature = ed25519.Sign(e.Signer, b)
			case "wrong-key":
				e.Master = bytes.Repeat([]byte{9}, 32)
			}
			if _, err = e.Restore(ctx, m, "tenant", "point", io.Discard, nil); err == nil {
				t.Fatal("invalid data accepted")
			}
		})
	}
}
func TestReplayAfterStorageOutage(t *testing.T) {
	e, s := fixture(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("abcdefgh"), ChunkSize)
	s.failAfter = 2
	if _, _, err := e.Ingest(ctx, "t", "p", "w", "test", nil, bytes.NewReader(data), nil); err == nil {
		t.Fatal("expected outage")
	}
	if _, ok := s.objects[ManifestKey("t", "p")]; ok {
		t.Fatal("partial manifest published")
	}
	s.failAfter = 0
	m, stats, err := e.Ingest(ctx, "t", "p", "w", "test", nil, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reused == 0 {
		t.Fatal("durable chunks not reused")
	}
	if _, err = e.Restore(ctx, m, "t", "p", io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	_, again, err := e.Ingest(ctx, "t", "p", "w", "test", nil, bytes.NewReader(data), nil)
	if err != nil || again.Uploaded != 0 {
		t.Fatal("replay not idempotent", err)
	}
}
func TestCancellationAndPathValidation(t *testing.T) {
	e, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.Ingest(ctx, "t", "p", "w", "test", nil, bytes.NewReader([]byte("x")), nil); err == nil {
		t.Fatal("cancel ignored")
	}
	for _, id := range []string{"../x", "a/b", "", "tenant.dot", "a\\b"} {
		if ValidID(id) {
			t.Fatalf("unsafe id %q", id)
		}
	}
}
func TestManifestIsSelfContained(t *testing.T) {
	e, s := fixture(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("independent-catalog"), 1000)
	m, _, err := e.Ingest(ctx, "t", "p", "w", "test", map[string]string{"disk": "synthetic"}, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := &Engine{Store: s, Master: e.Master, Trusted: e.Trusted}
	loaded, err := fresh.Load(ctx, "t", "p")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.StreamHash != m.StreamHash {
		t.Fatal("manifest mismatch")
	}
	var out bytes.Buffer
	if _, err = fresh.Restore(ctx, loaded, "t", "p", &out, nil); err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("catalog-independent restore failed", err)
	}
}
func TestReportSignatureSurvivesJSONRoundTrip(t *testing.T) {
	e, _ := fixture(t)
	report := map[string]any{"stats": Stats{Bytes: 1234, Seconds: 0.123}, "nested": map[string]any{"z": true, "a": "value"}}
	signature, err := e.Sign(report)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(report)
	var decoded any
	if err = json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if !VerifySigned(decoded, signature, e.Trusted) {
		t.Fatal("signature changed after JSON round trip")
	}
	decoded.(map[string]any)["nested"] = false
	if VerifySigned(decoded, signature, e.Trusted) {
		t.Fatal("tampered report accepted")
	}
}

func FuzzManifestValidation(f *testing.F) {
	f.Add("tenant", "point", int64(0))
	f.Add("../unsafe", "p", int64(-1))
	e, _ := fixture(f)
	f.Fuzz(func(t *testing.T, tenant, id string, size int64) {
		m := Manifest{Tenant: tenant, ID: id, Size: size}
		if err := e.ValidateManifest(m, tenant, id); err == nil {
			t.Fatal("unsigned manifest accepted")
		}
	})
}
func BenchmarkIngest(b *testing.B) {
	e, s := fixture(b)
	data := make([]byte, 4*ChunkSize)
	rand.Read(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		s.objects = map[string][]byte{}
		if _, _, err := e.Ingest(context.Background(), "t", "p", "w", "test", nil, bytes.NewReader(data), nil); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkRestore(b *testing.B) {
	e, _ := fixture(b)
	data := make([]byte, 4*ChunkSize)
	rand.Read(data)
	m, _, err := e.Ingest(context.Background(), "t", "p", "w", "test", nil, bytes.NewReader(data), nil)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err = e.Restore(context.Background(), m, "t", "p", io.Discard, nil); err != nil {
			b.Fatal(err)
		}
	}
}
