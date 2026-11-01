#!/bin/sh
# Installs the lagrange binary from the latest GitHub release:
#   curl -fsSL https://raw.githubusercontent.com/three-agent-problem/lagrange/main/install.sh | sh
#
# LAGRANGE_VERSION=0.1.0   install that version instead of the latest
# LAGRANGE_INSTALL_DIR=... install there (default /usr/local/bin if writable, else ~/.local/bin)
set -eu

repo=three-agent-problem/lagrange
base=${LAGRANGE_BASE_URL:-https://github.com/$repo/releases}

fail() {
	echo "lagrange install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "needs $1"
}
need curl
need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
darwin | linux) ;;
*) fail "no prebuilt binary for $os; use go install github.com/$repo/cmd/lagrange@latest" ;;
esac

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "no prebuilt binary for $(uname -m); use go install github.com/$repo/cmd/lagrange@latest" ;;
esac

version=${LAGRANGE_VERSION:-}
if [ -z "$version" ]; then
	# /releases/latest redirects to /releases/tag/v<version>.
	latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$base/latest") || fail "can't reach $base/latest"
	version=${latest##*/}
	[ "$version" != latest ] || fail "there's no release yet"
fi
version=${version#v}

archive="lagrange_${version}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading lagrange $version for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/download/v$version/$archive" || fail "can't download $archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/download/v$version/checksums.txt" || fail "can't download checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive isn't in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	need shasum
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" lagrange

dir=${LAGRANGE_INSTALL_DIR:-}
if [ -z "$dir" ]; then
	if [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir=$HOME/.local/bin
	fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/lagrange" "$dir/lagrange" 2>/dev/null || { cp "$tmp/lagrange" "$dir/lagrange" && chmod 0755 "$dir/lagrange"; }

echo "Installed $("$dir/lagrange" version) to $dir/lagrange"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Add $dir to your PATH to run it as lagrange." ;;
esac
