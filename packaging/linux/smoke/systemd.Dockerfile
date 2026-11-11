# The container smoke-systemd.sh runs `beacon endpoint install --system` in: Ubuntu with systemd as
# PID 1. Built by image.sh, which CI pulls prebuilt from GHCR (see .github/workflows/smoke-images.yml).
ARG BASE=ubuntu:24.04
FROM ${BASE}
RUN apt-get update -qq && apt-get install -y -qq systemd systemd-sysv dbus curl ca-certificates iproute2 >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
