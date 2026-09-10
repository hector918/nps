#!/usr/bin/env bash
#
# update-npc.sh -- swap the npc binary in place, restart, verify, roll back on failure.
#
# It touches nothing except the binary itself. The systemd unit, its flags and
# any npc.conf stay exactly as they were, so a node started with -config= and a
# node started with bare flags are both handled by doing nothing special to them.
#
#   update-npc.sh /tmp/npc            install a new binary
#   update-npc.sh --rollback          restore the most recent backup
#   DRY_RUN=1 update-npc.sh /tmp/npc  show what would happen, change nothing
#
set -euo pipefail

WAIT_SECS=${WAIT_SECS:-30}
KEEP_BACKUPS=${KEEP_BACKUPS:-3}
DRY_RUN=${DRY_RUN:-0}

info() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
run()  { if [[ $DRY_RUN == 1 ]]; then printf '  would run: %s\n' "$*"; else "$@"; fi; }

[[ $EUID -eq 0 ]] || die "must run as root"

# ---------------------------------------------------------------- locate ----
# The running process is the most reliable source of truth: its exe link gives
# the binary and its cgroup gives the unit, whatever the unit happens to be
# called (Npc.service, npc.service, ...).
PID=$(pgrep -x npc | head -1 || true)
if [[ -n $PID ]]; then
    BIN=$(readlink "/proc/$PID/exe" | sed 's/ (deleted)$//')
    UNIT=$(grep -o '[^/]*\.service' "/proc/$PID/cgroup" | head -1 || true)
else
    info "npc is not running, falling back to unit lookup"
    UNIT=$(systemctl list-units --type=service --all --plain --no-legend 2>/dev/null \
           | awk '{print $1}' | grep -i '^npc.*\.service$' | head -1 || true)
fi
[[ -n ${UNIT:-} ]] || die "cannot find an npc service unit"
[[ -n ${BIN:-} ]]  || BIN=$(systemctl show "$UNIT" -p ExecStart --value \
                            | sed -n 's/.*path=\([^ ;]*\).*/\1/p' | head -1)
[[ -n $BIN && -f $BIN ]] || die "cannot find the npc binary (unit $UNIT)"

info "unit:   $UNIT"
info "binary: $BIN"

# -------------------------------------------------------------- rollback ----
if [[ ${1:-} == --rollback ]]; then
    BACKUP=$(ls -1t "$BIN".bak.* 2>/dev/null | head -1 || true)
    [[ -n $BACKUP ]] || die "no backup found next to $BIN"
    info "restoring $BACKUP"
    run mv -f "$BACKUP" "$BIN"
    run systemctl restart "$UNIT"
    info "rolled back"
    exit 0
fi

NEW_BIN=${1:-}
[[ -n $NEW_BIN ]] || die "usage: $0 /path/to/new/npc  |  $0 --rollback"
[[ -f $NEW_BIN ]] || die "no such file: $NEW_BIN"

# ------------------------------------------------------------- sanitise -----
# Static checks only. The new binary is deliberately NOT executed here: npc
# falls through to "run the service" for any argument it does not recognise,
# so a smoke run risks starting a second client. Liveness is proven after the
# restart instead, with a rollback if it fails.
file -b "$NEW_BIN" | grep -q 'ELF 64-bit' || die "$NEW_BIN is not a 64-bit ELF binary"
file -b "$NEW_BIN" | grep -q 'statically linked' \
    || info "WARNING: $NEW_BIN is not statically linked, check glibc on this host"
if cmp -s "$NEW_BIN" "$BIN"; then
    info "already running this exact binary, nothing to do"
    exit 0
fi
info "new:    $(sha256sum "$NEW_BIN" | cut -c1-16)...  $(stat -c%s "$NEW_BIN") bytes"
info "old:    $(sha256sum "$BIN"     | cut -c1-16)...  $(stat -c%s "$BIN") bytes"

# --------------------------------------------------------------- install ----
BACKUP="$BIN.bak.$(date +%Y%m%d-%H%M%S)"
info "backing up to $BACKUP"
run cp -a "$BIN" "$BACKUP"

# A running executable cannot be overwritten in place (ETXTBSY), but its
# directory entry can be replaced: stage alongside, then rename atomically.
info "installing new binary"
run install -m 0755 "$NEW_BIN" "$BIN.new"
run chown --reference="$BACKUP" "$BIN.new"
run mv -f "$BIN.new" "$BIN"

# --------------------------------------------------------------- restart ----
LOG_PATH=$(systemctl show "$UNIT" -p ExecStart --value \
           | grep -o '\-log_path=[^" ]*' | cut -d= -f2 | head -1 || true)
LOG_MARK=0
[[ -n $LOG_PATH && -f $LOG_PATH ]] && LOG_MARK=$(stat -c%s "$LOG_PATH")

info "restarting $UNIT"
run systemctl restart "$UNIT"

if [[ $DRY_RUN == 1 ]]; then
    info "dry run complete, nothing was changed"
    exit 0
fi

# ---------------------------------------------------------------- verify ----
# Readiness is "the client has an established connection to its server", which
# holds regardless of the node's log level. A matching log line, when the level
# is verbose enough to produce one, is treated as a bonus.
info "waiting up to ${WAIT_SECS}s for the tunnel to come back"
ok=0
deadline=$((SECONDS + WAIT_SECS))
while ((SECONDS < deadline)); do
    if systemctl is-active --quiet "$UNIT"; then
        NEW_PID=$(pgrep -x npc | head -1 || true)
        if [[ -n $NEW_PID ]] \
           && ss -tnp state established 2>/dev/null | grep -q "pid=$NEW_PID,"; then
            ok=1
            break
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
        && die "rolled back to the previous binary, node is up on the old version" \
        || die "rolled back but $UNIT is still not active, needs a look"
fi

if [[ -n $LOG_PATH && -f $LOG_PATH ]] \
   && tail -c "+$((LOG_MARK + 1))" "$LOG_PATH" 2>/dev/null \
      | grep -q 'Successful connection with server'; then
    info "log confirms: connected to server"
fi

# --------------------------------------------------------------- cleanup ----
mapfile -t OLD < <(ls -1t "$BIN".bak.* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)))
for f in "${OLD[@]:-}"; do [[ -n $f ]] && rm -f "$f" && info "pruned $f"; done

CPU=$(ps -o %cpu= -p "$(pgrep -x npc | head -1)" | tr -d ' ')
info "done. npc is up, cpu ${CPU}%"
