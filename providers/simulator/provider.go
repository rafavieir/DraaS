package simulator

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	source "github.com/draas-platform/draas/connectors/simulator"
	"github.com/draas-platform/draas/pkg/contracts"
	"io"
)

type Provider struct{}

func (Provider) Name() string { return "simulator" }
func (Provider) Validate(ctx context.Context, r io.Reader) (contracts.Validation, error) {
	v := contracts.Validation{Provider: "simulator", Simulation: true, Isolated: true, Cleanup: true, Detail: "In-process synthetic image validation; no guest boot or production networking."}
	if err := ctx.Err(); err != nil {
		return v, err
	}
	header := make([]byte, 1<<20)
	if _, err := io.ReadFull(r, header); err != nil {
		return v, err
	}
	if string(header[:8]) != source.Magic {
		return v, errors.New("invalid simulated disk format")
	}
	rev := binary.LittleEndian.Uint64(header[8:16])
	v.FileValid = rev > 0 && bytes.Contains(header, []byte(fmt.Sprintf("file=revision-%d;", rev)))
	v.ServiceValid = bytes.Contains(header, []byte("service=ready;"))
	if !v.FileValid || !v.ServiceValid {
		return v, errors.New("simulated application validation failed")
	}
	return v, nil
}
