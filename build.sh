#!/bin/sh
# Builds collab-server for Windows, Linux and macOS into dist/.
# Uses a local Go toolchain when available, otherwise the golang Docker image.
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
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$out" .
	done
}

if [ "${1:-}" = "--inside-docker" ]; then
	build_all
elif command -v go >/dev/null 2>&1; then
	build_all
elif command -v docker >/dev/null 2>&1; then
	docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine sh ./build.sh --inside-docker
else
	echo "Neither Go (https://go.dev/dl/) nor Docker is installed." >&2
	exit 1
fi

echo "Done. Binaries are in $PWD/dist"
