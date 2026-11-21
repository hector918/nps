#!/usr/bin/env bash
#
# One command to install npc on a host that has none, register it as a systemd
# service and start it.
#
#   curl -fsSL https://github.com/hector918/nps/releases/latest/download/install.sh \
#     | sudo bash -s -- --server nps.example.com:8024 --vkey YOUR_VKEY
#
#   ... --config /etc/npc/npc.conf      start from a config file instead
#   (no --server/--vkey/--config: asks on the terminal, and for a config file
#    lets you paste its contents)
#   ... --type kcp                      bridge type, default tcp
#   ... --tag v0.27.15-hz1              pin a release instead of the latest
#   ... --file /tmp/npc                 install a binary already on this host
#   NPC_SERVER=... NPC_VKEY=... sudo -E bash install.sh   same, from the environment
#
# The vkey ends up in the service's arguments, so it is visible in ps and in
# the unit file (made root-only here). Use --config to keep it out of both.
#
# Refuses to run over an existing npc: use update.sh to upgrade one. Like
# update.sh it only ever takes the script from a release, never from a branch.
set -euo pipefail

REPO=${REPO:-hector918/nps}
SERVER=${NPC_SERVER:-}
VKEY=${NPC_VKEY:-}
TYPE=${NPC_TYPE:-tcp}
CONFIG=""
TAG=""
LOCAL_FILE=""

usage() {
    cat <<'USAGE'
install.sh -- install npc as a service on a fresh host

  curl -fsSL https://github.com/hector918/nps/releases/latest/download/install.sh \
    | sudo bash -s -- --server HOST:8024 --vkey KEY

  --server ADDR  nps bridge address (or NPC_SERVER)
  --vkey KEY     client vkey (or NPC_VKEY). Shown in ps; --config avoids that
  --type TYPE    tcp (default) or kcp, must match the server's bridge_type
  --config PATH  use a config file instead of --server/--vkey
  (none of these: ask interactively; a config can be pasted)
  --tag TAG      release to install, default the latest one
  --file PATH    install an npc binary already on this host, no download
  -h, --help     this text
USAGE
}

info() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case $1 in
        --server) SERVER=${2:?--server needs a value}; shift 2 ;;
        --vkey)   VKEY=${2:?--vkey needs a value};     shift 2 ;;
        --type)   TYPE=${2:?--type needs a value};     shift 2 ;;
        --config) CONFIG=${2:?--config needs a path};  shift 2 ;;
        --tag)    TAG=${2:?--tag needs a value};       shift 2 ;;
        --file)   LOCAL_FILE=${2:?--file needs a path}; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

[[ $EUID -eq 0 ]] || die "must run as root"
command -v systemctl >/dev/null 2>&1 || die "this script needs systemd"
[[ $(uname -s) == Linux ]] || die "Linux only"

# Captured first: grep -q exiting early would SIGPIPE systemctl and, under
# pipefail, make a found unit look absent.
UNITS=$(systemctl list-unit-files 2>/dev/null || true)
if pgrep -x npc >/dev/null 2>&1 || grep -qi '^npc\.service' <<<"$UNITS"; then
    die "npc is already installed or running here. Upgrade it with update.sh; to reinstall, run 'npc stop; npc uninstall' first"
fi

case "$(uname -m)" in
    # Only what build.release.sh publishes by default.
    x86_64)        ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture: $(uname -m) (releases carry amd64 and arm64)" ;;
esac

# ------------------------------------------------------------ how to start ---
# With no --server/--vkey/--config, ask. Run as `curl | bash` the script itself
# is stdin, so the answers come from the terminal, not from stdin.
CONF_DIR=/etc/npc
if [[ -z $CONFIG && ( -z $SERVER || -z $VKEY ) ]]; then
    { exec 3</dev/tty; } 2>/dev/null || die "need --server and --vkey (or --config); no terminal to ask on. See --help"
    echo
    echo "How should npc be started?"
    echo "  1) command-line arguments (server address + vkey)"
    echo "  2) a config file (paste its contents)"
    while :; do
        read -r -u 3 -p "Choose [1/2]: " CHOICE || die "no answer"
        [[ $CHOICE == 1 || $CHOICE == 2 ]] && break
    done
    if [[ $CHOICE == 1 ]]; then
        while [[ -z $SERVER ]]; do read -r -u 3 -p "Server address (host:port, e.g. 1.2.3.4:8024): " SERVER || die "no answer"; done
        while [[ -z $VKEY ]]; do read -rs -u 3 -p "vkey (hidden): " VKEY || die "no answer"; echo; done
    else
        echo "Paste the config now. Finish with a line containing only EOF, or press Ctrl-D."
        PASTED=""
        while IFS= read -r -u 3 LINE; do
            [[ $LINE == EOF ]] && break
            PASTED+="$LINE"$'\n'
        done
        grep -q '^\[common\]' <<<"$PASTED" || die "that is not an npc config: no [common] section"
        grep -qE '^[[:space:]]*server_addr[[:space:]]*=[[:space:]]*[^[:space:]]' <<<"$PASTED" || die "the config has no server_addr"
        grep -qE '^[[:space:]]*vkey[[:space:]]*=[[:space:]]*[^[:space:]]' <<<"$PASTED" || die "the config has no vkey"
        # npc silently never connects when conn_type is missing, so add it.
        if ! grep -qE '^[[:space:]]*conn_type[[:space:]]*=' <<<"$PASTED"; then
            PASTED=$(awk -v t="$TYPE" '{print} /^\[common\]/ && !d {print "conn_type=" t; d=1}' <<<"$PASTED")$'\n'
            info "the config had no conn_type, added conn_type=$TYPE (must match the server's bridge_type)"
        fi
        mkdir -p "$CONF_DIR"
        # The config holds the vkey: root-only. Never overwrite one silently.
        if [[ -e $CONF_DIR/npc.conf ]]; then
            cp -a "$CONF_DIR/npc.conf" "$CONF_DIR/npc.conf.bak.$(date +%Y%m%d-%H%M%S)"
            info "kept the previous $CONF_DIR/npc.conf as npc.conf.bak.*"
        fi
        ( umask 077; printf '%s' "$PASTED" > "$CONF_DIR/npc.conf" )
        CONFIG=$CONF_DIR/npc.conf
        info "wrote $CONFIG"
    fi
    exec 3<&-
fi

if [[ -n $CONFIG ]]; then
    [[ -f $CONFIG ]] || die "no such config file: $CONFIG"
    CONFIG=$(readlink -f "$CONFIG")
    ARGS=("-config=$CONFIG")
    grep -qE '^[[:space:]]*conn_type[[:space:]]*=' "$CONFIG" \
        || info "WARNING: $CONFIG has no conn_type; npc will not connect without it (add conn_type=tcp)"
else
    [[ $TYPE == tcp || $TYPE == kcp ]] || die "--type must be tcp or kcp"
    ARGS=("-server=$SERVER" "-vkey=$VKEY" "-type=$TYPE")
fi
ASSET="linux_${ARCH}_client.tar.gz"
if [[ -n $TAG ]]; then
    BASE="https://github.com/${REPO}/releases/download/${TAG}"
else
    BASE="https://github.com/${REPO}/releases/latest/download"
fi

# Not /tmp: it is noexec on hardened hosts.
WORK=$(mktemp -d /var/tmp/npc-install.XXXXXX)
trap 'rm -rf "$WORK"' EXIT

if [[ -n $LOCAL_FILE ]]; then
    [[ -f $LOCAL_FILE ]] || die "no such file: $LOCAL_FILE"
    cp "$LOCAL_FILE" "$WORK/npc"
    info "using local file $LOCAL_FILE"
else
    info "fetching $BASE/$ASSET"
    curl -fsSL -o "$WORK/$ASSET" "$BASE/$ASSET" || die "cannot download $ASSET (does the release exist?)"
    curl -fsSL -o "$WORK/sha256sums.txt" "$BASE/sha256sums.txt" || die "cannot download sha256sums.txt"
    WANT=$(awk -v a="$ASSET" '$2 == a || $2 == "./" a {print $1}' "$WORK/sha256sums.txt" | head -1)
    [[ -n $WANT ]] || die "$ASSET is not listed in sha256sums.txt"
    GOT=$(sha256sum "$WORK/$ASSET" | cut -d' ' -f1)
    [[ $WANT == "$GOT" ]] || die "checksum mismatch: got $GOT, want $WANT"
    info "checksum ok"

    # Only the binary: the tarball also carries a template npc.conf.
    tar -xzOf "$WORK/$ASSET" npc > "$WORK/npc" || die "no npc inside $ASSET"
fi
chmod 0755 "$WORK/npc"
if [[ $(od -An -tx1 -N4 < "$WORK/npc" | tr -d ' \n') != 7f454c46 ]]; then
    die "the downloaded file is not an ELF binary"
fi
info "version: $("$WORK/npc" -version 2>&1 | head -1)"

# The unit's ExecStart is the path npc install runs from, so that path has to
# outlive this script: run it from a permanent directory, not $WORK. (npc
# install also copies itself to /usr/bin/npc; that copy is for the shell.)
INSTALL_DIR=/opt/npc
mkdir -p "$INSTALL_DIR"
install -m 0755 "$WORK/npc" "$INSTALL_DIR/npc"
info "registering the service"
"$INSTALL_DIR/npc" install "${ARGS[@]}"
# npc install logs its failures and still exits 0, so look at the result.
[[ -s /etc/systemd/system/Npc.service ]] || die "npc install did not write /etc/systemd/system/Npc.service; see its output above"
# The unit carries the vkey in its arguments.
chmod 0600 /etc/systemd/system/Npc.service 2>/dev/null || true
info "starting"
"$INSTALL_DIR/npc" start

# Active only means the process is up; a wrong --server or --vkey leaves it
# active and unconnected. So when ss is available, wait for a connection.
up=0
for _ in $(seq 1 15); do
    systemctl is-active --quiet Npc && { up=1; break; }
    sleep 1
done
[[ $up == 1 ]] || die "npc was installed but the service is not active: journalctl -u Npc -n 50"
if command -v ss >/dev/null 2>&1; then
    for _ in $(seq 1 20); do
        PID=$(pgrep -x npc | head -1 || true)
        if [[ -n $PID ]] && { ss -tnp state established 2>/dev/null | grep -q "pid=$PID," ||
                              ss -unp 2>/dev/null | grep -q "pid=$PID,"; }; then
            info "done. npc is running and connected to the server"
            exit 0
        fi
        sleep 1
    done
    info "WARNING: npc is running but has not connected to the server. Check --server and --vkey: journalctl -u Npc -n 50"
    exit 1
fi
info "done. npc is running (no ss here, so the connection was not checked): systemctl status Npc"
