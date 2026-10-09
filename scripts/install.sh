#!/bin/sh
# Install the AudD CLI (audd) on macOS or Linux.
#
#   curl -fsSL https://audd.io/install.sh | sh
#
# Downloads the release archive for this system from GitHub, checks it
# against the release's checksums.txt (SHA-256), and puts audd in
# ~/.local/bin. Never uses sudo.
#
# Settings (environment variables):
#   AUDD_VERSION       version to install, such as 1.2.3 (default: the latest release)
#   AUDD_INSTALL_DIR   where to put audd (default: ~/.local/bin)
#   AUDD_DOWNLOAD_URL  where release files are downloaded from
#                      (default: https://github.com/AudDMusic/audd-cli/releases/download)
#   AUDD_LATEST_URL    page that redirects to the latest release
#                      (default: https://github.com/AudDMusic/audd-cli/releases/latest)
#   AUDD_RELEASES_API  where the latest release is looked up if that redirect fails
#                      (default: https://api.github.com/repos/AudDMusic/audd-cli/releases/latest)

set -eu

REPO="AudDMusic/audd-cli"
DOWNLOAD_URL="${AUDD_DOWNLOAD_URL:-https://github.com/$REPO/releases/download}"
LATEST_URL="${AUDD_LATEST_URL:-https://github.com/$REPO/releases/latest}"
RELEASES_API="${AUDD_RELEASES_API:-https://api.github.com/repos/$REPO/releases/latest}"
INSTALL_DIR="${AUDD_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*" >&2; }
fail() {
	say "audd install: $*"
	exit 1
}

# fetch URL FILE downloads URL to FILE.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "curl or wget is needed to download audd"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed 's/^.*= *//'
	else
		fail "sha256sum, shasum, or openssl is needed to verify the download"
	fi
}

detect_os() {
	os=$(uname -s)
	case "$os" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	MINGW* | MSYS* | CYGWIN* | Windows*)
		fail "this script is for macOS and Linux. On Windows, run: winget install --id AudD.CLI (or: npx @audd/cli)"
		;;
	*) fail "no audd build for $os. Try: go install github.com/AudDMusic/audd-cli/cmd/audd@latest" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) fail "no audd build for $arch. Try: go install github.com/AudDMusic/audd-cli/cmd/audd@latest" ;;
	esac
}

# latest_version prints the latest release tag. It follows the
# releases/latest redirect first (no API rate limit), then asks the API.
latest_version() {
	if command -v curl >/dev/null 2>&1; then
		url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL" 2>/dev/null) || url=""
		case "$url" in
		*/releases/tag/?*)
			echo "${url##*/releases/tag/}"
			return
			;;
		esac
	fi
	fetch "$RELEASES_API" "$tmp/latest.json" || fail "could not look up the latest release at $LATEST_URL or $RELEASES_API"
	v=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
	[ -n "$v" ] || fail "could not read the latest release version from $RELEASES_API"
	echo "$v"
}

os=$(detect_os)
arch=$(detect_arch)

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t audd-install)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

version="${AUDD_VERSION:-}"
if [ -z "$version" ]; then
	version=$(latest_version)
fi
version="${version#v}"

archive="audd_${version}_${os}_${arch}.tar.gz"
base="$DOWNLOAD_URL/v$version"

say "Downloading audd $version for $os/$arch"
fetch "$base/$archive" "$tmp/$archive" || fail "could not download $base/$archive (is $version a released version?)"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download $base/checksums.txt"

want=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no checksum for $archive; nothing was installed"
got=$(sha256 "$tmp/$archive")
if [ "$want" != "$got" ]; then
	fail "$archive does not match its checksum (expected $want, got $got); nothing was installed"
fi

mkdir -p "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" || fail "could not unpack $archive"
[ -f "$tmp/x/audd" ] || fail "$archive has no audd binary"

mkdir -p "$INSTALL_DIR" || fail "could not create $INSTALL_DIR"
# Copy next to the target, then rename, so a running audd is never half-written.
cp "$tmp/x/audd" "$INSTALL_DIR/.audd.new" || fail "could not write to $INSTALL_DIR"
chmod 755 "$INSTALL_DIR/.audd.new"
mv -f "$INSTALL_DIR/.audd.new" "$INSTALL_DIR/audd" || fail "could not write $INSTALL_DIR/audd"

say "Installed $INSTALL_DIR/audd"
"$INSTALL_DIR/audd" version --format table || true

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	say ""
	say "$INSTALL_DIR is not on your PATH. Add it with:"
	say "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile"
	say "then open a new terminal."
	;;
esac

say ""
say "Next: audd login (or get your API token at https://dashboard.audd.io and run audd config set token your-api-token)"
