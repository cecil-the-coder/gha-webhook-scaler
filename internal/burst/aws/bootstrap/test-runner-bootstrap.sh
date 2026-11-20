#!/bin/bash
# test-runner-bootstrap.sh: offline test of the EC2 runner-bootstrap.sh.
#
# Runs the bootstrap in BURST_TEST_MODE against a stub IMDSv2 (plain HTTP)
# and a stub scaler (HTTPS with a throwaway CA), both on loopback. The runner
# is a fake run.sh and poweroff is a marker file. Nothing leaves the machine.
#
# Needs bash, curl, python3 and openssl. Usage: ./test-runner-bootstrap.sh
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/runner-bootstrap.sh"
T="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
  rm -rf -- "$T"
}
trap cleanup EXIT

FAILS=0
fail() { echo "FAIL: $*"; FAILS=$((FAILS + 1)); }
pass() { echo "ok:   $*"; }

bash -n "$SCRIPT" || { echo "FAIL: bash -n"; exit 1; }
pass "bash -n"

# ── Throwaway CA-less self-signed cert for 127.0.0.1 ────────────────────────
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=127.0.0.1" \
  -addext "subjectAltName=IP:127.0.0.1" -keyout "$T/key.pem" -out "$T/cert.pem" >/dev/null 2>&1 \
  || { echo "FAIL: openssl"; exit 1; }

# ── Stub servers ────────────────────────────────────────────────────────────
cat > "$T/stub.py" <<'PY'
import base64, hashlib, hmac, http.server, json, os, ssl, sys

mode, state = sys.argv[1], sys.argv[2]
AKID, SECRET, SESSION = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "FwoGZXIvYXdzEXAMPLE"
IMDS_TOKEN = "imds-session-token-123"

def record(name, line):
    with open(os.path.join(state, name), "a") as f:
        f.write(line + "\n")

def sigv4_ok(tok, audience):
    h = {k.lower(): v[0] for k, v in tok["headers"].items()}
    auth = h["authorization"]
    cred = auth.split("Credential=")[1].split(",")[0]
    akid, day, region, service, _ = cred.split("/")
    signed = auth.split("SignedHeaders=")[1].split(",")[0].strip()
    names = signed.split(";")
    if akid != AKID or service != "sts" or "x-gha-scaler-audience" not in names or "host" not in names:
        return "scope"
    if h.get("x-gha-scaler-audience") != audience:
        return "audience"
    if tok["url"] != "https://sts.%s.amazonaws.com/" % region or h["host"] != "sts.%s.amazonaws.com" % region:
        return "host"
    body = base64.b64decode(tok["body"])
    if body != b"Action=GetCallerIdentity&Version=2011-06-15" or tok["method"] != "POST":
        return "request"
    canon = "\n".join(["POST", "/", "", "".join("%s:%s\n" % (n, " ".join(h[n].split())) for n in names),
                       signed, hashlib.sha256(body).hexdigest()])
    scope = "%s/%s/sts/aws4_request" % (day, region)
    sts = "\n".join(["AWS4-HMAC-SHA256", h["x-amz-date"], scope, hashlib.sha256(canon.encode()).hexdigest()])
    k = ("AWS4" + SECRET).encode()
    for p in (day, region, "sts", "aws4_request"):
        k = hmac.new(k, p.encode(), hashlib.sha256).digest()
    if not auth.endswith("Signature=" + hmac.new(k, sts.encode(), hashlib.sha256).hexdigest()):
        return "signature"
    if h.get("x-amz-security-token") != SESSION:
        return "session"
    return ""

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def send(self, code, body=b"", ctype="text/plain"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_PUT(self):
        record("imds.log", "PUT " + self.path)
        if mode == "imds" and self.path == "/latest/api/token" and self.headers.get("X-aws-ec2-metadata-token-ttl-seconds"):
            return self.send(200, IMDS_TOKEN.encode())
        self.send(400)

    def do_GET(self):
        if mode != "imds":
            return self.send(405)
        record("imds.log", "GET " + self.path)
        if self.headers.get("X-aws-ec2-metadata-token") != IMDS_TOKEN:
            return self.send(401)
        p = self.path
        if p == "/latest/meta-data/placement/region":
            return self.send(200, b"us-east-1")
        if p == "/latest/meta-data/iam/security-credentials/":
            return self.send(200, b"gha-burst-runner")
        if p == "/latest/meta-data/iam/security-credentials/gha-burst-runner":
            return self.send(200, json.dumps({"Code": "Success", "Type": "AWS-HMAC", "AccessKeyId": AKID,
                                              "SecretAccessKey": SECRET, "Token": SESSION}).encode())
        self.send(404)

    def do_POST(self):
        if mode != "broker":
            return self.send(405)
        auth = self.headers.get("Authorization", "")
        why = "no-bearer"
        if auth.startswith("Bearer "):
            try:
                tok = json.loads(base64.b64decode(auth[7:], validate=True))
                why = sigv4_ok(tok, open(os.path.join(state, "audience")).read().strip())
            except Exception as e:
                why = "decode:" + type(e).__name__
        record("broker.log", "POST " + self.path + " check=" + (why or "ok"))
        if why:
            return self.send(401)
        status = int(open(os.path.join(state, "broker_status")).read().strip())
        if status != 200:
            return self.send(status)
        self.send(200, json.dumps({"encoded_jit_config": "ZmFrZS1qaXQtY29uZmlnLWZvci10ZXN0cw=="}).encode(), "application/json")

srv = http.server.HTTPServer(("127.0.0.1", 0), H)
if mode == "broker":
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(sys.argv[3], sys.argv[4])
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
with open(os.path.join(state, mode + ".port"), "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY

mkdir -p "$T/state"
python3 -I "$T/stub.py" imds "$T/state" & PIDS+=($!)
python3 -I "$T/stub.py" broker "$T/state" "$T/cert.pem" "$T/key.pem" & PIDS+=($!)
for _ in $(seq 50); do
  [[ -s "$T/state/imds.port" && -s "$T/state/broker.port" ]] && break
  sleep 0.1
done
[[ -s "$T/state/imds.port" && -s "$T/state/broker.port" ]] || { echo "FAIL: stubs did not start"; exit 1; }
IMDS_PORT="$(cat "$T/state/imds.port")"
BROKER_PORT="$(cat "$T/state/broker.port")"
CONFIG_URL="https://127.0.0.1:$BROKER_PORT/v1/runner-config"
echo "$CONFIG_URL" > "$T/state/audience"

# render <settings-json> <out>: fills the placeholder the way the scaler does.
render() {
  local b64
  b64="$(printf '%s' "$1" | base64 -w0)"
  sed "s|__GHA_BURST_SETTINGS__|$b64|" "$SCRIPT" > "$2"
  chmod +x "$2"
}

# run_case <name> <settings-json> <broker-status>: runs the bootstrap in a fresh dir.
run_case() {
  local name="$1" settings="$2" status="$3"
  C="$T/$name"
  mkdir -p "$C/runner" "$C/run" "$C/home"
  cat > "$C/runner/run.sh" <<EOF
#!/bin/bash
printf '%s\n' "\$@" > "$C/run.args"
umask > "$C/run.umask"
cp .env "$C/env.seen" 2>/dev/null || true
EOF
  chmod +x "$C/runner/run.sh"
  cat > "$C/poweroff" <<EOF
#!/bin/bash
touch "$C/powered-off"
EOF
  chmod +x "$C/poweroff"
  echo "$status" > "$T/state/broker_status"
  : > "$T/state/broker.log"
  : > "$T/state/imds.log"
  render "$settings" "$C/bootstrap.sh"
  BURST_TEST_MODE=1 BURST_IMDS_BASE="http://127.0.0.1:$IMDS_PORT" BURST_RUN_DIR="$C/run" \
    BURST_RUNNER_DIR="$C/runner" BURST_RUNNER_HOME="$C/home" BURST_POWEROFF_CMD="$C/poweroff" \
    BURST_TEST_CA="$T/cert.pem" BURST_SKIP_SYSTEM=1 \
    bash "$C/bootstrap.sh" > "$C/stdout" 2> "$C/stderr"
  RC=$?
}

GOOD="{\"config_url\":\"$CONFIG_URL\",\"runner_version\":\"2.330.0\",\"install_docker\":true,\"runner_env\":[\"BUILDX_BUILDER=remote\",\"FOO=bar baz\"],\"packages\":[\"cmake\",\"libfreetype-dev\"]}"

# ── Case 1: happy path ──────────────────────────────────────────────────────
run_case ok "$GOOD" 200
[[ $RC == 0 ]] && pass "ok: exit 0" || fail "ok: exit $RC: $(cat "$C/stderr")"
[[ -e "$C/powered-off" ]] && pass "ok: powered off" || fail "ok: no poweroff"
[[ "$(sed -n 1p "$C/run.args" 2>/dev/null)" == "--jitconfig" && "$(sed -n 2p "$C/run.args")" == "ZmFrZS1qaXQtY29uZmlnLWZvci10ZXN0cw==" ]] \
  && pass "ok: run.sh --jitconfig <config>" || fail "ok: run.sh args: $(cat "$C/run.args" 2>/dev/null)"
[[ "$(cat "$C/run.umask" 2>/dev/null)" == 0022 ]] && pass "ok: runner starts with umask 022" || fail "ok: runner umask $(cat "$C/run.umask" 2>/dev/null)"
[[ "$(cat "$C/env.seen" 2>/dev/null)" == $'BUILDX_BUILDER=remote\nFOO=bar baz' ]] && pass "ok: .env written" || fail "ok: .env: $(cat "$C/env.seen" 2>/dev/null)"
[[ "$(cat "$T/state/broker.log")" == "POST /v1/runner-config check=ok" ]] && pass "ok: one POST, signature and audience valid" \
  || fail "ok: broker saw: $(cat "$T/state/broker.log")"
grep -q '^PUT /latest/api/token$' "$T/state/imds.log" && pass "ok: IMDSv2 token PUT" || fail "ok: no IMDSv2 PUT"
[[ -z "$(ls -A "$C/run/jit" 2>/dev/null)" && -z "$(find "$C/run" -name 'work.*')" ]] && pass "ok: jit file and workdir removed" || fail "ok: leftovers in run dir"
if grep -qE 'AKIA|wJalr|Fwo|Signature|Bearer|ZmFrZS1q|imds-session' "$C/stderr" "$C/stdout"; then
  fail "ok: secret material in the log"
else
  pass "ok: log has no secrets"
fi

# ── Case 2: scaler says 409; no retry, no runner, still powered off ─────────
run_case conflict "$GOOD" 409
[[ $RC != 0 ]] && pass "409: non-zero exit" || fail "409: exit 0"
[[ "$(grep -c POST "$T/state/broker.log")" == 1 ]] && pass "409: exactly one POST" || fail "409: POSTs: $(cat "$T/state/broker.log")"
[[ ! -e "$C/run.args" ]] && pass "409: runner not started" || fail "409: runner started"
[[ -e "$C/powered-off" ]] && pass "409: powered off" || fail "409: no poweroff"
grep -q 'step=config-request status=409 (no retry)' "$C/stderr" && pass "409: logged" || fail "409: log: $(cat "$C/stderr")"

# ── Case 3: malformed settings stop before any identity work ────────────────
run_case badurl '{"config_url":"http://127.0.0.1/v1/runner-config"}' 200
[[ $RC != 0 && -e "$C/powered-off" ]] && pass "bad url: refused and powered off" || fail "bad url: rc=$RC"
[[ ! -s "$T/state/imds.log" && ! -s "$T/state/broker.log" ]] && pass "bad url: no IMDS or scaler call" || fail "bad url: calls made"

run_case badenv "{\"config_url\":\"$CONFIG_URL\",\"runner_env\":[\"BAD LINE\"]}" 200
[[ $RC != 0 && -e "$C/powered-off" && ! -s "$T/state/broker.log" ]] && pass "bad env: refused" || fail "bad env: rc=$RC"

run_case badpkg "{\"config_url\":\"$CONFIG_URL\",\"packages\":[\"cmake;rm\"]}" 200
[[ $RC != 0 && -e "$C/powered-off" && ! -s "$T/state/broker.log" ]] && pass "bad package: refused" || fail "bad package: rc=$RC"

# ── Case 4: no IMDS at all; fails closed ────────────────────────────────────
C="$T/noimds"
mkdir -p "$C/runner" "$C/run"
printf '#!/bin/bash\ntouch "%s/ran"\n' "$C" > "$C/runner/run.sh"; chmod +x "$C/runner/run.sh"
printf '#!/bin/bash\ntouch "%s/powered-off"\n' "$C" > "$C/poweroff"; chmod +x "$C/poweroff"
render "$GOOD" "$C/bootstrap.sh"
: > "$T/state/broker.log"
BURST_TEST_MODE=1 BURST_IMDS_BASE="http://127.0.0.1:9" BURST_RUN_DIR="$C/run" BURST_RUNNER_DIR="$C/runner" \
  BURST_POWEROFF_CMD="$C/poweroff" BURST_TEST_CA="$T/cert.pem" bash "$C/bootstrap.sh" 2> "$C/stderr"
RC=$?
[[ $RC != 0 && -e "$C/powered-off" && ! -e "$C/ran" && ! -s "$T/state/broker.log" ]] && pass "no imds: fails closed" || fail "no imds: rc=$RC"
grep -q 'step=identity status=imds-token-failed' "$C/stderr" && pass "no imds: logged" || fail "no imds: log: $(cat "$C/stderr")"

echo
if (( FAILS )); then echo "$FAILS check(s) failed"; exit 1; fi
echo "all checks passed"
