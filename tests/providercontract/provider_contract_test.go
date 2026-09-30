package providercontract

import (
	"context"
	"testing"

	"github.com/draas-platform/draas/pkg/contracts"
	libvirtlab "github.com/draas-platform/draas/providers/libvirtlab"
	simulator "github.com/draas-platform/draas/providers/simulator"
)

func assertContractV2(t *testing.T, provider contracts.RecoveryProviderV2) {
	t.Helper()
	ctx := context.Background()
	if provider.Name() == "" {
		t.Fatal("provider name is empty")
	}
	caps, err := provider.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.ContractVersion != contracts.RecoveryProviderContractV2 {
		t.Fatalf("contract version=%q, want %q", caps.ContractVersion, contracts.RecoveryProviderContractV2)
	}
	if err = provider.ValidateCredentials(ctx); err != nil && provider.Name() == "simulator" {
		t.Fatalf("simulator credentials should validate: %v", err)
	}
	_, err = provider.GetResource(ctx, contracts.ResourceHandle{})
	if err == nil {
		t.Fatal("empty resource handle was accepted")
	}
}

func TestSimulatorProviderContractV2(t *testing.T) {
	assertContractV2(t, simulator.Provider{})
}

func TestLibvirtLabProviderContractV2Shape(t *testing.T) {
	p := (&libvirtlab.Provider{}).ContractV2()
	ctx := context.Background()
	caps, err := p.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.ContractVersion != contracts.RecoveryProviderContractV2 {
		t.Fatalf("contract version=%q", caps.ContractVersion)
	}
	if !caps.SupportsReboot || !caps.SupportsForceStop || !caps.SupportsGuestAgent || !caps.SupportsConsole {
		t.Fatalf("libvirt lab power/guest/console capabilities not declared: %+v", caps)
	}
	_, err = p.GetResource(ctx, contracts.ResourceHandle{})
	if err == nil {
		t.Fatal("empty libvirt handle was accepted")
	}
}
