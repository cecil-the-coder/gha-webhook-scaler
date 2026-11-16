#!/bin/bash
# runner-bootstrap.sh: the user-data script of one EC2 burst runner VM.
#
# cloud-init runs this as root at first boot. It:
#   1. installs the GitHub runner (and Docker) if the image lacks them,
#   2. asks the scaler for ONE single-use JIT runner config, authenticated by
#      an sts:GetCallerIdentity request the VM signs with its instance-role
#      credentials (from IMDSv2) and does not send itself,
#   3. runs `run.sh --jitconfig <config>` as the unprivileged runner user,
#   4. powers the VM off when the runner exits, on every path. VMs are created
#      with InstanceInitiatedShutdownBehavior=terminate, so poweroff ends them.
#
# Embedded in the scaler binary (internal/burst/aws). The scaler fills in
# SETTINGS_B64 below with base64 JSON of PUBLIC values only:
#   config_url      https://<host>/v1/runner-config; also the identity audience
#   runner_version  "latest" or a version such as 2.330.0
#   install_docker  true/false
#   runner_env      optional NAME=VALUE lines for the runner's .env
#
# RETRY RULE. The scaler issues one config per VM; a second request gets 409.
# So the POST is made once. Any non-200 answer powers the VM off, and the
# scaler deletes it (boot timeout).
#
# LOGGING. Step names and HTTP status codes only, to stderr (console log).
# Never a credential, a signed request, a response body or a config.
#
# TEST HOOKS. BURST_* overrides below are honoured only when BURST_TEST_MODE=1
# (test-runner-bootstrap.sh). Production never sets it.
{ set +x; } 2>/dev/null
set -uo pipefail
umask 077
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

SETTINGS_B64="__GHA_BURST_SETTINGS__"

TEST_MODE=0
if [[ "${BURST_TEST_MODE:-0}" == 1 ]]; then TEST_MODE=1; fi

if (( TEST_MODE )); then
  IMDS="${BURST_IMDS_BASE:-http://169.254.169.254}"
  RUN_DIR="${BURST_RUN_DIR:-/run/gha-burst}"
  RUNNER_DIR="${BURST_RUNNER_DIR:-/opt/actions-runner}"
  RUNNER_USER="${BURST_RUNNER_USER:-runner}"
  RUNNER_HOME="${BURST_RUNNER_HOME:-$HOME}"
  POWEROFF=("${BURST_POWEROFF_CMD:-systemctl poweroff}")
  CURL_BIN="${BURST_CURL:-curl}"
  GITHUB_API="${BURST_GITHUB_API:-https://api.github.com}"
  TEST_CA="${BURST_TEST_CA:-}"
  SKIP_SYSTEM="${BURST_SKIP_SYSTEM:-1}" # no useradd/apt in tests
  SIGN_TIME="${BURST_SIGN_TIME:-}"
else
  IMDS="http://169.254.169.254"
  RUN_DIR="/run/gha-burst"
  RUNNER_DIR="/opt/actions-runner"
  RUNNER_USER="runner"
  RUNNER_HOME="/home/runner"
  POWEROFF=(systemctl poweroff)
  CURL_BIN=curl
  GITHUB_API="https://api.github.com"
  TEST_CA=""
  SKIP_SYSTEM=0
  SIGN_TIME=""
fi

if (( TEST_MODE )); then
  AS_RUNNER=()
else
  AS_RUNNER=(setpriv --reset-env --reuid="$RUNNER_USER" --regid="$RUNNER_USER" --init-groups --)
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

# imds_token: sets IMDS_TOKEN to a fresh IMDSv2 session token.
imds_token() {
  IMDS_TOKEN="$("$CURL_BIN" -q -g -sS -f --max-time 5 -X PUT \
    -H 'X-aws-ec2-metadata-token-ttl-seconds: 300' "$IMDS/latest/api/token" 2>/dev/null)" || return 1
  [[ "$IMDS_TOKEN" =~ ^[A-Za-z0-9._=+/-]+$ ]]
}

# imds_get <path>: prints an IMDS value. The session token goes in through
# curl's config on stdin, not argv.
imds_get() {
  printf 'header = "X-aws-ec2-metadata-token: %s"\n' "$IMDS_TOKEN" |
    "$CURL_BIN" -q -g -sS -f --max-time 5 -K - "$IMDS/latest/$1" 2>/dev/null
}

if (( EUID != 0 && !TEST_MODE )); then
  log "step=start status=not-root"
  exit 1
fi

command -v python3 >/dev/null || { log "step=start status=no-python3"; exit 1; }
install -d -m 0755 "$RUN_DIR" || { log "step=workdir status=run-dir-failed"; exit 1; }
WORK="$(mktemp -d "$RUN_DIR/work.XXXXXX")" || { log "step=workdir status=failed"; exit 1; }

# ── 1. Settings (from user-data) ────────────────────────────────────────────
# Prints config URL, runner version and Docker switch, one per line, and
# writes the runner env lines to argv[1]. Every value is checked here.
log "step=settings"
SETTINGS_PY='
import base64, json, re, sys
try:
    s = json.loads(base64.b64decode(sys.argv[2], validate=True))
    url = s["config_url"]
    ver = s.get("runner_version") or "latest"
    docker = s.get("install_docker", True)
    env = s.get("runner_env") or []
    rdir = s.get("runner_dir") or "/opt/actions-runner"
    ruser = s.get("runner_user") or "runner"
    if not isinstance(rdir, str) or not re.fullmatch(r"/[A-Za-z0-9._/-]+", rdir) or ".." in rdir:
        raise ValueError("runner_dir")
    if not isinstance(ruser, str) or not re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", ruser) or ruser == "root":
        raise ValueError("runner_user")
    if not isinstance(url, str) or not re.fullmatch(r"https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/v1/runner-config", url):
        raise ValueError("config_url")
    if not isinstance(ver, str) or not re.fullmatch(r"latest|[0-9]+\.[0-9]+\.[0-9]+", ver):
        raise ValueError("runner_version")
    if not isinstance(docker, bool):
        raise ValueError("install_docker")
    if not isinstance(env, list):
        raise ValueError("runner_env")
    for line in env:
        if not isinstance(line, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=[^\x00-\x1f\x7f]*", line):
            raise ValueError("runner_env")
except Exception:
    sys.exit(1)
with open(sys.argv[1], "w") as f:
    for line in env:
        f.write(line + "\n")
print(url)
print(ver)
print("true" if docker else "false")
print(rdir)
print(ruser)
'
{
  read -r CONFIG_URL
  read -r RUNNER_VERSION
  read -r INSTALL_DOCKER
  read -r SET_RUNNER_DIR
  read -r SET_RUNNER_USER
} < <(python3 -I -c "$SETTINGS_PY" "$WORK/runner.env" "$SETTINGS_B64" || echo FAILED)
if [[ -z "${CONFIG_URL:-}" || "$CONFIG_URL" == FAILED || -z "${INSTALL_DOCKER:-}" ]]; then
  log "step=settings status=failed"
  exit 1
fi
# Test mode keeps its BURST_RUNNER_* hooks; production takes the pool's values.
if (( ! TEST_MODE )); then
  RUNNER_DIR="$SET_RUNNER_DIR"
  RUNNER_USER="$SET_RUNNER_USER"
  AS_RUNNER=(setpriv --reset-env --reuid="$RUNNER_USER" --regid="$RUNNER_USER" --init-groups --)
fi

# ── 2. Runner user, Docker and the runner itself (stock images only) ────────
if (( ! SKIP_SYSTEM )); then
  if ! id -u "$RUNNER_USER" >/dev/null 2>&1; then
    useradd -m -s /bin/bash "$RUNNER_USER" || { log "step=user status=failed"; exit 1; }
  fi
  RUNNER_HOME="$(getent passwd "$RUNNER_USER" | cut -d: -f6)"
  [[ -n "$RUNNER_HOME" ]] || { log "step=user status=no-home"; exit 1; }
  if [[ "$INSTALL_DOCKER" == true ]] && ! command -v docker >/dev/null; then
    log "step=docker status=installing"
    export DEBIAN_FRONTEND=noninteractive
    if apt-get update -qq >/dev/null 2>&1 &&
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

# ── 3. Job environment (.env) from the settings' runner_env ─────────────────
# A configured runner gets .env from env.sh; a JIT runner does not.
if [[ -s "$WORK/runner.env" ]]; then
  install -m 0644 "$WORK/runner.env" "$RUNNER_DIR/.env" || { log "step=runner-env status=write-failed"; exit 1; }
  log "step=runner-env status=ok vars=$(grep -c . "$WORK/runner.env")"
fi
rm -f -- "$WORK/runner.env"

# ── 4. Identity: a signed, unsent sts:GetCallerIdentity request ─────────────
# The instance-role credentials come from IMDSv2 straight into the signer on
# stdin; they never touch disk or argv. The signature covers the audience
# header (= the config URL), so the request is only good for this scaler.
# The role has no permissions: the signed request proves who the VM is and
# nothing else, and it expires (the scaler accepts it for 5 minutes).
log "step=identity"
imds_token || { log "step=identity status=imds-token-failed"; exit 1; }
REGION="$(imds_get meta-data/placement/region)" || { log "step=identity status=region-failed"; exit 1; }
if ! [[ "$REGION" =~ ^[a-z]{2}(-[a-z]+)+-[0-9]+$ ]]; then
  log "step=identity status=region-malformed"
  exit 1
fi
ROLE="$(imds_get meta-data/iam/security-credentials/ | head -n1)" || { log "step=identity status=no-role"; exit 1; }
if ! [[ "$ROLE" =~ ^[A-Za-z0-9+=,.@_-]{1,64}$ ]]; then
  log "step=identity status=role-malformed"
  exit 1
fi
# SigV4 (AWS4-HMAC-SHA256) of a POST to sts.<region>.amazonaws.com. Reads the
# IMDS credentials JSON on stdin, prints the base64 token on stdout.
SIGN_PY='
import base64, datetime, hashlib, hmac, json, sys
region, audience, when = sys.argv[1], sys.argv[2], sys.argv[3]
c = json.load(sys.stdin)
if c.get("Code", "Success") != "Success":
    sys.exit(1)
akid, secret, session = c["AccessKeyId"], c["SecretAccessKey"], c.get("Token", "")
if when:
    now = datetime.datetime.strptime(when, "%Y%m%dT%H%M%SZ")
else:
    now = datetime.datetime.now(datetime.timezone.utc)
amz_date = now.strftime("%Y%m%dT%H%M%SZ")
day = now.strftime("%Y%m%d")
host = "sts.%s.amazonaws.com" % region
body = "Action=GetCallerIdentity&Version=2011-06-15"
headers = {
    "content-type": "application/x-www-form-urlencoded; charset=utf-8",
    "host": host,
    "x-amz-date": amz_date,
    "x-gha-scaler-audience": audience,
}
if session:
    headers["x-amz-security-token"] = session
names = sorted(headers)
signed = ";".join(names)
canonical = "\n".join([
    "POST", "/", "",
    "".join("%s:%s\n" % (k, " ".join(headers[k].split())) for k in names),
    signed,
    hashlib.sha256(body.encode()).hexdigest(),
])
scope = "%s/%s/sts/aws4_request" % (day, region)
to_sign = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canonical.encode()).hexdigest()])
def h(k, m):
    return hmac.new(k, m.encode(), hashlib.sha256).digest()
k = h(h(h(h(("AWS4" + secret).encode(), day), region), "sts"), "aws4_request")
sig = hmac.new(k, to_sign.encode(), hashlib.sha256).hexdigest()
canon_names = {"content-type": "Content-Type", "host": "Host", "x-amz-date": "X-Amz-Date",
               "x-gha-scaler-audience": "X-Gha-Scaler-Audience", "x-amz-security-token": "X-Amz-Security-Token"}
out = {k2: [headers[k]] for k, k2 in canon_names.items() if k in headers}
out["Authorization"] = ["AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s" % (akid, scope, signed, sig)]
tok = {"method": "POST", "url": "https://%s/" % host, "headers": out,
       "body": base64.b64encode(body.encode()).decode()}
sys.stdout.write(base64.b64encode(json.dumps(tok, separators=(",", ":")).encode()).decode())
'
TOKEN="$(imds_get "meta-data/iam/security-credentials/$ROLE" |
  python3 -I -c "$SIGN_PY" "$REGION" "$CONFIG_URL" "$SIGN_TIME")" \
  || { log "step=identity status=sign-failed"; exit 1; }
unset IMDS_TOKEN
if ! [[ "$TOKEN" =~ ^[A-Za-z0-9+/=]+$ ]]; then
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
log "step=runner status=start"
rc=0
# shellcheck disable=SC2016 # expanded by the inner shell, from its arguments
"${AS_RUNNER[@]}" env -i PATH="/usr/local/bin:$PATH" HOME="$RUNNER_HOME" LANG=C.UTF-8 \
  bash -c 'cd -- "$1" || exit 1
cfg="$(cat -- "$2")" || exit 1
rm -f -- "$2"
exec ./run.sh --jitconfig "$cfg"' gha-burst-runner "$RUNNER_DIR" "$JIT" || rc=$?
log "step=runner status=exit rc=$rc"
