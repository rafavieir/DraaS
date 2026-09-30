# ADR 006 — Real Linux recovery on an isolated libvirt/KVM laboratory

2026-09-28. Preserve all accepted simulator paths. The host exposes `/dev/kvm`; no ZSvirt endpoint or credentials have been supplied. Implement a temporary **lab-only** libvirt provider using the supported virsh interface, with QEMU/KVM in a separate Docker recovery plane, not a Kubernetes pod. Its VMs outlive API/worker pod replacement.

Use an official Alpine cloud image, checksum verification, a real Go HTTP service and SQLite inside the guest. Cold, cleanly shut-down qcow2 captures are FILESYSTEM_CONSISTENT; no CBT/VSS/application-consistent claim. Each full manifest is complete; subsequent captures reuse chunks. Delete the source domain and its owned disk before restore. Validate guest boot identity, OS, HTTP, random marker and SQLite revision against values captured before backup.

Each VM gets a private QEMU user-network stack. Recovery uses `restrict=on` and loopback-only host forwards, no bridge to production. Source bootstrap alone can access repositories. Never expose guest SSH/console ports publicly. This is a real KVM guest, not a simulator, but not a qualified production provider. libvirt/QEMU CLI, files and guest networking remain inside `providers/libvirtlab`.

Sources: https://libvirt.org/formatdomain.html ; https://www.qemu.org/docs/master/system/invocation.html ; https://alpinelinux.org/cloud/ . ZSvirt adapter must use https://docs.zsvirt.io/en/docs/api-reference/ and require a real endpoint for live qualification.
