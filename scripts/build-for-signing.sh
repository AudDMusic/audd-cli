#!/bin/sh
# Builds the Windows or macOS binaries exactly as .goreleaser.yaml does, so
# the bytes sent for signing match what GoReleaser builds later in the
# release job. Keep the flags, ldflags, and env in step with the "audd" build
# there.
#
#   scripts/build-for-signing.sh <windows|darwin> <version> <out-dir>
#
# <version> is the tag without the leading v (GoReleaser's .Version).
# Writes <out-dir>/audd-<os>-<arch>[.exe] and prints "<arch> <sha256>" lines.
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: $0 <windows|darwin> <version> <out-dir>" >&2
	exit 2
fi
os=$1
version=$2
out=$3
case $os in
windows) ext=.exe ;;
darwin) ext= ;;
*)
	echo "$os binaries are not signed" >&2
	exit 2
	;;
esac

commit=$(git show --format=%H --quiet HEAD)
# GoReleaser's .CommitDate: the commit date in UTC, RFC 3339.
date=$(TZ=UTC git show --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ --quiet HEAD)
timestamp=$(git show --format=%ct --quiet HEAD)
pkg=github.com/AudDMusic/audd-cli/internal/app

if [ -n "$(git status --porcelain)" ]; then
	# Go records a modified tree in the binary, and GoReleaser refuses one.
	echo "the working tree has changes; the binary would not match the release build" >&2
	exit 1
fi

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

mkdir -p "$out"
for arch in amd64 arm64; do
	bin="$out/audd-$os-$arch$ext"
	case $arch in
	amd64) level=GOAMD64=v1 ;;
	arm64) level=GOARM64=v8.0 ;;
	esac
	env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$level" \
		go build -trimpath \
		-ldflags="-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.Date=$date" \
		-o "$bin" ./cmd/audd
	touch -d "@$timestamp" "$bin" 2>/dev/null || true
	echo "$arch $(sha256 "$bin")"
done
