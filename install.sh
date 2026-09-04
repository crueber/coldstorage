#!/bin/sh
# coldstorage installer: downloads the latest release for this platform and
# installs the binary to ~/.local/bin (override with COLDSTORAGE_INSTALL_DIR).
#
#   curl -fsSL https://raw.githubusercontent.com/crueber/coldstorage/main/install.sh | sh
#
# macOS users can also `brew install crueber/tap/coldstorage`.
set -eu
REPO="crueber/coldstorage"
DEST="${COLDSTORAGE_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
	Linux) plat=Linux ;;
	Darwin) plat=macOS ;;
	*)
		echo "coldstorage installer: unsupported OS '$(uname -s)' — download an archive from https://github.com/$REPO/releases" >&2
		exit 1
		;;
esac
case "$(uname -m)" in
	x86_64 | amd64) arch=x86_64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "coldstorage installer: unsupported architecture '$(uname -m)'" >&2
		exit 1
		;;
esac

command -v curl >/dev/null 2>&1 || {
	echo "coldstorage installer: curl is required" >&2
	exit 1
}

version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
	sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p' | head -n 1)
[ -n "$version" ] || {
	echo "coldstorage installer: could not determine the latest release" >&2
	exit 1
}

url="https://github.com/$REPO/releases/download/v$version/coldstorage_${version}_${plat}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$url" | tar xz -C "$tmp"

mkdir -p "$DEST"
install "$tmp/coldstorage" "$DEST/coldstorage"
echo "installed coldstorage $version -> $DEST/coldstorage"

case ":$PATH:" in
	*":$DEST:"*) ;;
	*)
		echo "NOTE: $DEST is not on your PATH. Add it:"
		echo "  export PATH=\"$DEST:\$PATH\""
		;;
esac
