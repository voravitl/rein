#!/bin/sh
# SessionStart hook of the rein plugin: make sure ${CLAUDE_PLUGIN_DATA}/bin/rein matches this plugin's source.
# Plugins have no install step, so the binary is built (Go) or downloaded (GitHub release, checksum-verified) the
# first time a session starts after an install or update. Silent when the binary is current. On failure it prints
# one line, which Claude Code adds to the context of the session that started (worker or coordinator), so it knows
# the guard is not active.
# Also usable by hand: CLAUDE_PLUGIN_ROOT=<repo> CLAUDE_PLUGIN_DATA=<dir> sh scripts/ensure-binary.sh [--force]
#
# Correctness does not depend on the lock: every installer writes its own temp file, validates it, publishes it with
# an atomic mv, and writes the stamp last (it is removed before publishing). The lock only saves duplicate work.
# Everything runs inside one deadline that leaves room in the hook's 300 s timeout for the final message.
set -u
START=$(date +%s); DEADLINE=$((START + 270))
left() { echo $((DEADLINE - $(date +%s))); }
ROOT=${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}
DATA=${CLAUDE_PLUGIN_DATA:-${XDG_DATA_HOME:-$HOME/.local/share}/rein}
REPO=${REIN_RELEASE_REPO:-voravitl/rein}
exe=; case $(uname -s) in MINGW*|MSYS*|CYGWIN*|Windows_NT) exe=.exe;; esac
BIN="$DATA/bin/rein$exe"; STAMP="$DATA/bin/.source-stamp"; BUILDFAIL="$DATA/bin/.build-failed"
FORCE=; [ "${1:-}" = "--force" ] && FORCE=1

say() { echo "rein: $*"; }
# run_bounded <seconds> <cmd...>: run a command, kill it when the time is up (exit 124).
run_bounded() {
  secs=$1; shift
  [ "$secs" -gt 0 ] || return 124
  "$@" & rb_pid=$!
  rb_i=0
  while kill -0 "$rb_pid" 2>/dev/null; do
    [ "$rb_i" -ge "$secs" ] && { kill "$rb_pid" 2>/dev/null; wait "$rb_pid" 2>/dev/null; return 124; }
    sleep 1; rb_i=$((rb_i + 1))
  done
  wait "$rb_pid"
}

# The stamp is a checksum of the Go sources, so an update that changes no code costs nothing.
[ -f "$ROOT/go.mod" ] && [ -d "$ROOT/cmd/rein" ] || { say "cannot read the plugin source at $ROOT; guard binary not checked"; exit 0; }
stamp=$(cd "$ROOT" && find cmd internal -name '*.go' | LC_ALL=C sort | xargs cat go.mod go.sum | cksum | cut -d' ' -f1)

# Windows: the hooks name ".../bin/rein" without an extension, so both files must be in place.
installed() { [ -x "$BIN" ] && { [ -z "$exe" ] || [ -f "$DATA/bin/rein" ]; }; }
GOVER=; HAVE_GO=
probe_go() {   # lazily, and outside the module dir so GOTOOLCHAIN=auto cannot start a toolchain download here
  [ -n "$HAVE_GO" ] && return 0
  command -v go >/dev/null 2>&1 || { HAVE_GO=no; return 0; }
  # GOTOOLCHAIN=local: report the installed toolchain, never start a toolchain download; bounded like everything else
  HAVE_GO=yes; GOVER=$(cd "$DATA" 2>/dev/null && GOTOOLCHAIN=local run_bounded 20 go env GOVERSION 2>/dev/null)
}
# A build that failed with this source and this Go is not retried on every start, but only for 6 hours (a network
# hiccup during the module download must not pin an older release binary forever), and only while a working binary
# exists: with nothing installed every start retries (the deadline caps the cost). --force always retries.
build_blocked() {
  [ -z "$FORCE" ] && installed || return 1
  set -- $(cat "$BUILDFAIL" 2>/dev/null)
  [ "${1:-}" = "$stamp" ] && [ "${2:-}" = "$GOVER" ] && [ $(( $(date +%s) - ${3:-0} )) -lt 21600 ]
}
# The stamp file says what was installed; the binary must agree, so a stamp can never vouch for another binary (two
# installers racing): a source build reports "rein <version>+<stamp>", a release download "rein <version>".
# A downloaded binary is stamped "dl-<stamp>": it may be older than the source, so it is current only while no
# usable Go is present.
version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$ROOT/.claude-plugin/plugin.json" | head -1)
current() {
  [ -z "$FORCE" ] && installed || return 1
  have=$(cat "$STAMP" 2>/dev/null); gotv=$("$BIN" version 2>/dev/null)
  # Windows: the hooks run the extensionless copy, so it must be the same build
  [ -z "$exe" ] || [ "$("$DATA/bin/rein" version 2>/dev/null)" = "$gotv" ] || return 1
  [ "$have" = "$stamp" ] && [ "$gotv" = "rein ${version:-dev}+$stamp" ] && return 0
  [ "$have" = "dl-$stamp" ] && [ "$gotv" = "rein $version" ] || return 1
  probe_go; [ "$HAVE_GO" = no ] || build_blocked
}
current && exit 0

mkdir -p "$DATA/bin" || { say "cannot create $DATA/bin"; exit 0; }
LOCK="$DATA/bin/.lock"
while ! mkdir "$LOCK" 2>/dev/null; do
  owner=$(cat "$LOCK/pid" 2>/dev/null)
  # dead owner; no pid after a minute (killed between mkdir and writing it); or older than 10 minutes
  if { [ -n "$owner" ] && ! kill -0 "$owner" 2>/dev/null; } || { [ -z "$owner" ] && [ -n "$(find "$LOCK" -prune -mmin +1 2>/dev/null)" ]; } || [ -n "$(find "$LOCK" -prune -mmin +10 2>/dev/null)" ]; then
    mv "$LOCK" "$LOCK.stale.$$" 2>/dev/null && rm -rf "$LOCK.stale.$$"; continue
  fi
  current && exit 0
  [ "$(left)" -le 150 ] && { say "timed out waiting for another session to install the binary; run /rein:setup"; exit 0; }
  sleep 1
done
echo $$ > "$LOCK/pid"
release() { [ "$(cat "$LOCK/pid" 2>/dev/null)" = "$$" ] && rm -rf "$LOCK"; }   # never another owner's lock
trap release EXIT
trap 'release; exit 1' INT TERM HUP
current && exit 0   # another session finished while we waited

TMP="$DATA/bin/rein.tmp.$$$exe"
how=; newstamp=$stamp; built=
probe_go
skipped=
if [ "$HAVE_GO" = yes ] && build_blocked; then skipped=1; fi
if [ "$HAVE_GO" = yes ] && [ -z "$skipped" ]; then
  # keep at least 60 s of the deadline for a release download if the build fails or hangs
  if (cd "$ROOT" && export GOOS= GOARCH= CGO_ENABLED=0 && run_bounded $(( $(left) - 60 )) go build -trimpath -ldflags "-s -w -X main.version=${version:-dev}+$stamp" -o "$TMP" ./cmd/rein) >"$DATA/bin/build.log" 2>&1; then
    how="built with $GOVER"; built=1; rm -f "$BUILDFAIL"
  else
    rm -f "$TMP"; echo "$stamp $GOVER $(date +%s)" > "$BUILDFAIL"
  fi
fi
if [ -z "$how" ] && [ -n "$version" ] && command -v curl >/dev/null 2>&1 && [ "$(left)" -gt 20 ]; then
  os=$(uname -s | tr '[:upper:]' '[:lower:]'); case "$os" in mingw*|msys*|cygwin*|windows_nt) os=windows;; esac
  arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; esac
  asset="rein-$os-$arch$exe"; url=${REIN_RELEASE_URL:-https://github.com/$REPO/releases/download/v$version}
  t=$(( $(left) - 15 )); [ "$t" -gt 90 ] && t=90
  if curl -fsL --max-time "$t" -o "$TMP" "$url/$asset" && curl -fsL --max-time 10 -o "$TMP.sums" "$url/SHA256SUMS"; then
    want=$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "$TMP.sums")
    got=$( (sha256sum "$TMP" 2>/dev/null || shasum -a 256 "$TMP") | cut -d' ' -f1)
    if [ -n "$want" ] && [ "$want" = "$got" ]; then how="downloaded release v$version (may be older than this source; a session with a working Go rebuilds it)"; newstamp="dl-$stamp"; else rm -f "$TMP"; say "checksum mismatch for $asset; not installed"; fi
  else
    rm -f "$TMP"
  fi
  rm -f "$TMP.sums"
fi
if [ -z "$how" ]; then
  if [ -n "$skipped" ]; then why="build skipped: it failed recently with $GOVER (see $DATA/bin/build.log); /rein:setup retries now"
  elif [ "$HAVE_GO" = yes ]; then why="go build failed or timed out (see $DATA/bin/build.log) and no release binary for v${version:-?}"
  else why="no Go toolchain (1.26+) and no release binary for v${version:-?}"; fi
  if [ -x "$BIN" ]; then say "guard binary NOT updated ($why); the previous guard is still active. Run /rein:setup."
  else say "guard binary NOT installed ($why); the rein hooks cannot run in this session. Install Go 1.26+ and start a new session, or run /rein:setup."; fi
  exit 0
fi
# Validate the candidate before it replaces anything (a wrong-platform or broken file must never be stamped).
if ! { chmod +x "$TMP" && "$TMP" version >/dev/null 2>&1; }; then
  rm -f "$TMP"; say "the new binary does not run on this machine ($how); not installed. Run /rein:setup."; exit 0
fi
rm -f "$STAMP"   # from here until the last step, nothing counts as current: a failure is retried next start
if ! mv -f "$TMP" "$BIN"; then
  rm -f "$TMP"; say "could not replace $BIN (in use by another session?); will retry at the next session start"; exit 0
fi
# Windows: refresh the extensionless copy through its own temp file and a move, never a copy over the live file.
if [ -n "$exe" ] && ! { cp -f "$BIN" "$DATA/bin/rein.copy.$$" && mv -f "$DATA/bin/rein.copy.$$" "$DATA/bin/rein"; } 2>/dev/null; then
  rm -f "$DATA/bin/rein.copy.$$"; say "installed $BIN but could not refresh the extensionless copy the hooks name (in use?); will retry at the next session start"; exit 0
fi
"$BIN" version >/dev/null 2>&1 || { say "installed $BIN but it does not run; see /rein:setup"; exit 0; }
[ -z "$exe" ] || "$DATA/bin/rein" version >/dev/null 2>&1 || { say "the hook path $DATA/bin/rein does not run; see /rein:setup"; exit 0; }
echo "$newstamp" > "$STAMP"
say "guard binary ready ($how): $BIN"
