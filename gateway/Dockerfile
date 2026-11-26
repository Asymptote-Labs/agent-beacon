# The beacon-gateway image. It packages the static Linux binaries scripts/release-build.sh built
# rather than compiling them, so the image runs the same bytes as the release's archives, and a
# multi-platform build only copies files (no emulation). Build context: the script's image/
# directory.
#
#   sh scripts/release-build.sh
#   docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile dist/image
#
# Distroless static, nonroot (uid 65532): CA certificates and nothing else, no shell. Pinned by the
# multi-platform index's digest; bump the digest, never back to the bare tag.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=

LABEL org.opencontainers.image.title="beacon-gateway" \
      org.opencontainers.image.description="Records the model calls an LLM gateway (LiteLLM, Envoy, Portkey) carries as Beacon events" \
      org.opencontainers.image.source="https://github.com/Asymptote-Labs/agent-beacon" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

COPY linux/${TARGETARCH}/beacon-gateway /usr/local/bin/beacon-gateway

# Without -config the gateway listens on the container's loopback: the LiteLLM callout on
# 127.0.0.1:4019, the admin listener on 127.0.0.1:4029. That is where a sidecar in the same pod
# reaches it. A published port (-p) or a gateway in another container cannot: for those, mount a
# configuration whose adapter listens on 0.0.0.0, on a network you trust, since the callout takes
# any caller. The default configuration writes gateway-events.jsonl in the working directory, the
# one directory the nonroot user can write; mount a volume for anything else.
WORKDIR /home/nonroot
# Numeric, as the base sets it: Kubernetes verifies runAsNonRoot only for a numeric user, and
# refuses to start an image whose user is a name.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/beacon-gateway"]
