# The container smoke-package.sh installs the .rpm in: Rocky Linux with systemd as PID 1, because
# RHEL and its rebuilds are what RPM fleets run. Its image already has curl-minimal, which conflicts
# with the curl package. binutils is for readelf. Built by image.sh, which CI pulls prebuilt from
# GHCR (see .github/workflows/smoke-images.yml).
ARG BASE=rockylinux/rockylinux:9
FROM ${BASE}
RUN dnf install -y -q systemd dbus ca-certificates iproute binutils >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
