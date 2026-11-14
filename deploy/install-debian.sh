#!/bin/sh
set -eu
binary=/opt/editor-collab-rust/collab-server
unit=/etc/systemd/system/collab-server-rust.service
config=/etc/editor-collab-rust.env
service=collab-server-rust.service

usage() {
    printf '%s\n' \
        'Usage: sudo sh deploy/install-debian.sh <built-linux-binary>' \
        '       sudo sh deploy/install-debian.sh --update' \
        '       sudo sh deploy/install-debian.sh --uninstall'
}

case "${1:-}" in
    -h|--help|'') usage; exit 0 ;;
esac
[ "$(id -u)" -eq 0 ] || { printf 'Run as root.\n' >&2; exit 1; }
[ "$#" -eq 1 ] || { usage >&2; exit 1; }

case "$1" in
    --update)
        [ -x "$binary" ] || { printf 'The Rust server is not installed.\n' >&2; exit 1; }
        "$binary" -update
        systemctl restart "$service"
        ;;
    --uninstall)
        systemctl disable --now "$service"
        rm -f -- "$unit" "$binary"
        systemctl daemon-reload
        printf 'Kept %s and /var/lib/editor-collab-rust.\n' "$config"
        ;;
    *)
        [ -f "$1" ] || { printf 'Specify a built Linux executable.\n' >&2; exit 1; }
        if [ ! -f "$config" ]; then
            umask 077
            cat >"$config" <<'EOF'
COLLAB_HOST=127.0.0.1
COLLAB_PORT=7878
# COLLAB_ROOM=MyProject
# COLLAB_PASSWORD=replace-with-a-secret
EOF
        fi
        dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
        install -D -m 755 "$1" "$binary"
        install -m 644 "$dir/collab-server-rust.service" "$unit"
        systemctl daemon-reload
        systemctl enable "$service"
        systemctl restart "$service"
        ;;
esac
