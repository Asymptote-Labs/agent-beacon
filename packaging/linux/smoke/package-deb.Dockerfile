# The container smoke-package.sh installs the .deb in: Ubuntu with systemd as PID 1. binutils is for
# readelf, which checks the packaged Vector is static. Built by image.sh, which CI pulls prebuilt
# from GHCR (see .github/workflows/smoke-images.yml).
ARG BASE=ubuntu:24.04
FROM ${BASE}
# --error-on=any: a failed index download fails here, naming the mirror, rather than surfacing later
# as "Unable to locate package".
RUN apt-get update -qq --error-on=any && apt-get install -y -qq systemd systemd-sysv dbus libpam-systemd curl ca-certificates iproute2 binutils >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
