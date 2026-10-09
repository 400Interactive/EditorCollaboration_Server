#!/bin/sh
# Installs the Editor Collaboration server on Debian 11, 12, or 13.
# Downloads the binary (linux-amd64 or linux-arm64) from GitHub Releases,
# installs it to /opt/editor-collab, and registers a systemd service.
#
#   https://github.com/400Interactive/EditorCollaboration_Server
# Assets expected in a release:
#   collab-server-linux-amd64
#   collab-server-linux-arm64
#
# Install (once this script is on GitHub):
#   curl -fsSL https://raw.githubusercontent.com/400Interactive/EditorCollaboration_Server/main/deploy/install-debian.sh | sudo bash
#
# With a password, no prompt:
#   curl -fsSL https://raw.githubusercontent.com/400Interactive/EditorCollaboration_Server/main/deploy/install-debian.sh | sudo COLLAB_PASSWORD='my-password' bash
#
# From a local copy, at the repository root:
#   sudo sh deploy/install-debian.sh
#
# Variables:
#   COLLAB_PASSWORD      client password (prompted if missing and /etc/editor-collab.env does not exist)
#   COLLAB_PORT          TCP port (default 7878)
#   COLLAB_AUTO_UPDATE   1 to install updates every day (default: off)
#   COLLAB_VERSION       release tag, e.g. v1.0.0 (default: latest release)
#   COLLAB_GITHUB_REPO   owner/name repository (default: temporary link above)
#   COLLAB_DOWNLOAD_URL  full binary URL, instead of GitHub Releases
#
# Other uses, from the repository root:
#   sudo sh deploy/install-debian.sh --update             # replace the binary from GitHub and restart
#   sudo sh deploy/install-debian.sh --uninstall          # remove the service and binary, keep the data
#   sudo sh deploy/install-debian.sh --purge              # also remove the data and the password
#   sudo sh deploy/install-debian.sh --force              # skip the Debian 11/12/13 check

set -eu

GITHUB_REPO="${COLLAB_GITHUB_REPO:-400Interactive/EditorCollaboration_Server}"
PORT="${COLLAB_PORT:-7878}"
INSTALL_BIN="/opt/editor-collab/collab-server"
ENV_FILE="/etc/editor-collab.env"
UNIT_FILE="/etc/systemd/system/collab-server.service"
DATA_DIR="/var/lib/editor-collab"
IPTABLES_RULES_V4="/etc/iptables/rules.v4"
IPTABLES_RULES_V6="/etc/iptables/rules.v6"
IPTABLES_UNIT="/etc/systemd/system/editor-collab-iptables.service"
UPDATE_SERVICE="/etc/systemd/system/collab-server-update.service"
UPDATE_TIMER="/etc/systemd/system/collab-server-update.timer"

die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

info() {
	printf '%s\n' "$*"
}

usage() {
	cat <<'EOF'
Usage: sudo sh install-debian.sh [options]

Options:
  --uninstall   remove the service and the binary, keep the data
  --purge       also remove /var/lib/editor-collab and /etc/editor-collab.env
  --update      download the latest release, replace the binary, and restart
  --force       continue even if the system is not Debian 11, 12, or 13
  --help        show this help

Variables:
  COLLAB_PASSWORD      password (prompted if missing)
  COLLAB_PORT          TCP port (default 7878)
  COLLAB_AUTO_UPDATE   1 to install updates every day (default: off)
  COLLAB_VERSION       release tag, e.g. v1.0.0 (default: latest release)
  COLLAB_GITHUB_REPO   GitHub repository owner/name
  COLLAB_DOWNLOAD_URL  full binary URL, instead of GitHub Releases
EOF
}

need_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "Run this script as root: sudo sh install-debian.sh"
	fi
}

check_debian() {
	if [ "${FORCE:-0}" -eq 1 ]; then
		return 0
	fi
	if [ ! -r /etc/os-release ]; then
		die "Cannot read /etc/os-release. This script targets Debian 11, 12, and 13 (use --force to continue)."
	fi
	# shellcheck disable=SC1091
	. /etc/os-release
	if [ "${ID:-}" != "debian" ]; then
		die "Detected system: ${ID:-unknown}. This script targets Debian 11, 12, and 13 (use --force to continue)."
	fi
	case "${VERSION_ID:-}" in
		11|11.*|12|12.*|13|13.*) ;;
		*)
			die "Detected Debian ${VERSION_ID:-unknown}. Supported versions: 11, 12, and 13 (use --force to continue)."
			;;
	esac
	info "Debian ${VERSION_ID} (${VERSION_CODENAME:-})."
}

check_port() {
	case "$PORT" in
		''|*[!0-9]*) die "Invalid COLLAB_PORT: $PORT" ;;
	esac
	if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
		die "COLLAB_PORT out of range: $PORT"
	fi
}

asset_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		aarch64|arm64) printf 'arm64' ;;
		*) die "Unsupported architecture: $(uname -m). Expected x86_64 or aarch64." ;;
	esac
}

download_url() {
	arch=$1
	if [ -n "${COLLAB_DOWNLOAD_URL:-}" ]; then
		printf '%s' "$COLLAB_DOWNLOAD_URL"
		return 0
	fi
	asset="collab-server-linux-${arch}"
	if [ -n "${COLLAB_VERSION:-}" ]; then
		printf 'https://github.com/%s/releases/download/%s/%s' "$GITHUB_REPO" "$COLLAB_VERSION" "$asset"
	else
		printf 'https://github.com/%s/releases/latest/download/%s' "$GITHUB_REPO" "$asset"
	fi
}

ensure_curl() {
	if command -v curl >/dev/null 2>&1; then
		return 0
	fi
	info "Installing curl..."
	export DEBIAN_FRONTEND=noninteractive
	apt-get update
	apt-get install -y curl ca-certificates
}

read_secret() {
	prompt=$1
	printf '%s' "$prompt" >/dev/tty
	stty -echo </dev/tty
	IFS= read -r value </dev/tty || value=""
	stty echo </dev/tty
	printf '\n' >/dev/tty
	printf '%s' "$value"
}

ensure_password() {
	if [ -n "${COLLAB_PASSWORD:-}" ]; then
		write_password "$COLLAB_PASSWORD"
		return 0
	fi
	if [ -f "$ENV_FILE" ] && grep -q '^COLLAB_PASSWORD=.' "$ENV_FILE"; then
		info "Password already set in $ENV_FILE (kept)."
		return 0
	fi
	if [ ! -r /dev/tty ]; then
		die "No terminal to ask for the password. Rerun with: sudo COLLAB_PASSWORD='...' sh install-debian.sh"
	fi
	pass=$(read_secret "Server password: ")
	confirm=$(read_secret "Confirm the password: ")
	if [ -z "$pass" ]; then
		die "The password cannot be empty."
	fi
	if [ "$pass" != "$confirm" ]; then
		die "The passwords do not match."
	fi
	write_password "$pass"
}

write_password() {
	pass=$1
	umask 077
	tmp=$(mktemp)
	if [ -f "$ENV_FILE" ]; then
		grep -v '^COLLAB_PASSWORD=' "$ENV_FILE" >"$tmp" || true
	fi
	printf 'COLLAB_PASSWORD=%s\n' "$pass" >>"$tmp"
	install -m 600 "$tmp" "$ENV_FILE"
	rm -f "$tmp"
	info "Password saved to $ENV_FILE (root-only)."
}

write_unit() {
	tmp=$(mktemp)
	cat >"$tmp" <<EOF
[Unit]
Description=Editor Collaboration server (Unreal Engine plugin)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=${INSTALL_BIN} -data ${DATA_DIR}
Environment=COLLAB_PORT=${PORT}
# COLLAB_PASSWORD lives in ${ENV_FILE}, readable by root only.
EnvironmentFile=${ENV_FILE}
DynamicUser=yes
StateDirectory=editor-collab
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
	install -m 644 "$tmp" "$UNIT_FILE"
	rm -f "$tmp"
}

auto_update_enabled() {
	case "$(printf '%s' "${COLLAB_AUTO_UPDATE:-}" | tr '[:upper:]' '[:lower:]')" in
		1|true|yes|on) return 0 ;;
	esac
	return 1
}

set_env_assignment() {
	key=$1
	value=$2
	umask 077
	tmp=$(mktemp)
	if [ -f "$ENV_FILE" ]; then
		grep -v "^${key}=" "$ENV_FILE" >"$tmp" || true
	fi
	if [ -n "$value" ]; then
		printf '%s=%s\n' "$key" "$value" >>"$tmp"
	fi
	install -m 600 "$tmp" "$ENV_FILE"
	rm -f "$tmp"
}

write_update_units() {
	tmp=$(mktemp)
	cat >"$tmp" <<EOF
[Unit]
Description=Update the Editor Collaboration server from GitHub
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${INSTALL_BIN} -update
EOF
	install -m 644 "$tmp" "$UPDATE_SERVICE"
	cat >"$tmp" <<EOF
[Unit]
Description=Daily update of the Editor Collaboration server

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
EOF
	install -m 644 "$tmp" "$UPDATE_TIMER"
	rm -f "$tmp"
}

remove_update_units() {
	if command -v systemctl >/dev/null 2>&1; then
		systemctl disable --now collab-server-update.timer >/dev/null 2>&1 || true
	fi
	rm -f "$UPDATE_SERVICE" "$UPDATE_TIMER"
}

sync_auto_update() {
	if auto_update_enabled; then
		set_env_assignment COLLAB_AUTO_UPDATE 1
		write_update_units
		systemctl daemon-reload
		systemctl enable --now collab-server-update.timer >/dev/null
		info "Automatic updates enabled (daily)."
	else
		set_env_assignment COLLAB_AUTO_UPDATE ""
		remove_update_units
		if command -v systemctl >/dev/null 2>&1; then
			systemctl daemon-reload
		fi
	fi
}

installed_port() {
	if [ ! -f "$UNIT_FILE" ]; then
		return 0
	fi
	unit_port=$(sed -n 's/^Environment=COLLAB_PORT=//p' "$UNIT_FILE" | tr -d '[:space:]' | head -n 1)
	case "$unit_port" in
		''|*[!0-9]*) ;;
		*) PORT=$unit_port ;;
	esac
}

update_server() {
	need_root
	installed_port
	if [ ! -x "$INSTALL_BIN" ]; then
		die "The server is not installed. Run install-debian.sh without --update first."
	fi
	ensure_curl
	tmpdir=$(mktemp -d)
	trap 'rm -rf "$tmpdir"' EXIT INT TERM
	set +e
	"$INSTALL_BIN" -update >"$tmpdir/out" 2>"$tmpdir/err"
	code=$?
	set -e
	cat "$tmpdir/out"
	cat "$tmpdir/err" >&2
	if [ "$code" -eq 0 ]; then
		if wait_health; then
			info "The server answers at http://127.0.0.1:${PORT}/health"
		fi
		return 0
	fi
	if grep -q "flag provided but not defined: -update" "$tmpdir/err"; then
		info "This executable has no -update yet. Downloading the latest release."
		download_binary "$tmpdir/collab-server"
		install -m 755 "$tmpdir/collab-server" "$INSTALL_BIN"
		systemctl restart collab-server
		if wait_health; then
			info "The server answers at http://127.0.0.1:${PORT}/health"
		fi
		return 0
	fi
	exit "$code"
}

verify_elf() {
	file=$1
	magic=$(od -An -t x1 -N 4 "$file" | tr -d ' \n')
	if [ "$magic" != "7f454c46" ]; then
		die "The downloaded file is not a Linux executable. The GitHub release may not exist yet."
	fi
}

verify_sha256() {
	bin=$1
	url=$2
	sumfile="${bin}.sha256"
	if ! curl -fsSL --retry 3 --retry-delay 2 -o "$sumfile" "${url}.sha256"; then
		info "No .sha256 file next to the binary: checksum not verified."
		rm -f "$sumfile"
		return 0
	fi
	expected=$(awk 'NR==1 { print $1 }' "$sumfile")
	actual=$(sha256sum "$bin" | awk 'NR==1 { print $1 }')
	rm -f "$sumfile"
	if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
		die "SHA256 checksum does not match."
	fi
	info "SHA256 checksum verified."
}

download_binary() {
	arch=$(asset_arch)
	url=$(download_url "$arch")
	dest="$1"
	info "Downloading (${arch}): $url"
	if ! curl -fL --retry 3 --retry-delay 2 -o "$dest" "$url"; then
		die "Download failed. Publish a release containing collab-server-linux-${arch}, or set COLLAB_DOWNLOAD_URL / COLLAB_GITHUB_REPO / COLLAB_VERSION."
	fi
	verify_elf "$dest"
	verify_sha256 "$dest" "$url"
}

apply_iptables_rule() {
	cmd=$1
	if "$cmd" -S INPUT 2>/dev/null | grep -Eq -- "--dport ${PORT}([^0-9]|$).*-j ACCEPT"; then
		rule_state=present
		return 0
	fi
	if ! "$cmd" -I INPUT -p tcp --dport "$PORT" -m comment --comment "Editor Collaboration" -j ACCEPT; then
		rule_state=failed
		return 0
	fi
	rule_state=added
}

remove_iptables_rule() {
	cmd=$1
	rule_removed=0
	while "$cmd" -D INPUT -p tcp --dport "$PORT" -m comment --comment "Editor Collaboration" -j ACCEPT >/dev/null 2>&1; do
		rule_removed=1
	done
	[ "$rule_removed" -eq 1 ]
}

drop_iptables_restore_unit() {
	if [ ! -f "$IPTABLES_UNIT" ]; then
		return 0
	fi
	if command -v systemctl >/dev/null 2>&1; then
		systemctl disable editor-collab-iptables.service >/dev/null 2>&1 || true
	fi
	rm -f "$IPTABLES_UNIT"
	if command -v systemctl >/dev/null 2>&1; then
		systemctl daemon-reload
	fi
}

install_iptables_restore_unit() {
	restore4=$(command -v iptables-restore || true)
	restore6=$(command -v ip6tables-restore || true)
	if [ -z "$restore4" ]; then
		die "iptables-restore was not found."
	fi
	tmp=$(mktemp)
	cat >"$tmp" <<EOF
[Unit]
Description=Restore iptables rules saved by the Editor Collaboration installer
DefaultDependencies=no
Before=network-pre.target
Wants=network-pre.target
ConditionPathExists=${IPTABLES_RULES_V4}

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=${restore4} ${IPTABLES_RULES_V4}
EOF
	if [ -n "$restore6" ]; then
		printf 'ExecStart=-%s %s\n' "$restore6" "$IPTABLES_RULES_V6" >>"$tmp"
	fi
	cat >>"$tmp" <<EOF

[Install]
WantedBy=multi-user.target
EOF
	install -m 644 "$tmp" "$IPTABLES_UNIT"
	rm -f "$tmp"
	systemctl daemon-reload
	systemctl enable editor-collab-iptables.service >/dev/null
}

persist_iptables() {
	if command -v netfilter-persistent >/dev/null 2>&1; then
		drop_iptables_restore_unit
		if ! netfilter-persistent save >/dev/null; then
			die "netfilter-persistent save failed."
		fi
		info "Firewall rules saved with netfilter-persistent."
		return 0
	fi
	if ! command -v iptables-save >/dev/null 2>&1; then
		die "Neither netfilter-persistent nor iptables-save is available."
	fi
	mkdir -p /etc/iptables
	iptables-save >"$IPTABLES_RULES_V4"
	if command -v ip6tables-save >/dev/null 2>&1; then
		ip6tables-save >"$IPTABLES_RULES_V6" || true
	fi
	install_iptables_restore_unit
	info "Firewall rules saved with iptables-save."
}

open_firewall() {
	if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q 'Status: active'; then
		ufw allow "${PORT}/tcp" comment 'Editor Collaboration' >/dev/null
		info "Firewall: allowed TCP port ${PORT} (ufw)."
		return 0
	fi

	if ! command -v iptables >/dev/null 2>&1; then
		info "Neither ufw nor iptables is active. Open TCP port ${PORT} if a firewall is filtering traffic."
		return 0
	fi

	added=0
	apply_iptables_rule iptables
	case "$rule_state" in
		added) added=1 ;;
		failed) die "Could not add the iptables rule for TCP port ${PORT}." ;;
	esac
	if command -v ip6tables >/dev/null 2>&1; then
		apply_iptables_rule ip6tables
		case "$rule_state" in
			added) added=1 ;;
			failed) info "IPv6 rule not added (ip6tables unavailable)." ;;
		esac
	fi

	if [ "$added" -eq 1 ]; then
		info "Firewall: allowed TCP port ${PORT} (iptables)."
		persist_iptables
	else
		info "Firewall: TCP port ${PORT} already allowed (iptables)."
	fi
}

close_firewall() {
	if command -v ufw >/dev/null 2>&1; then
		ufw delete allow "${PORT}/tcp" >/dev/null 2>&1 || true
	fi

	removed=0
	if command -v iptables >/dev/null 2>&1 && remove_iptables_rule iptables; then
		removed=1
	fi
	if command -v ip6tables >/dev/null 2>&1 && remove_iptables_rule ip6tables; then
		removed=1
	fi
	if [ "$removed" -eq 1 ]; then
		info "Firewall: removed TCP port ${PORT} (iptables)."
		persist_iptables
	fi
}

primary_ipv4() {
	ip -4 -o addr show scope global 2>/dev/null | awk '{ print $4 }' | cut -d/ -f1 | head -n 1
}

wait_health() {
	i=0
	while [ "$i" -lt 20 ]; do
		if curl -fsS "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

install_server() {
	need_root
	check_debian
	check_port
	if ! command -v systemctl >/dev/null 2>&1; then
		die "systemd is required (systemctl not found)."
	fi
	ensure_curl

	tmpdir=$(mktemp -d)
	trap 'stty echo </dev/tty 2>/dev/null || true; rm -rf "$tmpdir"' EXIT INT TERM
	download_binary "$tmpdir/collab-server"

	info "Installing $INSTALL_BIN"
	install -D -m 755 "$tmpdir/collab-server" "$INSTALL_BIN"
	ensure_password
	write_unit
	sync_auto_update

	systemctl daemon-reload
	systemctl enable collab-server >/dev/null
	if systemctl is-active --quiet collab-server; then
		systemctl restart collab-server
	else
		systemctl start collab-server
	fi

	open_firewall

	if wait_health; then
		info "The server answers at http://127.0.0.1:${PORT}/health"
	else
		info "The service is running, but /health does not answer yet. Check: journalctl -u collab-server -e"
	fi

	ipaddr=$(primary_ipv4)
	if [ -z "$ipaddr" ]; then
		ipaddr="<ip-address>"
	fi
	printf '\n'
	info "Installed. Data: $DATA_DIR"
	info "URL for the plugin: ws://${ipaddr}:${PORT}/ws"
	info "Status: systemctl status collab-server"
	info "Logs:   journalctl -u collab-server -f"
	info "Stop:   sudo systemctl stop collab-server"
}

uninstall_server() {
	need_root
	if [ -f "$UNIT_FILE" ]; then
		unit_port=$(sed -n 's/^Environment=COLLAB_PORT=//p' "$UNIT_FILE" | tr -d '[:space:]' | head -n 1)
		case "$unit_port" in
			''|*[!0-9]*) ;;
			*) PORT=$unit_port ;;
		esac
	fi
	close_firewall
	if command -v systemctl >/dev/null 2>&1; then
		systemctl disable --now collab-server >/dev/null 2>&1 || true
	fi
	remove_update_units
	rm -f "$UNIT_FILE" "$INSTALL_BIN"
	rmdir /opt/editor-collab 2>/dev/null || true
	if command -v systemctl >/dev/null 2>&1; then
		systemctl daemon-reload
	fi
	if [ "${PURGE:-0}" -eq 1 ]; then
		rm -rf "$DATA_DIR"
		rm -f "$ENV_FILE"
		info "Service, binary, data, and password removed."
	else
		info "Service and binary removed."
		info "Data kept: $DATA_DIR and $ENV_FILE"
		info "To remove everything: sudo sh install-debian.sh --purge"
	fi
}

FORCE=0
UNINSTALL=0
PURGE=0
UPDATE=0
while [ $# -gt 0 ]; do
	case "$1" in
		--force) FORCE=1 ;;
		--uninstall) UNINSTALL=1 ;;
		--purge) UNINSTALL=1; PURGE=1 ;;
		--update) UPDATE=1 ;;
		-h|--help) usage; exit 0 ;;
		*) die "Unknown option: $1 (try --help)" ;;
	esac
	shift
done

if [ "$UPDATE" -eq 1 ] && [ "$UNINSTALL" -eq 1 ]; then
	die "Use either --update or --uninstall."
fi

if [ "$UPDATE" -eq 1 ]; then
	update_server
elif [ "$UNINSTALL" -eq 1 ]; then
	uninstall_server
else
	install_server
fi
