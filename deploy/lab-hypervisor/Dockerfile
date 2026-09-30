FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients cloud-image-utils curl ca-certificates openssh-client iproute2 socat && rm -rf /var/lib/apt/lists/*
RUN printf 'security_driver = "none"\nuser = "root"\ngroup = "root"\ndynamic_ownership = 0\nnamespaces = []\ncgroup_controllers = []\n' > /etc/libvirt/qemu.conf && rm -f /etc/libvirt/qemu/networks/autostart/default.xml
COPY deploy/lab-hypervisor/entrypoint.sh /entrypoint.sh
COPY deploy/lab-hypervisor/qemu.conf /etc/libvirt/qemu.conf
RUN chmod 755 /entrypoint.sh
EXPOSE 8090
ENTRYPOINT ["/entrypoint.sh"]
