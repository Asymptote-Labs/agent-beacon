#!/bin/sh
# Builds the beacon-gateway release from this checkout: one static binary per target, a tar.gz of
# each with the license, README and documented configuration, checksums.txt over the archives, and
# the Linux binaries laid out as the image's build context (image/linux/<arch>/beacon-gateway, for
# gateway/Dockerfile). CI runs it on every change and release-gateway.yml publishes what it builds,
# so the two never build differently.
#
#   sh gateway/scripts/release-build.sh [out-dir]     (default: gateway/dist)
#
# The version comes from the source (cmd/beacon-gateway/version.go), the commit and build date from
# git: the build date is the commit's, so building one commit twice stamps the same values. On Linux
# every binary built for Linux must be statically linked, checked on its ELF header; elsewhere the
# check is left to the Linux build.
set -eu

gateway_dir=$(cd "$(dirname "$0")/.." && pwd)
repo_dir=$(cd "$gateway_dir/.." && pwd)
out=${1:-$gateway_dir/dist}
case "$out" in /*) ;; *) out=$(pwd)/$out ;; esac

cd "$gateway_dir"
version=$(go run ./cmd/beacon-gateway -version | awk '{print $2}')
[ -n "$version" ] || { echo "release-build: no version from beacon-gateway -version" >&2; exit 1; }
commit=$(git rev-parse --short=12 HEAD)
epoch=$(git log -1 --format=%ct HEAD)
build_date=$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD)
ldflags="-s -w -X main.commit=$commit -X main.buildDate=$build_date"

rm -rf "$out"
mkdir -p "$out/image"
# GNU tar can write the same bytes for the same files; bsdtar (macOS) cannot, which only matters for
# CI and the release, both on Linux. COPYFILE_DISABLE keeps macOS's ._ metadata files out.
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
	tar_flags="--sort=name --owner=0 --group=0 --numeric-owner --mtime=@$epoch"
else
	tar_flags=""
	export COPYFILE_DISABLE=1
fi

static_check() {
	bin=$1
	if [ "$(uname -s)" != Linux ]; then
		return 0
	fi
	# Fails closed: a missing readelf or empty output leaves the linkage unknown, never passed.
	headers=$(readelf -l "$bin") || { echo "release-build: readelf failed on $bin, so its linkage is unknown" >&2; exit 1; }
	case "$headers" in
	*"Program Headers"*) ;;
	*) echo "release-build: readelf printed no program headers for $bin, so its linkage is unknown" >&2; exit 1 ;;
	esac
	case "$headers" in
	*INTERP*)
		echo "release-build: $bin is dynamically linked (it has a PT_INTERP segment)" >&2
		exit 1
		;;
	esac
}

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	goos=${target%/*}
	goarch=${target#*/}
	name=beacon-gateway_${version}_${goos}_${goarch}
	stage=$out/stage/$name
	mkdir -p "$stage"
	CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -ldflags "$ldflags" \
		-o "$stage/beacon-gateway" ./cmd/beacon-gateway
	cp "$repo_dir/LICENSE" "$gateway_dir/README.md" "$stage/"
	cp "$gateway_dir/deploy/beacon-gateway.yaml" "$stage/beacon-gateway.example.yaml"
	if [ "$goos" = linux ]; then
		static_check "$stage/beacon-gateway"
		mkdir -p "$out/image/linux/$goarch"
		cp "$stage/beacon-gateway" "$out/image/linux/$goarch/beacon-gateway"
	fi
	# shellcheck disable=SC2086 # tar_flags is a list of flags, or nothing
	(cd "$out/stage" && tar $tar_flags -cf - "$name") | gzip -n -9 > "$out/$name.tar.gz"
done
rm -rf "$out/stage"

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt
else
	shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt
fi
echo "beacon-gateway $version ($commit, $build_date) in $out:"
cat checksums.txt
