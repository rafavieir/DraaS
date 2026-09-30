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
func (Provider) Capabilities(context.Context) (contracts.RecoveryProviderCapabilities, error) {
	return contracts.RecoveryProviderCapabilities{ContractVersion: contracts.RecoveryProviderContractV2}, nil
}
func (Provider) Health(context.Context) (map[string]string, error) {
	return map[string]string{"provider": "simulator", "qualified": "SIMULATION_ONLY"}, nil
}
func (Provider) ValidateCredentials(context.Context) error { return nil }
func (Provider) Provision(context.Context, string, contracts.WorkloadSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("simulator provider does not provision VMs")
}
func (Provider) GetResource(context.Context, contracts.ResourceHandle) (contracts.ProviderResource, error) {
	return contracts.ProviderResource{}, errors.New("simulator provider does not manage persistent resources")
}
func (Provider) CreateNetwork(context.Context, string, contracts.NetworkSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("simulator provider does not create networks")
}
func (Provider) DeleteNetwork(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not delete networks")
}
func (Provider) AttachDisk(context.Context, string, contracts.ResourceHandle, contracts.DiskSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("simulator provider does not attach disks")
}
func (Provider) ConfigureNetwork(context.Context, string, contracts.ResourceHandle, contracts.NetworkSpec) error {
	return errors.New("simulator provider does not configure networks")
}
func (Provider) Start(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not power VMs")
}
func (Provider) Shutdown(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not power VMs")
}
func (Provider) Reboot(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not power VMs")
}
func (Provider) ForceStop(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not power VMs")
}
func (Provider) GetPowerState(context.Context, contracts.ResourceHandle) (contracts.PowerState, error) {
	return contracts.PowerState{}, errors.New("simulator provider does not power VMs")
}
func (Provider) GetGuestState(context.Context, contracts.ResourceHandle) (contracts.GuestState, error) {
	return contracts.GuestState{}, errors.New("simulator provider does not expose guest state")
}
func (Provider) OpenConsole(context.Context, contracts.ResourceHandle) (contracts.ConsoleSession, error) {
	return contracts.ConsoleSession{}, errors.New("simulator provider does not expose console")
}
func (Provider) Delete(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("simulator provider does not delete persistent resources")
}
func (Provider) ListManagedResources(context.Context) ([]contracts.ProviderResource, error) {
	return nil, nil
}

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
