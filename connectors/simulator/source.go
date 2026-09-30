package simulator

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/draas-platform/draas/pkg/contracts"
	"io"
)

// The format is explicitly synthetic, not a bootable disk image.
const Magic = "DRSIM001"

type Source struct{}

func (Source) Capabilities() contracts.Capabilities {
	return contracts.Capabilities{Snapshot: true, ApplicationConsistent: true}
}
func (Source) Discover(ctx context.Context) ([]contracts.Workload, error) {
	return []contracts.Workload{{ID: "linux-demo", Name: "Linux application simulator", Source: "simulator", Revision: 1, Size: 16 << 20}}, ctx.Err()
}
func (Source) PrepareSnapshot(ctx context.Context, w contracts.Workload) (contracts.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Snapshot{}, err
	}
	if w.Size < 1<<20 || w.Size > 256<<20 || w.Revision < 1 {
		return contracts.Snapshot{}, errors.New("invalid simulator size or revision")
	}
	header := make([]byte, 1<<20)
	copy(header, Magic)
	binary.LittleEndian.PutUint64(header[8:], uint64(w.Revision))
	copy(header[16:], fmt.Sprintf("service=ready;file=revision-%d;workload=%s;", w.Revision, w.ID))
	// Stable payload chunks are repeated; only the first chunk changes between revisions.
	return contracts.Snapshot{Reader: io.NopCloser(io.MultiReader(bytes.NewReader(header), io.LimitReader(patternReader{}, w.Size-int64(len(header))))), Size: w.Size, Metadata: map[string]string{"format": "drsim-v1", "consistency": "application-simulator", "revision": fmt.Sprint(w.Revision), "boot_type": "none", "os": "synthetic-linux"}}, nil
}
func (Source) FinalizeSnapshot(ctx context.Context, _ contracts.Workload) error { return ctx.Err() }

type patternReader struct{}

func (patternReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i % 251)
	}
	return len(p), nil
}
