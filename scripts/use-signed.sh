#!/bin/sh
# GoReleaser build post-hook: swaps a fresh Windows or macOS binary for its
# signed copy.
#
#   scripts/use-signed.sh <os> <arch> <path> <commit-timestamp>
#
# AUDD_SIGNED_WINDOWS_DIR holds audd-windows-<arch>.exe and
# AUDD_SIGNED_DARWIN_DIR holds audd-darwin-<arch>, signed in the release
# workflow. AUDD_<OS>_<ARCH>_SHA256 is the sha256 of the unsigned binary that
# was sent for signing. The hook refuses unless GoReleaser's binary has
# exactly that sha256, so the signed file is the same build plus a signature.
# Without the directory for an OS it does nothing, and that OS's binaries
# ship unsigned.
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: $0 <os> <arch> <path> <commit-timestamp>" >&2
	exit 2
fi
os=$1
arch=$2
path=$3
timestamp=$4

case $os in
windows)
	dir=${AUDD_SIGNED_WINDOWS_DIR:-}
	name=audd-windows-$arch.exe
	case $arch in
	amd64) expected=${AUDD_WINDOWS_AMD64_SHA256:-} ;;
	arm64) expected=${AUDD_WINDOWS_ARM64_SHA256:-} ;;
	*) expected=unknown ;;
	esac
	;;
darwin)
	dir=${AUDD_SIGNED_DARWIN_DIR:-}
	name=audd-darwin-$arch
	case $arch in
	amd64) expected=${AUDD_DARWIN_AMD64_SHA256:-} ;;
	arm64) expected=${AUDD_DARWIN_ARM64_SHA256:-} ;;
	*) expected=unknown ;;
	esac
	;;
*) exit 0 ;;
esac

if [ -z "$dir" ]; then
	echo "$os/$arch: no signed binary; releasing it unsigned"
	exit 0
fi
if [ "$expected" = unknown ]; then
	echo "$os/$arch: no signed binary is built for this architecture" >&2
	exit 1
fi
if [ -z "$expected" ]; then
	echo "$os/$arch: the sha256 of the binary sent for signing is missing" >&2
	exit 1
fi

signed="$dir/$name"
if [ ! -f "$signed" ]; then
	echo "$os/$arch: $signed is missing" >&2
	exit 1
fi

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

got=$(sha256 "$path")
expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')
if [ "$got" != "$expected" ]; then
	echo "$os/$arch: GoReleaser built sha256 $got, but $expected was sent for signing; refusing to ship the signed binary" >&2
	exit 1
fi

cp "$signed" "$path"
chmod 755 "$path"
touch -d "@$timestamp" "$path" 2>/dev/null || true
echo "$os/$arch: replaced with the signed binary (unsigned sha256 $got, signed sha256 $(sha256 "$path"))"
