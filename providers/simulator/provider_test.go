package simulator

import (
	"context"
	"github.com/draas-platform/draas/connectors/simulator"
	"github.com/draas-platform/draas/pkg/contracts"
	"strings"
	"testing"
)

func TestContract(t *testing.T) {
	var p contracts.RecoveryProvider = Provider{}
	var s contracts.SourceConnector = simulator.Source{}
	ctx := context.Background()
	w, err := s.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.PrepareSnapshot(ctx, w[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Reader.Close()
	v, err := p.Validate(ctx, snap.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Simulation || v.BootVerified || !v.FileValid || !v.ServiceValid || !v.Cleanup || !v.Isolated {
		t.Fatal("provider truthfulness contract", v)
	}
	if _, err = p.Validate(ctx, strings.NewReader("not an image")); err == nil {
		t.Fatal("invalid image accepted")
	}
	if err = s.FinalizeSnapshot(ctx, w[0]); err != nil {
		t.Fatal(err)
	}
}
