// Package libvirtlab contains all temporary laboratory hypervisor specifics.
// It operates only domains and files that carry the draas-lab ownership prefix.
package libvirtlab

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/failpoint"
	"github.com/draas-platform/draas/pkg/contracts"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ImageURL = "https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/cloud/generic_alpine-3.24.1-x86_64-bios-cloudinit-r0.qcow2"

type Provider struct {
	Root          string
	FixtureBinary string
}
type VM struct {
	ID            string    `json:"id"`
	Tenant        string    `json:"tenant"`
	RunID         string    `json:"run_id"`
	RecoveryJobID string    `json:"recovery_job_id,omitempty"`
	Disk          string    `json:"disk"`
	HTTPPort      int       `json:"http_port"`
	SSHPort       int       `json:"ssh_port"`
	Isolated      bool      `json:"isolated"`
	CreatedAt     time.Time `json:"created_at"`
	Lifetime      string    `json:"lifetime"`
	Operation     string    `json:"operation_id,omitempty"`
}
type Guest struct {
	Healthy     bool     `json:"healthy"`
	Database    string   `json:"database"`
	MarkerHash  string   `json:"marker_sha256"`
	MarkerBytes int      `json:"marker_bytes"`
	BootID      string   `json:"boot_id"`
	OS          string   `json:"os_release"`
	Addresses   []string `json:"addresses"`
}

func (p *Provider) Name() string { return "libvirt-lab" }

func (p *Provider) Capabilities(context.Context) (contracts.RecoveryProviderCapabilities, error) {
	return contracts.RecoveryProviderCapabilities{
		ContractVersion:       contracts.RecoveryProviderContractV2,
		SupportsConsole:       true,
		SupportsHotAttach:     false,
		SupportsNetworkCreate: false,
		SupportsGuestAgent:    true,
		SupportsReboot:        true,
		SupportsForceStop:     true,
		SupportsTags:          true,
		SupportsAsyncTasks:    false,
	}, nil
}

func (p *Provider) ValidateCredentials(ctx context.Context) error {
	_, err := p.Health(ctx)
	return err
}

func handleFromVM(v VM) contracts.ResourceHandle {
	return contracts.ResourceHandle{ProviderResourceID: v.ID, ProviderCluster: "libvirt-qemu-system", Metadata: map[string]string{
		"managed_by":      "draas",
		"tenant":          v.Tenant,
		"run_id":          v.RunID,
		"recovery_job_id": v.RecoveryJobID,
		"disk":            v.Disk,
		"http_port":       strconv.Itoa(v.HTTPPort),
		"ssh_port":        strconv.Itoa(v.SSHPort),
		"isolated":        strconv.FormatBool(v.Isolated),
		"lifetime":        v.Lifetime,
		"operation_id":    v.Operation,
	}}
}

func vmFromHandle(h contracts.ResourceHandle) (VM, error) {
	if !safeVM(h.ProviderResourceID) {
		return VM{}, errors.New("invalid libvirt resource handle")
	}
	m := h.Metadata
	if m == nil {
		m = map[string]string{}
	}
	httpPort, _ := strconv.Atoi(m["http_port"])
	sshPort, _ := strconv.Atoi(m["ssh_port"])
	isolated, _ := strconv.ParseBool(m["isolated"])
	return VM{
		ID:            h.ProviderResourceID,
		Tenant:        m["tenant"],
		RunID:         m["run_id"],
		RecoveryJobID: m["recovery_job_id"],
		Disk:          m["disk"],
		HTTPPort:      httpPort,
		SSHPort:       sshPort,
		Isolated:      isolated,
		Lifetime:      m["lifetime"],
		Operation:     m["operation_id"],
	}, nil
}

func (p *Provider) Provision(context.Context, string, contracts.WorkloadSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("libvirt lab provision is orchestrated by the recovery engine restore flow")
}

func (p *Provider) GetResource(ctx context.Context, h contracts.ResourceHandle) (contracts.ProviderResource, error) {
	v, err := vmFromHandle(h)
	if err != nil {
		return contracts.ProviderResource{}, err
	}
	state, err := p.State(ctx, v)
	if err != nil {
		return contracts.ProviderResource{}, err
	}
	return contracts.ProviderResource{Handle: h, Kind: "vm", State: state, ManagedBy: "draas", TenantID: v.Tenant}, nil
}

func (p *Provider) CreateNetwork(context.Context, string, contracts.NetworkSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("libvirt lab uses per-VM isolated user networking and does not create external networks")
}

func (p *Provider) DeleteNetwork(context.Context, string, contracts.ResourceHandle) error {
	return errors.New("libvirt lab uses per-VM isolated user networking and does not delete external networks")
}

func (p *Provider) AttachDisk(context.Context, string, contracts.ResourceHandle, contracts.DiskSpec) (contracts.ResourceHandle, error) {
	return contracts.ResourceHandle{}, errors.New("libvirt lab disk attachment is performed during VM definition")
}

func (p *Provider) ConfigureNetwork(context.Context, string, contracts.ResourceHandle, contracts.NetworkSpec) error {
	return errors.New("libvirt lab network is immutable after VM definition")
}

type V2Provider struct{ Provider *Provider }

func (p *Provider) ContractV2() contracts.RecoveryProviderV2 {
	return V2Provider{Provider: p}
}

func (v V2Provider) Name() string { return v.Provider.Name() }
func (v V2Provider) Capabilities(ctx context.Context) (contracts.RecoveryProviderCapabilities, error) {
	return v.Provider.Capabilities(ctx)
}
func (v V2Provider) Health(ctx context.Context) (map[string]string, error) {
	return v.Provider.Health(ctx)
}
func (v V2Provider) ValidateCredentials(ctx context.Context) error {
	return v.Provider.ValidateCredentials(ctx)
}
func (v V2Provider) Provision(ctx context.Context, operationID string, spec contracts.WorkloadSpec) (contracts.ResourceHandle, error) {
	return v.Provider.Provision(ctx, operationID, spec)
}
func (v V2Provider) GetResource(ctx context.Context, h contracts.ResourceHandle) (contracts.ProviderResource, error) {
	return v.Provider.GetResource(ctx, h)
}
func (v V2Provider) CreateNetwork(ctx context.Context, operationID string, spec contracts.NetworkSpec) (contracts.ResourceHandle, error) {
	return v.Provider.CreateNetwork(ctx, operationID, spec)
}
func (v V2Provider) DeleteNetwork(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	return v.Provider.DeleteNetwork(ctx, operationID, h)
}
func (v V2Provider) AttachDisk(ctx context.Context, operationID string, vm contracts.ResourceHandle, disk contracts.DiskSpec) (contracts.ResourceHandle, error) {
	return v.Provider.AttachDisk(ctx, operationID, vm, disk)
}
func (v V2Provider) ConfigureNetwork(ctx context.Context, operationID string, vm contracts.ResourceHandle, network contracts.NetworkSpec) error {
	return v.Provider.ConfigureNetwork(ctx, operationID, vm, network)
}
func (v V2Provider) Start(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	vm, err := vmFromHandle(h)
	if err != nil {
		return err
	}
	return v.Provider.Start(ctx, vm)
}
func (v V2Provider) Shutdown(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	vm, err := vmFromHandle(h)
	if err != nil {
		return err
	}
	return v.Provider.Stop(ctx, vm)
}
func (v V2Provider) Reboot(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	vm, err := vmFromHandle(h)
	if err != nil {
		return err
	}
	return v.Provider.Reboot(ctx, vm)
}
func (v V2Provider) ForceStop(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	vm, err := vmFromHandle(h)
	if err != nil {
		return err
	}
	return v.Provider.ForceStop(ctx, vm)
}
func (v V2Provider) GetPowerState(ctx context.Context, h contracts.ResourceHandle) (contracts.PowerState, error) {
	vm, err := vmFromHandle(h)
	if err != nil {
		return contracts.PowerState{}, err
	}
	state, err := v.Provider.State(ctx, vm)
	return contracts.PowerState{State: state}, err
}
func (v V2Provider) GetGuestState(ctx context.Context, h contracts.ResourceHandle) (contracts.GuestState, error) {
	vm, err := vmFromHandle(h)
	if err != nil {
		return contracts.GuestState{}, err
	}
	g, err := v.Provider.Guest(ctx, vm)
	if err != nil {
		return contracts.GuestState{}, err
	}
	return contracts.GuestState{Healthy: g.Healthy, BootID: g.BootID, Details: map[string]string{"database": g.Database, "marker_sha256": g.MarkerHash, "os_release": g.OS}}, nil
}
func (v V2Provider) OpenConsole(ctx context.Context, h contracts.ResourceHandle) (contracts.ConsoleSession, error) {
	vm, err := vmFromHandle(h)
	if err != nil {
		return contracts.ConsoleSession{}, err
	}
	if vm.SSHPort == 0 {
		return contracts.ConsoleSession{}, errors.New("console requires ssh port in provider handle metadata")
	}
	return contracts.ConsoleSession{Protocol: "ssh", URL: fmt.Sprintf("ssh://root@127.0.0.1:%d", vm.SSHPort)}, nil
}

func (v V2Provider) Command(ctx context.Context, h contracts.ResourceHandle, cmd string) (string, error) {
	vm, err := vmFromHandle(h)
	if err != nil {
		return "", err
	}
	return v.Provider.Command(ctx, vm, cmd)
}

func (v V2Provider) Delete(ctx context.Context, operationID string, h contracts.ResourceHandle) error {
	vm, err := vmFromHandle(h)
	if err != nil {
		return err
	}
	return v.Provider.Delete(ctx, vm)
}
func (v V2Provider) ListManagedResources(ctx context.Context) ([]contracts.ProviderResource, error) {
	rows, err := virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, err
	}
	var resources []contracts.ProviderResource
	for _, id := range strings.Fields(rows) {
		if !safeVM(id) {
			continue
		}
		vm, err := v.Provider.VMFromDomain(ctx, id)
		if err != nil {
			continue
		}
		state, _ := v.Provider.State(ctx, vm)
		resources = append(resources, contracts.ProviderResource{Handle: handleFromVM(vm), Kind: "vm", State: state, ManagedBy: "draas", TenantID: vm.Tenant})
	}
	return resources, nil
}

func (v V2Provider) FindResourceByOperation(ctx context.Context, operationID string) (contracts.ProviderResource, bool, error) {
	resources, err := v.ListManagedResources(ctx)
	if err != nil {
		return contracts.ProviderResource{}, false, err
	}
	for _, r := range resources {
		if r.Handle.Metadata["operation_id"] == operationID {
			return r, true, nil
		}
	}
	return contracts.ProviderResource{}, false, nil
}

func New(root, fixture string) (*Provider, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if root != "/var/lib/draas-lab" {
		return nil, errors.New("libvirt lab root must be /var/lib/draas-lab")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Provider{Root: root, FixtureBinary: fixture}, nil
}
func command(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s failed: %w: %s", name, err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}
func virsh(ctx context.Context, args ...string) (string, error) {
	return command(ctx, "virsh", append([]string{"-c", "qemu:///system"}, args...)...)
}
func (p *Provider) Health(ctx context.Context) (map[string]string, error) {
	v, err := virsh(ctx, "version")
	if err != nil {
		return nil, err
	}
	q, err := command(ctx, "qemu-system-x86_64", "--version")
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat("/dev/kvm"); err != nil {
		return nil, err
	}
	return map[string]string{"provider": "libvirt-kvm-lab", "libvirt": v, "qemu": q, "acceleration": "KVM", "qualified": "LAB_ONLY"}, nil
}
func (p *Provider) Dir(run string) (string, error) {
	if !backup.ValidID(run) {
		return "", errors.New("invalid run ID")
	}
	dir := filepath.Join(p.Root, run)
	return dir, os.MkdirAll(dir, 0700)
}
func safeVM(id string) bool     { return backup.ValidID(id) && strings.HasPrefix(id, "draas-lab-") }
func xmlEscape(s string) string { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func (p *Provider) EnsureImage(ctx context.Context) (string, string, error) {
	path := filepath.Join(p.Root, "alpine-base.qcow2")
	client := &http.Client{Timeout: 5 * time.Minute}
	get := func(url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		r, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return nil, fmt.Errorf("image HTTP %d", r.StatusCode)
		}
		return io.ReadAll(io.LimitReader(r.Body, 1024))
	}
	sumBytes, err := get(ImageURL + ".sha512")
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(string(sumBytes))
	if len(fields) == 0 {
		return "", "", errors.New("empty image checksum")
	}
	expected := fields[0]
	if len(expected) != 128 {
		return "", "", errors.New("invalid image checksum")
	}
	if _, err = os.Stat(path); os.IsNotExist(err) {
		req, err := http.NewRequestWithContext(ctx, "GET", ImageURL, nil)
		if err != nil {
			return "", "", err
		}
		r, err := client.Do(req)
		if err != nil {
			return "", "", err
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return "", "", fmt.Errorf("image HTTP %d", r.StatusCode)
		}
		f, err := os.OpenFile(path+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(f, io.LimitReader(r.Body, 512<<20))
		closeErr := f.Close()
		if copyErr != nil {
			return "", "", copyErr
		}
		if closeErr != nil {
			return "", "", closeErr
		}
		if err = os.Rename(path+".partial", path); err != nil {
			return "", "", err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	h := sha512.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", "", err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return "", "", errors.New("official cloud image checksum mismatch")
	}
	return path, actual, nil
}
func (p *Provider) PrepareSource(ctx context.Context, tenant, run string) (VM, string, error) {
	dir, err := p.Dir(run)
	if err != nil {
		return VM{}, "", err
	}
	base, checksum, err := p.EnsureImage(ctx)
	if err != nil {
		return VM{}, "", err
	}
	disk := filepath.Join(dir, "source.qcow2")
	if _, err = os.Stat(disk); err == nil {
		return VM{}, "", errors.New("source already exists; use a new run ID")
	}
	// This official image has a whole-disk ext4 filesystem. Grow it before
	// cloud-init parses the embedded fixture, which exceeds its initial free space.
	raw := filepath.Join(dir, "source-build.raw")
	if _, err = command(ctx, "qemu-img", "convert", "-f", "qcow2", "-O", "raw", base, raw); err != nil {
		return VM{}, "", err
	}
	if _, err = command(ctx, "qemu-img", "resize", "-f", "raw", raw, "2G"); err != nil {
		return VM{}, "", err
	}
	if _, err = command(ctx, "resize2fs", raw); err != nil {
		return VM{}, "", err
	}
	if _, err = command(ctx, "qemu-img", "convert", "-f", "raw", "-O", "qcow2", raw, disk); err != nil {
		return VM{}, "", err
	}
	if err = os.Remove(raw); err != nil {
		return VM{}, "", err
	}
	if _, err = command(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "guest-key")); err != nil {
		return VM{}, "", err
	}
	pub, err := os.ReadFile(filepath.Join(dir, "guest-key.pub"))
	if err != nil {
		return VM{}, "", err
	}
	fixture, err := os.ReadFile(p.FixtureBinary)
	if err != nil {
		return VM{}, "", err
	}
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)
	if err = os.WriteFile(filepath.Join(dir, "fixture-token"), []byte(token), 0600); err != nil {
		return VM{}, "", err
	}
	userdata := fmt.Sprintf(`#cloud-config
hostname: draas-linux
disable_root: false
ssh_pwauth: false
ssh_authorized_keys:
  - %s
users:
  - default
  - name: root
    lock_passwd: false
    ssh_authorized_keys:
      - %s
packages:
  - sqlite
  - curl
write_files:
  - path: /usr/local/bin/draas-fixture
    permissions: '0755'
    encoding: b64
    content: %s
  - path: /etc/draas-fixture-token
    permissions: '0600'
    content: %s
  - path: /etc/init.d/draas-fixture
    permissions: '0755'
    content: |
      #!/sbin/openrc-run
      name="DR fixture"
      command="/usr/local/bin/draas-fixture"
      command_background=true
      pidfile="/run/draas-fixture.pid"
      depend() { need net; }
runcmd:
  - rc-update add draas-fixture default
  - rc-service draas-fixture start
  - touch /etc/cloud/cloud-init.disabled
`, strings.TrimSpace(string(pub)), strings.TrimSpace(string(pub)), base64.StdEncoding.EncodeToString(fixture), token)
	if err = os.WriteFile(filepath.Join(dir, "user-data"), []byte(userdata), 0600); err != nil {
		return VM{}, "", err
	}
	metadata := "instance-id: " + run + "\nlocal-hostname: draas-linux\n"
	if err = os.WriteFile(filepath.Join(dir, "meta-data"), []byte(metadata), 0600); err != nil {
		return VM{}, "", err
	}
	seed := filepath.Join(dir, "seed.iso")
	if _, err = command(ctx, "cloud-localds", seed, filepath.Join(dir, "user-data"), filepath.Join(dir, "meta-data")); err != nil {
		return VM{}, "", err
	}
	vm, err := p.Create(ctx, tenant, run, "source", disk, seed, false)
	return vm, checksum, err
}
func zvolDiskPath(disk string) bool {
	clean := filepath.Clean(disk)
	return strings.HasPrefix(clean, "/dev/zvol/") && !strings.Contains(clean, "..") && !strings.ContainsAny(clean, " \t\n\r;&|`$<>")
}
func ownedDiskPath(disk, dir string) bool {
	clean := filepath.Clean(disk)
	return strings.HasPrefix(clean, dir+string(os.PathSeparator)) || zvolDiskPath(clean)
}

func (p *Provider) Create(ctx context.Context, tenant, run, suffix, disk, seed string, isolated bool) (VM, error) {
	return p.CreateWithOperation(ctx, tenant, run, run, suffix, disk, seed, isolated, "", "", "TEST")
}

func (p *Provider) CreateWithOperation(ctx context.Context, tenant, run, recoveryJobID, suffix, disk, seed string, isolated bool, operationID, recoveryPointID, environmentType string) (VM, error) {
	if !backup.ValidID(tenant) || !backup.ValidID(suffix) {
		return VM{}, errors.New("invalid resource identity")
	}
	dir, err := p.Dir(run)
	if err != nil {
		return VM{}, err
	}
	disk = filepath.Clean(disk)
	if !ownedDiskPath(disk, dir) {
		return VM{}, errors.New("disk outside owned run")
	}
	id := "draas-lab-" + run + "-" + suffix
	if !safeVM(id) {
		return VM{}, errors.New("invalid domain ID")
	}
	record := filepath.Join(dir, suffix+"-vm.json")
	if b, err := os.ReadFile(record); err == nil {
		var v VM
		if json.Unmarshal(b, &v) != nil || v.Tenant != tenant || v.Disk != disk {
			return v, errors.New("resource identity conflict")
		}
		if _, err = virsh(ctx, "dominfo", id); err == nil {
			return v, nil
		}
		return v, errors.New("resource record exists but domain absent; reconcile explicitly")
	}
	hp, err := freePort()
	if err != nil {
		return VM{}, err
	}
	sp, err := freePort()
	if err != nil {
		return VM{}, err
	}
	if environmentType == "" {
		environmentType = "TEST"
	}
	v := VM{ID: id, Tenant: tenant, RunID: run, RecoveryJobID: recoveryJobID, Disk: disk, HTTPPort: hp, SSHPort: sp, Isolated: isolated, CreatedAt: time.Now().UTC(), Lifetime: "TEMPORARY_TEST", Operation: operationID}
	restrict := "off"
	if isolated {
		restrict = "on"
	}
	cd := ""
	if seed != "" {
		cd = fmt.Sprintf("<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='%s'/><target dev='sda' bus='sata'/><readonly/></disk>", xmlEscape(seed))
	}
	body := fmt.Sprintf(`<domain type='kvm' xmlns:qemu='http://libvirt.org/schemas/domain/qemu/1.0'><name>%s</name><metadata><draas:resource xmlns:draas='https://draas.local/draas' managed_by='draas' tenant_id='%s' recovery_job_id='%s' run_id='%s' operation_id='%s' recovery_point_id='%s' environment_type='%s' lifetime='TEMPORARY_TEST' disk='%s' http_port='%d' ssh_port='%d' isolated='%t'/></metadata><memory unit='MiB'>512</memory><vcpu>1</vcpu><os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features><cpu mode='host-passthrough'/><on_poweroff>destroy</on_poweroff><on_reboot>restart</on_reboot><devices><emulator>/usr/bin/qemu-system-x86_64</emulator><disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='%s'/><target dev='vda' bus='virtio'/></disk>%s<serial type='file'><source path='%s'/><target port='0'/></serial><console type='file'><source path='%s'/><target type='serial' port='0'/></console><memballoon model='none'/></devices><qemu:commandline><qemu:arg value='-netdev'/><qemu:arg value='user,id=labnet,restrict=%s,hostfwd=tcp:127.0.0.1:%d-:8080,hostfwd=tcp:127.0.0.1:%d-:22'/><qemu:arg value='-device'/><qemu:arg value='virtio-net-pci,netdev=labnet,mac=52:54:00:44:52:01,addr=0x10'/></qemu:commandline></domain>`, id, tenant, recoveryJobID, run, operationID, recoveryPointID, environmentType, xmlEscape(disk), hp, sp, isolated, xmlEscape(disk), cd, xmlEscape(filepath.Join(dir, suffix+"-serial.log")), xmlEscape(filepath.Join(dir, suffix+"-serial.log")), restrict, hp, sp)
	xmlPath := filepath.Join(dir, suffix+".xml")
	if err = os.WriteFile(xmlPath, []byte(body), 0600); err != nil {
		return v, err
	}
	if _, err = virsh(ctx, "define", xmlPath); err != nil {
		return v, err
	}
	if err = failpoint.Trigger(ctx, "after_provider_call_before_persist"); err != nil {
		return v, err
	}
	if err = failpoint.Trigger(ctx, "after_vm_create"); err != nil {
		return v, err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err = os.WriteFile(record, b, 0600); err != nil {
		return v, err
	}
	return v, nil
}

func (p *Provider) VMFromDomain(ctx context.Context, id string) (VM, error) {
	if !safeVM(id) {
		return VM{}, errors.New("unmanaged domain")
	}
	definition, err := virsh(ctx, "dumpxml", id)
	if err != nil {
		return VM{}, err
	}
	var domain struct {
		Metadata struct {
			Resource struct {
				Managed     string `xml:"managed_by,attr"`
				Tenant      string `xml:"tenant_id,attr"`
				Run         string `xml:"recovery_job_id,attr"`
				RunID       string `xml:"run_id,attr"`
				Operation   string `xml:"operation_id,attr"`
				Lifetime    string `xml:"lifetime,attr"`
				Environment string `xml:"environment_type,attr"`
				Disk        string `xml:"disk,attr"`
				HTTPPort    int    `xml:"http_port,attr"`
				SSHPort     int    `xml:"ssh_port,attr"`
				Isolated    bool   `xml:"isolated,attr"`
			} `xml:"resource"`
		} `xml:"metadata"`
	}
	if xml.Unmarshal([]byte(definition), &domain) != nil || domain.Metadata.Resource.Managed != "draas" {
		return VM{}, errors.New("domain is not managed by draas")
	}
	runID := domain.Metadata.Resource.RunID
	if runID == "" {
		runID = domain.Metadata.Resource.Run
	}
	vm := VM{ID: id, Tenant: domain.Metadata.Resource.Tenant, RunID: runID, RecoveryJobID: domain.Metadata.Resource.Run, Disk: domain.Metadata.Resource.Disk, HTTPPort: domain.Metadata.Resource.HTTPPort, SSHPort: domain.Metadata.Resource.SSHPort, Isolated: domain.Metadata.Resource.Isolated, Lifetime: domain.Metadata.Resource.Lifetime, Operation: domain.Metadata.Resource.Operation}
	if vm.Lifetime == "" {
		vm.Lifetime = "TEMPORARY_TEST"
	}
	if backup.ValidID(vm.RunID) {
		dir, err := p.Dir(vm.RunID)
		if err == nil {
			suffix := strings.TrimPrefix(id, "draas-lab-"+vm.RunID+"-")
			record := filepath.Join(dir, suffix+"-vm.json")
			if b, err := os.ReadFile(record); err == nil {
				var recorded VM
				if json.Unmarshal(b, &recorded) == nil && recorded.ID == id {
					recorded.Operation = vm.Operation
					return recorded, nil
				}
			}
		}
	}
	return vm, nil
}
func (p *Provider) State(ctx context.Context, v VM) (string, error) {
	if !safeVM(v.ID) {
		return "", errors.New("unmanaged domain")
	}
	return virsh(ctx, "domstate", v.ID)
}
func (p *Provider) Start(ctx context.Context, v VM) error {
	s, err := p.State(ctx, v)
	if err != nil {
		return err
	}
	if s == "running" {
		return nil
	}
	_, err = virsh(ctx, "start", v.ID)
	return err
}
func (p *Provider) Stop(ctx context.Context, v VM) error {
	s, err := p.State(ctx, v)
	if err != nil {
		return err
	}
	if s == "shut off" {
		return nil
	}
	if _, err = virsh(ctx, "shutdown", v.ID); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			s, err = p.State(ctx, v)
			if err != nil {
				return err
			}
			if s == "shut off" {
				return nil
			}
		}
	}
}
func (p *Provider) ForceStop(ctx context.Context, v VM) error {
	s, err := p.State(ctx, v)
	if err != nil {
		return err
	}
	if s == "shut off" {
		return nil
	}
	_, err = virsh(ctx, "destroy", v.ID)
	return err
}
func (p *Provider) Reboot(ctx context.Context, v VM) error {
	s, err := p.State(ctx, v)
	if err != nil {
		return err
	}
	if s != "running" {
		return errors.New("domain must be running before reboot")
	}
	_, err = virsh(ctx, "reboot", v.ID)
	return err
}
func (p *Provider) Delete(ctx context.Context, v VM) error {
	if !safeVM(v.ID) || v.Lifetime == "ACTIVE_DR" {
		return errors.New("refuse unsafe resource deletion")
	}
	dir, err := p.Dir(v.RunID)
	if err != nil {
		return err
	}
	prefix := "draas-lab-" + v.RunID + "-"
	if !strings.HasPrefix(v.ID, prefix) || !ownedDiskPath(v.Disk, dir) {
		return errors.New("resource outside owned run")
	}
	suffix := strings.TrimPrefix(v.ID, prefix)
	if !backup.ValidID(suffix) {
		return errors.New("invalid resource suffix")
	}
	b, err := os.ReadFile(filepath.Join(dir, suffix+"-vm.json"))
	if err != nil {
		return err
	}
	var recorded VM
	if json.Unmarshal(b, &recorded) != nil || recorded.ID != v.ID || recorded.Tenant != v.Tenant || recorded.Disk != v.Disk || recorded.Lifetime != "TEMPORARY_TEST" {
		return errors.New("ownership or lifetime mismatch")
	}
	// A failed lookup is not proof of absence. Require a successful inventory.
	names, err := virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return err
	}
	exists := false
	for _, name := range strings.Fields(names) {
		if name == v.ID {
			exists = true
		}
	}
	if exists {
		definition, err := virsh(ctx, "dumpxml", v.ID)
		if err != nil {
			return err
		}
		var domain struct {
			Metadata struct {
				Resource struct {
					Managed  string `xml:"managed_by,attr"`
					Tenant   string `xml:"tenant_id,attr"`
					Run      string `xml:"recovery_job_id,attr"`
					Lifetime string `xml:"lifetime,attr"`
				} `xml:"resource"`
			} `xml:"metadata"`
		}
		if xml.Unmarshal([]byte(definition), &domain) != nil || domain.Metadata.Resource.Managed != "draas" || domain.Metadata.Resource.Tenant != v.Tenant || domain.Metadata.Resource.Run != v.RunID || domain.Metadata.Resource.Lifetime != "TEMPORARY_TEST" {
			return errors.New("domain ownership metadata mismatch")
		}
		s, err := p.State(ctx, v)
		if err != nil {
			return err
		}
		if s != "shut off" {
			if _, err = virsh(ctx, "destroy", v.ID); err != nil {
				return err
			}
		}
		if _, err = virsh(ctx, "undefine", v.ID); err != nil {
			return err
		}
	}
	if !ownedDiskPath(v.Disk, dir) {
		return errors.New("disk outside owned run")
	}
	if zvolDiskPath(v.Disk) {
		// ZFS lifecycle is managed by the block storage reconciler; deleting a VM must not unlink /dev/zvol devices.
	} else {
		if err = os.Remove(v.Disk); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err = os.Stat(v.Disk); !os.IsNotExist(err) {
			return errors.New("disk deletion not proven")
		}
	}
	names, err = virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(names) {
		if name == v.ID {
			return errors.New("domain still exists")
		}
	}
	return nil
}
func (p *Provider) Guest(ctx context.Context, v VM) (Guest, error) {
	var g Guest
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", v.HTTPPort), nil)
	if err != nil {
		return g, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return g, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return g, fmt.Errorf("guest health HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&g)
	return g, err
}
func (p *Provider) WaitGuest(ctx context.Context, v VM) (Guest, error) {
	for {
		g, err := p.Guest(ctx, v)
		if err == nil && g.Healthy && g.BootID != "" && strings.Contains(g.OS, "Alpine") {
			return g, nil
		}
		select {
		case <-ctx.Done():
			return Guest{}, fmt.Errorf("guest readiness: %w (last: %v)", ctx.Err(), err)
		case <-time.After(2 * time.Second):
		}
	}
}
func (p *Provider) Mutate(ctx context.Context, v VM) (Guest, error) {
	dir, err := p.Dir(v.RunID)
	if err != nil {
		return Guest{}, err
	}
	token, err := os.ReadFile(filepath.Join(dir, "fixture-token"))
	if err != nil {
		return Guest{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://127.0.0.1:%d/mutate", v.HTTPPort), nil)
	if err != nil {
		return Guest{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return Guest{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Guest{}, fmt.Errorf("mutation HTTP %d", res.StatusCode)
	}
	var g Guest
	err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&g)
	return g, err
}
func (p *Provider) Command(ctx context.Context, v VM, cmd string) (string, error) {
	if !safeVM(v.ID) || len(cmd) > 4096 {
		return "", errors.New("invalid console request")
	}
	dir, err := p.Dir(v.RunID)
	if err != nil {
		return "", err
	}
	return command(ctx, "ssh", "-i", filepath.Join(dir, "guest-key"), "-p", strconv.Itoa(v.SSHPort), "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=accept-new", "-o", "HostKeyAlias=draas-"+v.RunID, "-o", "UserKnownHostsFile="+filepath.Join(dir, "known_hosts"), "root@127.0.0.1", cmd)
}
func (p *Provider) Validate(ctx context.Context, v VM, expected Guest) (map[string]any, error) {
	g, err := p.WaitGuest(ctx, v)
	if err != nil {
		return nil, err
	}
	s, err := p.State(ctx, v)
	if err != nil {
		return nil, err
	}
	if s != "running" || g.MarkerHash != expected.MarkerHash || g.MarkerBytes != expected.MarkerBytes || g.Database != expected.Database || g.BootID == expected.BootID {
		return nil, errors.New("restored guest/data validation mismatch")
	}
	out, err := p.Command(ctx, v, "uname -s; cat /proc/sys/kernel/random/boot_id; sqlite3 /var/lib/draas-fixture/app.db 'PRAGMA integrity_check; SELECT version FROM app WHERE id=1;' ")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(out, "Linux") || !strings.Contains(out, g.BootID) {
		return nil, errors.New("independent guest command validation failed")
	}
	return map[string]any{"boot_verified": true, "service_verified": g.Healthy, "data_verified": true, "guest": g, "provider_state": s, "ssh_console_command": out, "network_mode": "ISOLATED_USERNET_RESTRICT"}, nil
}
func (p *Provider) CheckEgress(ctx context.Context, v VM) (string, error) {
	if !v.Isolated {
		return "", errors.New("VM was not created with restricted network")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "draas-isolation-canary") }), ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	defer server.Close()
	probe, err := (&http.Client{Timeout: time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return "", err
	}
	probe.Body.Close()
	if probe.StatusCode != http.StatusOK {
		return "", errors.New("host canary unavailable")
	}
	cmd := fmt.Sprintf("for endpoint in http://10.0.2.2:%d/ http://1.1.1.1/; do if curl --connect-timeout 2 --max-time 3 -s \"$endpoint\" >/dev/null; then echo EGRESS_ALLOWED; exit 1; else echo EGRESS_DENIED; fi; done", port)
	out, err := p.Command(ctx, v, cmd)
	if err != nil {
		return out, err
	}
	if strings.Count(out, "EGRESS_DENIED") != 2 {
		return out, errors.New("egress denial unproven")
	}
	return out, nil
}
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
