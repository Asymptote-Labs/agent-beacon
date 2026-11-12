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

# The default configuration writes gateway-events.jsonl in the working directory; the nonroot
# user's home is the one directory it can write. Mount a volume or pass -config for anything else.
WORKDIR /home/nonroot
USER nonroot
ENTRYPOINT ["/usr/local/bin/beacon-gateway"]
