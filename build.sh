#!/bin/sh
set -eu
cd "$(dirname "$0")"
target=${1:-$(rustc -vV | sed -n 's/^host: //p')}
case "$target" in
    x86_64-*) arch=amd64 ;;
    aarch64-*) arch=arm64 ;;
    *) printf 'Unsupported release target %s\n' "$target" >&2; exit 1 ;;
esac
extension=
case "$target" in
    *-windows-msvc) os=windows; extension=.exe ;;
    *-linux-gnu|*-linux-musl) os=linux ;;
    *-apple-darwin) os=darwin ;;
    *) printf 'Unsupported release target %s\n' "$target" >&2; exit 1 ;;
esac
descriptor=../EditorCollaboration.uplugin
COLLAB_PLUGIN_VERSION=$(sed -n 's/.*"VersionName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$descriptor")
COLLAB_PLUGIN_MARKETPLACE=$(sed -n 's/.*"MarketplaceURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$descriptor")
export COLLAB_PLUGIN_VERSION COLLAB_PLUGIN_MARKETPLACE
target_dir=${CARGO_TARGET_DIR:-target}
cargo build --locked --release --target "$target" --target-dir "$target_dir"
mkdir -p dist
name="collab-server-rust-$os-$arch$extension"
cp "$target_dir/$target/release/collab-server$extension" "dist/$name"
sums() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
(
    cd dist
    : > SHA256SUMS
    for binary in collab-server-rust-*; do
        case "$binary" in *.sha256) continue ;; esac
        sums "$binary" | tee "$binary.sha256" >> SHA256SUMS
    done
)
printf '%s\n' "$PWD/dist/$name"
