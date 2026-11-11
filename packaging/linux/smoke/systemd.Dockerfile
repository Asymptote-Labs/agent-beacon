# The container smoke-systemd.sh runs `beacon endpoint install --system` in: Ubuntu with systemd as
# PID 1. Built by image.sh, which CI pulls prebuilt from GHCR (see .github/workflows/smoke-images.yml).
ARG BASE=ubuntu:24.04
FROM ${BASE}
# --error-on=any: a failed index download fails here, naming the mirror, rather than surfacing later
# as "Unable to locate package".
RUN apt-get update -qq --error-on=any && apt-get install -y -qq systemd systemd-sysv dbus curl ca-certificates iproute2 >/dev/null
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
