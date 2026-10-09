#!/bin/sh
# Builds collab-server for Windows, Linux and macOS into dist/.
# Uses a local Go toolchain when available, otherwise the golang Docker image.
# Reads EditorCollaboration.uplugin (a symlink to the plugin descriptor) for the
# version embedded in the binary and written to dist/plugin.json.
set -eu
cd "$(dirname "$0")"
mkdir -p dist

TARGETS="windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

build_all() {
	for target in $TARGETS; do
		os=${target%/*}
		arch=${target#*/}
		ext=""
		[ "$os" = "windows" ] && ext=".exe"
		out="dist/collab-server-$os-$arch$ext"
		echo "Building $out"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$out" .
	done
}

if [ "${1:-}" = "--inside-docker" ]; then
	if [ -z "${LDFLAGS:-}" ]; then
		echo "LDFLAGS is required inside Docker." >&2
		exit 1
	fi
	build_all
	exit 0
fi

PLUGIN_FILE="EditorCollaboration.uplugin"
if [ ! -e "$PLUGIN_FILE" ]; then
	echo "EditorCollaboration.uplugin does not resolve. Build from the Unreal tree, where this file links to ../EditorCollaboration.uplugin." >&2
	exit 1
fi
first=$(head -n 1 "$PLUGIN_FILE" | tr -d '\r')
case "$first" in
	{*) ;;
	*)
		if [ -f "$first" ]; then
			PLUGIN_FILE=$first
		elif [ -f "$(dirname "$PLUGIN_FILE")/$first" ]; then
			PLUGIN_FILE="$(dirname "$PLUGIN_FILE")/$first"
		else
			echo "EditorCollaboration.uplugin points at $first, which does not exist." >&2
			exit 1
		fi
		;;
esac
PLUGIN_VERSION=$(sed -n 's/.*"VersionName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$PLUGIN_FILE" | head -n 1)
PLUGIN_MARKET=$(sed -n 's/.*"MarketplaceURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$PLUGIN_FILE" | head -n 1)
if [ -z "$PLUGIN_VERSION" ]; then
	echo "VersionName is missing in EditorCollaboration.uplugin." >&2
	exit 1
fi
LDFLAGS="-s -w -X main.pluginVersion=${PLUGIN_VERSION} -X main.pluginMarketplaceURL=${PLUGIN_MARKET}"
export LDFLAGS

if command -v go >/dev/null 2>&1; then
	build_all
elif command -v docker >/dev/null 2>&1; then
	docker run --rm -v "$PWD:/src" -w /src -e LDFLAGS golang:1.27-alpine sh ./build.sh --inside-docker
else
	echo "Neither Go (https://go.dev/dl/) nor Docker is installed." >&2
	exit 1
fi

printf '%s\n' "{\"version\":\"${PLUGIN_VERSION}\",\"marketplaceUrl\":\"${PLUGIN_MARKET}\"}" > dist/plugin.json

sums() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}
(
	cd dist
	# shellcheck disable=SC2086
	sums collab-server-* plugin.json > SHA256SUMS
)

echo "Done. Binaries are in $PWD/dist"
