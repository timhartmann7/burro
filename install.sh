#!/bin/sh
# Installs burro from GitHub Releases: https://github.com/timhartmann7/burro
#
#   curl -fsSL https://github.com/timhartmann7/burro/releases/latest/download/install.sh | sh
#
# BURRO_VERSION=v0.1.0 picks a release instead of the latest.
# BURRO_INSTALL_DIR picks the folder, default ~/.local/bin. No sudo: nothing
# is written outside that folder and a temp folder removed at the end.
set -eu

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

base=${BURRO_BASE_URL:-https://github.com/timhartmann7/burro/releases}
case $base in
https://* | http://127.0.0.1 | http://127.0.0.1[:/]*) ;;
*) fail "BURRO_BASE_URL must start with https:// or http://127.0.0.1" ;;
esac
dir=${BURRO_INSTALL_DIR:-$HOME/.local/bin}
command -v curl >/dev/null || fail "curl is missing, install it and rerun"

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "burro runs on macOS and Linux, not on $(uname -s)" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "burro runs on amd64 and arm64, not on $(uname -m)" ;;
esac
# Under Rosetta uname reports x86_64 on an Apple silicon Mac.
if [ "$os" = darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
	arch=arm64
fi

# The latest release redirects to its tag, which names the version.
tag=${BURRO_VERSION:-}
if [ -z "$tag" ]; then
	tag=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$base/latest") ||
		fail "can't reach $base/latest"
	tag=${tag##*/}
fi
case $tag in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) fail "not a release version: $tag" ;;
esac

archive=burro_${os}_${arch}.tar.gz
echo "downloading burro ${tag#v} for $os/$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for f in "$archive" checksums.txt; do
	curl -fsSL -o "$tmp/$f" "$base/download/$tag/$f" ||
		fail "can't download $base/download/$tag/$f"
done

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null; then
	got=$(sha256sum "$tmp/$archive")
else
	got=$(shasum -a 256 "$tmp/$archive")
fi
if [ -z "$want" ] || [ "${got%% *}" != "$want" ]; then
	fail "checksum of $archive does not match checksums.txt, nothing was installed"
fi
echo "checksum ok"

tar -xzf "$tmp/$archive" -C "$tmp" burro
mkdir -p "$dir"
# A rename in the same folder never leaves half a binary behind.
cp "$tmp/burro" "$dir/.burro.$$"
chmod 755 "$dir/.burro.$$"
mv -f "$dir/.burro.$$" "$dir/burro"

case $dir in
"$HOME"/*) shown="~${dir#"$HOME"}" profile="\$HOME${dir#"$HOME"}" ;;
*) shown=$dir profile=$dir ;;
esac
echo "installed to $shown/burro"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "add to your shell profile: export PATH=\"$profile:\$PATH\"" ;;
esac
echo
"$dir/burro"
