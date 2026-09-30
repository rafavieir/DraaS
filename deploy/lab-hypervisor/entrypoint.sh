#!/bin/sh
set -eu
mkdir -p /run/libvirt /var/log/libvirt /var/lib/draas-lab
virtlogd -d
libvirtd -d
exec "$@"
