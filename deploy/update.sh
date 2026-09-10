#!/usr/bin/env bash
#
# One command to replace a running npc or nps with a release build.
#
#   curl -fsSL https://raw.githubusercontent.com/hector918/nps/<tag>/deploy/update.sh | sudo bash
#   sudo update.sh --tag v0.27.0-hz1     pin a release instead of taking the latest
#   sudo update.sh --file /tmp/npc       install a binary already on this host
#   sudo update.sh --rollback            put the previous binary back
#   DRY_RUN=1 sudo update.sh             show what would happen, change nothing
#
# It touches nothing but the binary. The service unit, its flags and any
# npc.conf are left exactly as they are, so a node started with -config= and a
# node started with bare flags need no special handling.
#
# Fetch this script from a tag rather than from master: piping it to a root
# shell hands every node to whoever can write to the branch it came from.
set -euo pipefail

REPO=${REPO:-hector918/nps}
WAIT_SECS=${WAIT_SECS:-30}
KEEP_BACKUPS=${KEEP_BACKUPS:-3}
DRY_RUN=${DRY_RUN:-0}

TAG=""
LOCAL_FILE=""
ROLLBACK=0

# Spelled out rather than read back out of the file: the usual way to run this
# is piped into bash, where the script is stdin and $0 is "bash".
usage() {
    cat <<'USAGE'
update.sh -- replace a running npc or nps with a release build

  curl -fsSL https://raw.githubusercontent.com/hector918/nps/master/deploy/update.sh | sudo bash
  ... | sudo bash -s -- --tag v0.27.1-hz1   install a specific release
  ... | sudo bash -s -- --rollback          put the previous binary back

  --tag TAG      release to install, default the latest one
  --file PATH    install a binary already on this host instead of downloading
  --rollback     restore the most recent backup
  -h, --help     this text

  REPO=owner/name   pull from a different repository
  WAIT_SECS=30      how long to wait for the tunnel to come back
  DRY_RUN=1         show what would happen, change nothing
USAGE
}

info() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
run()  { if [[ $DRY_RUN == 1 ]]; then printf '  would run: %s\n' "$*"; else "$@"; fi; }

while [[ $# -gt 0 ]]; do
    case $1 in
        --tag)      TAG=${2:?--tag needs a value}; shift 2 ;;
        --file)     LOCAL_FILE=${2:?--file needs a path}; shift 2 ;;
        --rollback) ROLLBACK=1; shift ;;
        -h|--help)  usage; exit 0 ;;
        *)          die "unknown argument: $1" ;;
    esac
done

[[ $EUID -eq 0 ]] || die "must run as root"

# ---------------------------------------------------------------- locate ----
# The running process is the most reliable source of truth: its exe link gives
# the binary and its cgroup gives the unit, whatever that unit is called
# (Npc.service, npc.service, nps.service, ...).
ROLE=""
PID=""
for candidate in npc nps; do
    PID=$(pgrep -x "$candidate" | head -1 || true)
    if [[ -n $PID ]]; then ROLE=$candidate; break; fi
done

if [[ -n $PID ]]; then
    BIN=$(readlink "/proc/$PID/exe" | sed 's/ (deleted)$//')
    UNIT=$(grep -o '[^/]*\.service' "/proc/$PID/cgroup" | head -1 || true)
else
    info "neither npc nor nps is running, falling back to a unit lookup"
    UNIT=$(systemctl list-units --type=service --all --plain --no-legend 2>/dev/null \
           | awk '{print $1}' | grep -iE '^np[cs].*\.service$' | head -1 || true)
    [[ -n $UNIT ]] || die "cannot find an npc or nps service unit"
    BIN=$(systemctl show "$UNIT" -p ExecStart --value \
          | sed -n 's/.*path=\([^ ;]*\).*/\1/p' | head -1)
    ROLE=$(basename "${BIN:-}")
fi
[[ -n ${UNIT:-} ]]        || die "cannot determine the service unit"
[[ -n ${BIN:-} && -f $BIN ]] || die "cannot find the $ROLE binary (unit $UNIT)"

info "role:   $ROLE"
info "unit:   $UNIT"
info "binary: $BIN"

# -------------------------------------------------------------- rollback ----
if [[ $ROLLBACK == 1 ]]; then
    BACKUP=$(ls -1t "$BIN".bak.* 2>/dev/null | head -1 || true)
    [[ -n $BACKUP ]] || die "no backup found next to $BIN"
    info "restoring $BACKUP"
    run mv -f "$BACKUP" "$BIN"
    run systemctl restart "$UNIT"
    info "rolled back"
    exit 0
fi

# ------------------------------------------------------------------ fetch ---
# Staged beside the target binary, not in /tmp: the smoke test below executes
# it, and /tmp is mounted noexec on plenty of hardened hosts. It also keeps the
# final move a rename on one filesystem.
WORK=$(mktemp -d "$(dirname "$BIN")/.update.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

if [[ -n $LOCAL_FILE ]]; then
    [[ -f $LOCAL_FILE ]] || die "no such file: $LOCAL_FILE"
    cp "$LOCAL_FILE" "$WORK/staged"
    info "using local file $LOCAL_FILE"
else
    # These must be the GOARCH values build.release.sh labels its output with,
    # not finer-grained ones: asking for linux_arm_v7 when the builder produced
    # linux_arm just 404s.
    case "$(uname -m)" in
        x86_64)          ARCH=amd64 ;;
        aarch64|arm64)   ARCH=arm64 ;;
        armv7l|armv6l)   ARCH=arm ;;
        i386|i686)       ARCH=386 ;;
        *)               die "unsupported architecture: $(uname -m)" ;;
    esac
    KIND=client
    [[ $ROLE == nps ]] && KIND=server
    ASSET="linux_${ARCH}_${KIND}.tar.gz"

    if [[ -n $TAG ]]; then
        BASE="https://github.com/${REPO}/releases/download/${TAG}"
    else
        # This redirect always resolves to the newest release and, unlike the
        # API, is not rate limited per source IP -- which matters when many
        # nodes sit behind one address.
        BASE="https://github.com/${REPO}/releases/latest/download"
    fi

    info "fetching $BASE/$ASSET"
    curl -fsSL -o "$WORK/$ASSET" "$BASE/$ASSET" \
        || die "cannot download $ASSET (does the release exist?)"
    curl -fsSL -o "$WORK/sha256sums.txt" "$BASE/sha256sums.txt" \
        || die "cannot download sha256sums.txt"

    WANT=$(awk -v a="$ASSET" '$2 == a || $2 == "./" a {print $1}' "$WORK/sha256sums.txt" | head -1)
    [[ -n $WANT ]] || die "$ASSET is not listed in sha256sums.txt"
    GOT=$(sha256sum "$WORK/$ASSET" | cut -d' ' -f1)
    [[ $WANT == "$GOT" ]] || die "checksum mismatch: got $GOT, want $WANT"
    info "checksum ok"

    # Only the binary comes out. These tarballs also carry conf/npc.conf and,
    # for the server, web/views and web/static. Unpacking the config over a
    # live node would replace its vkey and every tunnel it serves with the
    # template, on every node at once.
    tar -xzOf "$WORK/$ASSET" "$ROLE" > "$WORK/staged" \
        || die "no $ROLE inside $ASSET"
fi

chmod 0755 "$WORK/staged"

# ---------------------------------------------------------------- verify ----
file -b "$WORK/staged" | grep -q 'ELF' || die "the staged file is not an ELF binary"
if cmp -s "$WORK/staged" "$BIN"; then
    info "already running this exact binary, nothing to do"
    exit 0
fi

# -version prints and exits on both npc and nps. Never use a bare word here:
# npc treats an unrecognised argument as "start the service".
if ! STAGED_VERSION=$("$WORK/staged" -version 2>&1 | head -1); then
    die "the staged binary failed its smoke test: $STAGED_VERSION"
fi
info "staged: $STAGED_VERSION"
info "current: $("$BIN" -version 2>/dev/null | head -1 || echo unknown)"

# --------------------------------------------------------------- install ----
BACKUP="$BIN.bak.$(date +%Y%m%d-%H%M%S)"
info "backing up to $BACKUP"
run cp -a "$BIN" "$BACKUP"

# A running executable cannot be overwritten (ETXTBSY) but its directory entry
# can be replaced: stage alongside, then rename.
run install -m 0755 "$WORK/staged" "$BIN.new"
run chown --reference="$BACKUP" "$BIN.new"
run mv -f "$BIN.new" "$BIN"

info "restarting $UNIT"
run systemctl restart "$UNIT"

if [[ $DRY_RUN == 1 ]]; then
    info "dry run complete, nothing was changed"
    exit 0
fi

# ---------------------------------------------------------------- prove -----
# Readiness differs by role and neither form depends on the node's log level.
# A client proves itself by connecting out to its server; a server proves
# itself by listening again.
info "waiting up to ${WAIT_SECS}s for $ROLE to come back"
ok=0
deadline=$((SECONDS + WAIT_SECS))
while ((SECONDS < deadline)); do
    if systemctl is-active --quiet "$UNIT"; then
        NEW_PID=$(pgrep -x "$ROLE" | head -1 || true)
        if [[ -n $NEW_PID ]]; then
            if [[ $ROLE == nps ]]; then
                ss -tlnp 2>/dev/null | grep -q "pid=$NEW_PID," && { ok=1; break; }
            else
                # TCP for the usual bridge, UDP for a node whose bridge is
                # kcp -- checking only TCP would roll back every healthy kcp
                # node, every time.
                ss -tnp state established 2>/dev/null | grep -q "pid=$NEW_PID," && { ok=1; break; }
                ss -unp 2>/dev/null | grep -q "pid=$NEW_PID," && { ok=1; break; }
            fi
        fi
    fi
    sleep 1
done

if ((ok == 0)); then
    info "FAILED to come up, rolling back"
    mv -f "$BACKUP" "$BIN"
    systemctl restart "$UNIT"
    sleep 3
    systemctl is-active --quiet "$UNIT" \
        && die "rolled back, this node is up on the previous binary" \
        || die "rolled back but $UNIT is still not active, needs a look"
fi

# --------------------------------------------------------------- cleanup ----
mapfile -t OLD < <(ls -1t "$BIN".bak.* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)))
for f in "${OLD[@]:-}"; do [[ -n $f ]] && rm -f "$f" && info "pruned $f"; done

info "done. $ROLE is up on $STAGED_VERSION"
