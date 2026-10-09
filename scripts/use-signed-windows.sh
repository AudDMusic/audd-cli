#!/bin/sh
# GoReleaser build post-hook: swaps a fresh Windows binary for its signed copy.
#
#   scripts/use-signed-windows.sh <os> <arch> <path> <commit-timestamp>
#
# AUDD_SIGNED_WINDOWS_DIR holds audd-windows-<arch>.exe, signed in the
# release workflow. AUDD_WINDOWS_<ARCH>_SHA256 is the sha256 of the unsigned
# binary that was approved for signing. The hook refuses unless GoReleaser's
# binary has exactly that sha256, so the signed file is the same build plus
# a signature. Without AUDD_SIGNED_WINDOWS_DIR it does nothing, and the
# Windows binaries ship unsigned.
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: $0 <os> <arch> <path> <commit-timestamp>" >&2
	exit 2
fi
os=$1
arch=$2
path=$3
timestamp=$4

[ "$os" = windows ] || exit 0

dir=${AUDD_SIGNED_WINDOWS_DIR:-}
if [ -z "$dir" ]; then
	echo "windows/$arch: no signed binary; releasing it unsigned"
	exit 0
fi

case $arch in
amd64) expected=${AUDD_WINDOWS_AMD64_SHA256:-} ;;
arm64) expected=${AUDD_WINDOWS_ARM64_SHA256:-} ;;
*)
	echo "windows/$arch: no signed binary is built for this architecture" >&2
	exit 1
	;;
esac
if [ -z "$expected" ]; then
	echo "windows/$arch: the approved sha256 is missing" >&2
	exit 1
fi

signed="$dir/audd-windows-$arch.exe"
if [ ! -f "$signed" ]; then
	echo "windows/$arch: $signed is missing" >&2
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
	echo "windows/$arch: GoReleaser built sha256 $got, but $expected was approved for signing; refusing to ship the signed binary" >&2
	exit 1
fi

cp "$signed" "$path"
chmod 755 "$path"
touch -d "@$timestamp" "$path" 2>/dev/null || true
echo "windows/$arch: replaced with the signed binary (unsigned sha256 $got, signed sha256 $(sha256 "$path"))"
