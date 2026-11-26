#!/bin/bash
# runner-bootstrap.sh: the startup script of one burst runner VM.
#
# GCE runs this as root at boot. It:
#   1. stops unattended-upgrades (its 30 s shutdown delay outlasts a spot
#      preemption), then installs the GitHub runner (and Docker) if the image
#      lacks them,
#   2. asks the scaler for ONE single-use JIT runner config, authenticated by
#      the VM's own instance identity token,
#   3. runs `run.sh --jitconfig <config>` as the unprivileged runner user,
#   4. powers the VM off when the runner exits, on every path. VMs are created
#      with instanceTerminationAction=DELETE (spot) or are reaped by the scaler.
#
# Embedded in the scaler binary (internal/burst/gce) and set as the
# startup-script metadata item. Metadata it reads (all PUBLIC values):
#   runner-config-url  https://<host>/v1/runner-config; also the token audience
#   runner-version     "latest" or a version such as 2.330.0
#   install-docker     true/false
#   runner-dir         where the runner lives (default /opt/actions-runner)
#   runner-user        the unprivileged user it runs as (default runner)
#   runner-env         optional NAME=VALUE lines for the runner's .env
#   packages           optional space-separated apt packages to install
#
# RETRY RULE. The scaler issues one config per VM; a second request gets 409.
# So the POST is made once. Any non-200 answer powers the VM off, and the
# scaler deletes it (boot timeout).
#
# LOGGING. Step names and HTTP status codes only, to stderr (serial console).
# Never a token, a response body or a config.
#
# TEST HOOKS. BURST_* overrides below are honoured only when BURST_TEST_MODE=1
# (test-runner-bootstrap.sh). Production never sets it.
{ set +x; } 2>/dev/null
set -uo pipefail
umask 077
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

TEST_MODE=0
if [[ "${BURST_TEST_MODE:-0}" == 1 ]]; then TEST_MODE=1; fi

if (( TEST_MODE )); then
  META_BASE="${BURST_META_BASE:-http://metadata.google.internal/computeMetadata/v1}"
  RUN_DIR="${BURST_RUN_DIR:-/run/gha-burst}"
  RUNNER_DIR="${BURST_RUNNER_DIR:-/opt/actions-runner}"
  RUNNER_USER="${BURST_RUNNER_USER:-runner}"
  RUNNER_HOME="${BURST_RUNNER_HOME:-$HOME}"
  POWEROFF=("${BURST_POWEROFF_CMD:-systemctl poweroff}")
  CURL_BIN="${BURST_CURL:-curl}"
  GITHUB_API="${BURST_GITHUB_API:-https://api.github.com}"
  TEST_CA="${BURST_TEST_CA:-}"
  SKIP_SYSTEM="${BURST_SKIP_SYSTEM:-1}" # no useradd/apt in tests
else
  META_BASE="http://metadata.google.internal/computeMetadata/v1"
  RUN_DIR="/run/gha-burst"
  RUNNER_DIR="/opt/actions-runner"
  RUNNER_USER="runner"
  RUNNER_HOME="/home/runner"
  POWEROFF=(systemctl poweroff)
  CURL_BIN=curl
  GITHUB_API="https://api.github.com"
  TEST_CA=""
  SKIP_SYSTEM=0
fi

WORK=""
JIT_DIR="$RUN_DIR/jit"
JIT=""

log() { printf 'gha-burst-bootstrap: %s\n' "$*" >&2; }

on_exit() {
  local rc=$?
  if [[ -n "$JIT" ]]; then rm -f -- "$JIT"; fi
  if [[ -n "$WORK" ]]; then rm -rf -- "$WORK"; fi
  log "step=exit status=$rc"
  "${POWEROFF[@]}" >/dev/null 2>&1 || log "step=poweroff status=failed"
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 1' HUP INT TERM

# meta_opt <attribute>: sets META_VAL and returns 0 on 200, 2 on 404, 1 otherwise.
meta_opt() {
  local raw code
  raw="$("$CURL_BIN" -q -g -sS --max-time 10 -H 'Metadata-Flavor: Google' -w '\n%{http_code}' \
    "$META_BASE/instance/attributes/$1" 2>/dev/null)" || raw=$'\n000'
  code="${raw##*$'\n'}"
  META_VAL="${raw%$'\n'*}"
  case "$code" in
    200) return 0 ;;
    404) META_VAL=""; return 2 ;;
    *) META_VAL=""; return 1 ;;
  esac
}

if (( EUID != 0 && !TEST_MODE )); then
  log "step=start status=not-root"
  exit 1
fi

# ── 1. Metadata ──────────────────────────────────────────────────────────────
log "step=metadata"
meta_opt runner-config-url || { log "step=metadata status=runner-config-url-failed"; exit 1; }
CONFIG_URL="$META_VAL"
if ! [[ "$CONFIG_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/v1/runner-config$ ]]; then
  log "step=metadata status=runner-config-url-malformed"
  exit 1
fi
RUNNER_VERSION="latest"
if meta_opt runner-version; then RUNNER_VERSION="$META_VAL"; fi
if ! [[ "$RUNNER_VERSION" =~ ^(latest|[0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
  log "step=metadata status=runner-version-malformed"
  exit 1
fi
INSTALL_DOCKER="true"
if meta_opt install-docker; then INSTALL_DOCKER="$META_VAL"; fi
# check_packages: every PACKAGES entry is a plain Debian package name.
check_packages() {
  local pkg
  if (( ${#PACKAGES[@]} > 64 )); then return 1; fi
  for pkg in ${PACKAGES[@]+"${PACKAGES[@]}"}; do
    [[ "$pkg" =~ ^[a-z0-9][a-z0-9+.-]+$ ]] || return 1
  done
}
PACKAGES=()
if meta_opt packages; then read -r -a PACKAGES <<<"$META_VAL"; fi
if ! check_packages; then
  log "step=metadata status=packages-malformed"
  exit 1
fi
if (( ! TEST_MODE )); then
  if meta_opt runner-dir; then RUNNER_DIR="$META_VAL"; fi
  if meta_opt runner-user; then RUNNER_USER="$META_VAL"; fi
fi
if ! [[ "$RUNNER_DIR" =~ ^/[A-Za-z0-9._/-]+$ && "$RUNNER_DIR" != *..* ]]; then
  log "step=metadata status=runner-dir-malformed"
  exit 1
fi
if ! [[ "$RUNNER_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ && "$RUNNER_USER" != root ]]; then
  log "step=metadata status=runner-user-malformed"
  exit 1
fi

if (( TEST_MODE )); then
  AS_RUNNER=()
else
  AS_RUNNER=(setpriv --reset-env --reuid="$RUNNER_USER" --regid="$RUNNER_USER" --init-groups --)
fi

command -v python3 >/dev/null || { log "step=start status=no-python3"; exit 1; }
install -d -m 0755 "$RUN_DIR" || { log "step=workdir status=run-dir-failed"; exit 1; }
WORK="$(mktemp -d "$RUN_DIR/work.XXXXXX")" || { log "step=workdir status=failed"; exit 1; }

# ── 1b. No shutdown delay ────────────────────────────────────────────────────
# Ubuntu's unattended-upgrades.service holds a logind "delay" inhibitor, and the
# image's unattended-upgrades-logind-maxdelay.conf raises InhibitDelayMaxSec to
# 30 s. A spot preemption allows about 30 s, so GCE hard-stopped the VM before
# shutdown began: no shutdown-script, no signal to the runner, and the job was
# lost as "lost communication". A one-job VM never needs upgrades, so stop the
# service, which releases the inhibitor and lets shutdown start at once.
if (( ! SKIP_SYSTEM )); then
  systemctl stop unattended-upgrades.service >/dev/null 2>&1 || log "step=inhibitor status=stop-failed"
  DELAY_LOCKS="$(systemd-inhibit --list --no-pager --no-legend 2>/dev/null | grep -cE '[[:space:]]delay[[:space:]]*$' || true)"
  log "step=inhibitor delay_locks=${DELAY_LOCKS:-unknown}"
fi

# ── 2. Runner user, Docker and the runner itself (stock images only) ────────
if (( ! SKIP_SYSTEM )); then
  if ! id -u "$RUNNER_USER" >/dev/null 2>&1; then
    useradd -m -s /bin/bash "$RUNNER_USER" || { log "step=user status=failed"; exit 1; }
  fi
  RUNNER_HOME="$(getent passwd "$RUNNER_USER" | cut -d: -f6)"
  [[ -n "$RUNNER_HOME" ]] || { log "step=user status=no-home"; exit 1; }
  export DEBIAN_FRONTEND=noninteractive
  APT_UPDATED=0
  apt_update() { (( APT_UPDATED )) || { apt-get update -qq >/dev/null 2>&1 && APT_UPDATED=1; }; }
  if [[ "$INSTALL_DOCKER" == true ]] && ! command -v docker >/dev/null; then
    log "step=docker status=installing"
    if apt_update &&
       { apt-get install -y -qq docker.io docker-buildx >/dev/null 2>&1 ||
         apt-get install -y -qq docker.io >/dev/null 2>&1; }; then
      systemctl enable --now docker >/dev/null 2>&1 || true
      log "step=docker status=ok"
    else
      # Not fatal: jobs that need Docker fail visibly; others still run.
      log "step=docker status=failed"
    fi
  fi
  if getent group docker >/dev/null; then usermod -aG docker "$RUNNER_USER" || true; fi
  if (( ${#PACKAGES[@]} )); then
    log "step=packages status=installing count=${#PACKAGES[@]}"
    if apt_update && apt-get install -y -qq --no-install-recommends "${PACKAGES[@]}" >/dev/null 2>&1; then
      log "step=packages status=ok"
    else
      # Not fatal: jobs that need them fail visibly; others still run.
      log "step=packages status=failed"
    fi
  fi
fi

if [[ ! -x "$RUNNER_DIR/run.sh" ]]; then
  log "step=runner-install version=$RUNNER_VERSION"
  case "$(uname -m)" in
    x86_64) ARCH=x64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) log "step=runner-install status=unsupported-arch"; exit 1 ;;
  esac
  if [[ "$RUNNER_VERSION" == latest ]]; then REL="releases/latest"; else REL="releases/tags/v$RUNNER_VERSION"; fi
  CA_ARGS=()
  if [[ -n "$TEST_CA" ]]; then CA_ARGS=(--cacert "$TEST_CA"); fi
  "$CURL_BIN" -q -g -sS -f --max-time 30 ${CA_ARGS[@]+"${CA_ARGS[@]}"} -H 'Accept: application/vnd.github+json' \
    -o "$WORK/release.json" "$GITHUB_API/repos/actions/runner/$REL" \
    || { log "step=runner-install status=release-lookup-failed"; exit 1; }
  # Prints "<url> <sha256>" for this arch's tarball. The digest comes from the
  # release API; a missing or odd one fails the boot.
  PICK_PY='
import json, re, sys
arch = sys.argv[1]
rel = json.load(open(sys.argv[2]))
ver = rel["tag_name"].lstrip("v")
want = "actions-runner-linux-%s-%s.tar.gz" % (arch, ver)
for a in rel.get("assets", []):
    if a.get("name") == want:
        d = a.get("digest") or ""
        url = a.get("browser_download_url") or ""
        if re.fullmatch(r"sha256:[0-9a-f]{64}", d) and url.startswith("https://"):
            print(url, d[7:])
            sys.exit(0)
sys.exit(1)
'
  read -r TAR_URL TAR_SHA < <(python3 -I -c "$PICK_PY" "$ARCH" "$WORK/release.json") \
    || { log "step=runner-install status=no-asset"; exit 1; }
  "$CURL_BIN" -q -g -sS -fL --max-time 300 ${CA_ARGS[@]+"${CA_ARGS[@]}"} -o "$WORK/runner.tgz" "$TAR_URL" \
    || { log "step=runner-install status=download-failed"; exit 1; }
  if [[ "$(sha256sum "$WORK/runner.tgz" | cut -d' ' -f1)" != "$TAR_SHA" ]]; then
    log "step=runner-install status=checksum-mismatch"
    exit 1
  fi
  install -d -m 0755 "$RUNNER_DIR" || { log "step=runner-install status=dir-failed"; exit 1; }
  tar -xzf "$WORK/runner.tgz" -C "$RUNNER_DIR" || { log "step=runner-install status=extract-failed"; exit 1; }
  rm -f -- "$WORK/runner.tgz" "$WORK/release.json"
  if (( ! SKIP_SYSTEM )); then
    "$RUNNER_DIR/bin/installdependencies.sh" >/dev/null 2>&1 || log "step=runner-install deps=failed"
    chown -R "$RUNNER_USER:$RUNNER_USER" "$RUNNER_DIR" || { log "step=runner-install status=chown-failed"; exit 1; }
  fi
  [[ -x "$RUNNER_DIR/run.sh" ]] || { log "step=runner-install status=no-run-sh"; exit 1; }
  log "step=runner-install status=ok"
fi

# ── 3. Job environment (.env) from the optional runner-env item ─────────────
# A configured runner gets .env from env.sh; a JIT runner does not. Each line is
# checked again here; a malformed line stops the boot.
meta_opt runner-env
case $? in
  0)
    ENV_FILE="$WORK/runner.env"
    : > "$ENV_FILE"
    while IFS= read -r line || [[ -n "$line" ]]; do
      [[ -z "$line" ]] && continue
      if ! [[ "$line" =~ ^[A-Za-z_][A-Za-z0-9_]*=[^[:cntrl:]]*$ ]]; then
        log "step=runner-env status=malformed"
        exit 1
      fi
      printf '%s\n' "$line" >> "$ENV_FILE"
    done <<< "$META_VAL"
    install -m 0644 "$ENV_FILE" "$RUNNER_DIR/.env" || { log "step=runner-env status=write-failed"; exit 1; }
    log "step=runner-env status=ok vars=$(grep -c . "$ENV_FILE")"
    ;;
  2) ;;
  *) log "step=runner-env status=metadata-failed"; exit 1 ;;
esac

# ── 4. Identity token, audience = the config URL ────────────────────────────
log "step=identity"
AUD="$(python3 -I -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$CONFIG_URL")"
TOKEN="$("$CURL_BIN" -q -g -sS -f --max-time 10 -H 'Metadata-Flavor: Google' \
  "$META_BASE/instance/service-accounts/default/identity?audience=${AUD}&format=full")" \
  || { log "step=identity status=metadata-failed"; exit 1; }
if ! [[ "$TOKEN" =~ ^[A-Za-z0-9._-]+$ ]]; then
  log "step=identity status=malformed"
  exit 1
fi

# ── 5. One POST for the runner config, no retry ─────────────────────────────
# The header goes in through curl's config on stdin, not argv, so the token
# never shows in the process list. The body goes to a root-only file.
log "step=config-request"
RESP="$WORK/resp.json"
CA_ARGS=()
if [[ -n "$TEST_CA" ]]; then CA_ARGS=(--cacert "$TEST_CA"); fi
code="$(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
  "$CURL_BIN" -q -g -sS -K - ${CA_ARGS[@]+"${CA_ARGS[@]}"} --proto '=https' --tlsv1.2 --max-time 60 \
       -X POST -o "$RESP" -w '%{http_code}' "$CONFIG_URL" 2>/dev/null)" || code=000
unset TOKEN
if [[ "$code" != 200 ]]; then
  log "step=config-request status=$code (no retry)"
  exit 1
fi
log "step=config-request status=200"

# ── 6. Extract the config into a runner-only file ───────────────────────────
install -d -m 0700 "$JIT_DIR" || { log "step=extract status=dir-failed"; exit 1; }
if (( ! TEST_MODE )); then chown "$RUNNER_USER:$RUNNER_USER" "$JIT_DIR" || { log "step=extract status=chown-failed"; exit 1; }; fi
JIT="$JIT_DIR/jit-config"
( umask 077; : > "$JIT" ) || { log "step=extract status=create-failed"; exit 1; }
if (( ! TEST_MODE )); then chown "$RUNNER_USER:$RUNNER_USER" "$JIT" || { log "step=extract status=chown-failed"; exit 1; }; fi
EXTRACT_PY='
import json, re, sys
try:
    v = json.load(sys.stdin).get("encoded_jit_config")
    if not isinstance(v, str) or not re.fullmatch(r"[A-Za-z0-9+/=]{16,65536}", v):
        raise ValueError
except Exception:
    sys.exit(1)
sys.stdout.write(v)
'
if ! python3 -I -c "$EXTRACT_PY" < "$RESP" > "$JIT"; then
  log "step=extract status=invalid-body"
  exit 1
fi
chmod 0400 "$JIT" || { log "step=extract status=chmod-failed"; exit 1; }
rm -f -- "$RESP"
log "step=extract status=ok"

# ── 7. Run the one job ───────────────────────────────────────────────────────
# config.sh has no --jitconfig option: run.sh takes the config directly. It is
# in run.sh's argv while the runner lives; accepted, since it is single-use,
# expires in about an hour and the VM is single-tenant. The file is deleted
# before run.sh starts.
#
# UMASK. The bootstrap runs under `umask 077` to protect its own files, but the
# runner and every job step inherit the umask of this shell. Under 077,
# actions/checkout writes the workspace as 700/600, so a job container running
# as a non-root user cannot read a bind-mounted checkout (it looks empty), while
# root containers still can. Reset to the usual 022 just before run.sh; the
# config file is already read and deleted by then.
log "step=runner status=start"
rc=0
# shellcheck disable=SC2016 # expanded by the inner shell, from its arguments
"${AS_RUNNER[@]}" env -i PATH="/usr/local/bin:$PATH" HOME="$RUNNER_HOME" LANG=C.UTF-8 \
  bash -c 'cd -- "$1" || exit 1
cfg="$(cat -- "$2")" || exit 1
rm -f -- "$2"
umask 022
exec ./run.sh --jitconfig "$cfg"' gha-burst-runner "$RUNNER_DIR" "$JIT" || rc=$?
log "step=runner status=exit rc=$rc"
