package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const ChunkSize = 1 << 20
const MaxManifestSize = 32 << 20

var ErrNotFound = errors.New("object not found")

type Store interface {
	PutIfAbsent(context.Context, string, []byte) (bool, error)
	Get(context.Context, string, int64) ([]byte, error)
	List(context.Context, string) ([]string, error)
}
type Chunk struct {
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Size   int    `json:"size"`
}
type Manifest struct {
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	Tenant        string            `json:"tenant"`
	Workload      string            `json:"workload"`
	CreatedAt     time.Time         `json:"created_at"`
	Source        string            `json:"source"`
	Metadata      map[string]string `json:"metadata"`
	KeyVersion    string            `json:"key_version"`
	HashAlgorithm string            `json:"hash_algorithm"`
	Compression   string            `json:"compression"`
	Encryption    string            `json:"encryption"`
	Size          int64             `json:"size"`
	StreamHash    string            `json:"stream_hash"`
	Chunks        []Chunk           `json:"chunks"`
	MerkleRoot    string            `json:"merkle_root"`
	Signature     []byte            `json:"signature"`
}
type Stats struct {
	Bytes    int64   `json:"bytes"`
	Uploaded int     `json:"uploaded_chunks"`
	Reused   int     `json:"reused_chunks"`
	Seconds  float64 `json:"seconds"`
}
type Engine struct {
	Store   Store
	Master  []byte
	Signer  ed25519.PrivateKey
	Trusted ed25519.PublicKey
}

func ValidID(s string) bool {
	if len(s) < 1 || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func Sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func ManifestKey(tenant, id string) string { return "manifests/" + tenant + "/" + id + ".json" }
func chunkKey(tenant, version, hash string) string {
	return "chunks/" + tenant + "/" + version + "/" + hash
}
func (e *Engine) aead(tenant, version string) (cipher.AEAD, error) {
	if len(e.Master) != 32 || !ValidID(tenant) || version != "v1" {
		return nil, errors.New("invalid key configuration")
	}
	k, err := hkdf.Key(sha256.New, e.Master, nil, "draas/chunk/"+tenant+"/"+version, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func aad(tenant, version, hash string) []byte { return []byte(tenant + "/" + version + "/" + hash) }
func Merkle(chunks []Chunk) string {
	if len(chunks) == 0 {
		return Sum([]byte{0})
	}
	level := make([][]byte, len(chunks))
	for i, c := range chunks {
		h := sha256.Sum256(append([]byte{0}, []byte(c.Hash)...))
		level[i] = h[:]
	}
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			j := i + 1
			if j == len(level) {
				j = i
			}
			b := append([]byte{1}, level[i]...)
			b = append(b, level[j]...)
			h := sha256.Sum256(b)
			next = append(next, h[:])
		}
		level = next
	}
	return hex.EncodeToString(level[0])
}
func signingBytes(m Manifest) ([]byte, error) { m.Signature = nil; return json.Marshal(m) }
func (e *Engine) ValidateManifest(m Manifest, tenant, id string) error {
	if m.Version != 1 || m.Tenant != tenant || m.ID != id || !ValidID(m.Workload) || !ValidID(tenant) || !ValidID(id) || m.KeyVersion != "v1" || m.HashAlgorithm != "sha256" || m.Compression != "zstd" || m.Encryption != "aes-256-gcm" {
		return errors.New("manifest identity or format invalid")
	}
	if len(e.Trusted) != ed25519.PublicKeySize {
		return errors.New("trusted signing key not configured")
	}
	b, err := signingBytes(m)
	if err != nil {
		return err
	}
	if !ed25519.Verify(e.Trusted, b, m.Signature) {
		return errors.New("manifest signature invalid")
	}
	var size int64
	for _, c := range m.Chunks {
		if c.Offset != size || c.Size < 1 || c.Size > ChunkSize || !validHash(c.Hash) {
			return errors.New("invalid chunk map")
		}
		size += int64(c.Size)
	}
	if size != m.Size || !validHash(m.StreamHash) || m.MerkleRoot != Merkle(m.Chunks) {
		return errors.New("manifest size or Merkle root invalid")
	}
	return nil
}
func (e *Engine) Load(ctx context.Context, tenant, id string) (Manifest, error) {
	var m Manifest
	if !ValidID(tenant) || !ValidID(id) {
		return m, errors.New("invalid identity")
	}
	b, err := e.Store.Get(ctx, ManifestKey(tenant, id), MaxManifestSize)
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, e.ValidateManifest(m, tenant, id)
}

// Ingest is bounded by a chunk buffer plus the block map. Each object is conditionally created.
// Replaying an interrupted job rescans the immutable snapshot and reuses durable chunks.
func (e *Engine) Ingest(ctx context.Context, tenant, id, workload, source string, metadata map[string]string, r io.Reader, progress func(int64)) (Manifest, Stats, error) {
	start := time.Now()
	var stats Stats
	var m Manifest
	if !ValidID(tenant) || !ValidID(id) || !ValidID(workload) {
		return m, stats, errors.New("invalid identity")
	}
	if old, err := e.Load(ctx, tenant, id); err == nil {
		if old.Workload != workload {
			return m, stats, errors.New("recovery point identity conflict")
		}
		return old, Stats{Bytes: old.Size, Reused: len(old.Chunks), Seconds: time.Since(start).Seconds()}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return m, stats, err
	}
	if len(e.Signer) != ed25519.PrivateKeySize {
		return m, stats, errors.New("signing key unavailable")
	}
	a, err := e.aead(tenant, "v1")
	if err != nil {
		return m, stats, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return m, stats, err
	}
	defer enc.Close()
	m = Manifest{Version: 1, ID: id, Tenant: tenant, Workload: workload, Source: source, Metadata: metadata, CreatedAt: time.Now().UTC(), KeyVersion: "v1", HashAlgorithm: "sha256", Compression: "zstd", Encryption: "aes-256-gcm", Chunks: []Chunk{}}
	whole := sha256.New()
	buf := make([]byte, ChunkSize)
	for {
		if err = ctx.Err(); err != nil {
			return m, stats, err
		}
		n, readErr := io.ReadFull(r, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return m, stats, fmt.Errorf("read source: %w", readErr)
		}
		if n == 0 {
			break
		}
		raw := buf[:n]
		hash := Sum(raw)
		whole.Write(raw)
		nonce := make([]byte, a.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return m, stats, err
		}
		compressed := enc.EncodeAll(raw, nil)
		sealed := a.Seal(nonce, nonce, compressed, aad(tenant, "v1", hash))
		created, err := e.Store.PutIfAbsent(ctx, chunkKey(tenant, "v1", hash), sealed)
		if err != nil {
			return m, stats, fmt.Errorf("store chunk: %w", err)
		}
		if created {
			stats.Uploaded++
		} else {
			stats.Reused++
		}
		m.Chunks = append(m.Chunks, Chunk{Hash: hash, Offset: m.Size, Size: n})
		m.Size += int64(n)
		if progress != nil {
			progress(m.Size)
		}
		if len(m.Chunks) > 200000 {
			return m, stats, errors.New("lab manifest chunk limit exceeded")
		}
		if readErr != nil {
			break
		}
	}
	m.StreamHash = hex.EncodeToString(whole.Sum(nil))
	m.MerkleRoot = Merkle(m.Chunks)
	b, err := signingBytes(m)
	if err != nil {
		return m, stats, err
	}
	m.Signature = ed25519.Sign(e.Signer, b)
	b, err = json.Marshal(m)
	if err != nil {
		return m, stats, err
	}
	if len(b) > MaxManifestSize {
		return m, stats, errors.New("manifest exceeds supported size")
	}
	created, err := e.Store.PutIfAbsent(ctx, ManifestKey(tenant, id), b)
	if err != nil {
		return m, stats, err
	}
	if !created {
		m, err = e.Load(ctx, tenant, id)
	}
	stats.Bytes = m.Size
	stats.Seconds = time.Since(start).Seconds()
	return m, stats, err
}
func (e *Engine) Restore(ctx context.Context, m Manifest, tenant, id string, w io.Writer, progress func(int64)) (Stats, error) {
	start := time.Now()
	var s Stats
	if err := e.ValidateManifest(m, tenant, id); err != nil {
		return s, err
	}
	a, err := e.aead(tenant, m.KeyVersion)
	if err != nil {
		return s, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderMaxWindow(8<<20))
	if err != nil {
		return s, err
	}
	defer dec.Close()
	whole := sha256.New()
	for _, c := range m.Chunks {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		b, err := e.Store.Get(ctx, chunkKey(tenant, m.KeyVersion, c.Hash), 2*ChunkSize)
		if err != nil {
			return s, fmt.Errorf("fetch chunk %s: %w", c.Hash, err)
		}
		if len(b) < a.NonceSize() {
			return s, errors.New("truncated ciphertext")
		}
		compressed, err := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], aad(tenant, m.KeyVersion, c.Hash))
		if err != nil {
			return s, fmt.Errorf("chunk authentication: %w", err)
		}
		raw, err := dec.DecodeAll(compressed, make([]byte, 0, c.Size))
		if err != nil {
			return s, fmt.Errorf("decompress: %w", err)
		}
		if len(raw) != c.Size || Sum(raw) != c.Hash {
			return s, errors.New("chunk integrity mismatch")
		}
		n, err := io.MultiWriter(w, whole).Write(raw)
		if err != nil {
			return s, err
		}
		if n != len(raw) {
			return s, io.ErrShortWrite
		}
		s.Bytes += int64(n)
		if progress != nil {
			progress(s.Bytes)
		}
	}
	if hex.EncodeToString(whole.Sum(nil)) != m.StreamHash {
		return s, errors.New("restored stream hash mismatch")
	}
	s.Seconds = time.Since(start).Seconds()
	return s, nil
}
func (e *Engine) Verify(ctx context.Context, tenant, id string) (Manifest, Stats, error) {
	m, err := e.Load(ctx, tenant, id)
	if err != nil {
		return m, Stats{}, err
	}
	s, err := e.Restore(ctx, m, tenant, id, io.Discard, nil)
	return m, s, err
}
func (e *Engine) Sign(v any) ([]byte, error) {
	if len(e.Signer) != ed25519.PrivateKeySize {
		return nil, errors.New("signing key unavailable")
	}
	b, err := canonicalJSON(v)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(e.Signer, b), nil
}
func VerifySigned(v any, sig []byte, key ed25519.PublicKey) bool {
	b, err := canonicalJSON(v)
	return err == nil && len(key) == ed25519.PublicKeySize && ed25519.Verify(key, b, sig)
}

// Normalize struct/map field ordering before signing. Reports only contain finite
// numbers within float64's exact integer range. This is a versioned lab encoding,
// not a claim of general RFC 8785 support for arbitrary documents.
func canonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
