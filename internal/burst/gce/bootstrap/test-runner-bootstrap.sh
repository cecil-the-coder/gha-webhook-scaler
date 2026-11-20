#!/bin/bash
# Offline test for runner-bootstrap.sh. Loopback only: no GCP, no GitHub.
#
# Cases:
#   installed  runner already in the image; scaler returns 200 -> run.sh gets
#              exactly --jitconfig <config>, the config file is gone first.
#   install    no runner; it is "downloaded" from the stub release API, its
#              sha256 checked, extracted, then run.
#   badsum     the tarball does not match the release digest -> no runner.
#   409 / 500  refused -> power off once, no runner, no config file.
#   env        runner-env lines land verbatim in .env; malformed ones stop the boot.
#
# Usage: test-runner-bootstrap.sh
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOT="$HERE/runner-bootstrap.sh"
STUBS="$HERE/stubs.py"
BLOB="c3R1Yi1qaXQtYmxvYi0xMjM0NTY3ODk="
TOKEN="stub.identity-token"

WORK="$(mktemp -d)"
PIDS=()
cleanup() {
  local p
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
  rm -rf -- "$WORK"
}
trap cleanup EXIT

fails=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

for tool in openssl python3 curl tar sha256sum; do
  command -v "$tool" >/dev/null || { echo "SKIP: $tool is required"; exit 0; }
done
REAL_CURL="$(command -v curl)"
bash -n "$BOOT" && pass "bash -n" || fail "bash -n"
if command -v shellcheck >/dev/null; then
  shellcheck "$BOOT" && pass "shellcheck" || fail "shellcheck"
else
  echo "NOTE shellcheck not installed; not run"
fi

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=127.0.0.1 \
  -addext "subjectAltName=IP:127.0.0.1" \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" >/dev/null 2>&1 || { fail "openssl cert"; exit 1; }

# A fake runner tarball: run.sh records its argv, cwd and whether the config file still exists.
make_tarball() { # <dir-for-records> <out.tgz>
  local src="$WORK/tarsrc.$RANDOM"
  mkdir -p "$src"
  cat > "$src/run.sh" <<EOF
#!/bin/bash
printf '%s\n' "\$@" > "$1/runner-argv"
pwd > "$1/runner-cwd"
umask > "$1/runner-umask"
find "$1/run" -name jit-config > "$1/runner-saw-config"
EOF
  chmod +x "$src/run.sh"
  tar -czf "$2" -C "$src" run.sh
}

# run_case <name> <status> <mode: installed|install|badsum> [runner-env-file]
run_case() {
  local name="$1" status="$2" mode="$3" env_file="${4:-}" spid i tgz
  c="$WORK/$name"
  mkdir -p "$c/run" "$c/bin"
  cat > "$c/bin/curl" <<EOF
#!/bin/bash
printf '%s ' "\$@" >> "$c/curl-argv"
printf '\n' >> "$c/curl-argv"
exec "$REAL_CURL" "\$@"
EOF
  chmod +x "$c/bin/curl"
  printf '#!/bin/bash\necho poweroff >> "%s/poweroff.log"\n' "$c" > "$c/poweroff.sh"
  chmod +x "$c/poweroff.sh"

  tgz="$c/runner.tgz"
  make_tarball "$c" "$tgz"
  if [[ "$mode" == installed ]]; then
    mkdir -p "$c/runner" && tar -xzf "$tgz" -C "$c/runner"
  fi
  local bad=0
  [[ "$mode" == badsum ]] && bad=1

  STUB_BAD_SUM="$bad" python3 -I "$STUBS" "$c/ports" "$status" "$WORK/cert.pem" "$WORK/key.pem" "$tgz" $env_file >"$c/stubs.log" 2>&1 &
  spid=$!
  PIDS+=("$spid")
  for i in $(seq 100); do [[ -s "$c/ports" ]] && break; sleep 0.1; done
  read -r mport bport < "$c/ports" || { fail "$name: stubs did not start"; return 1; }

  env -i PATH="$PATH" HOME="$c" \
    BURST_TEST_MODE=1 \
    BURST_META_BASE="http://127.0.0.1:$mport/computeMetadata/v1" \
    BURST_RUN_DIR="$c/run" \
    BURST_RUNNER_DIR="$c/runner" \
    BURST_RUNNER_USER="$(id -un)" \
    BURST_RUNNER_HOME="$c" \
    BURST_POWEROFF_CMD="$c/poweroff.sh" \
    BURST_CURL="$c/bin/curl" \
    BURST_GITHUB_API="https://127.0.0.1:$bport" \
    BURST_TEST_CA="$WORK/cert.pem" \
    bash "$BOOT" >"$c/log" 2>&1
  echo $? > "$c/rc"
  kill "$spid" 2>/dev/null
  return 0
}

poweroff_once() { [[ -f "$1/poweroff.log" && "$(wc -l < "$1/poweroff.log")" == 1 ]]; }
no_secrets() {
  if grep -q -e "$BLOB" -e "$TOKEN" "$2/log"; then fail "$1: secret in log"; else pass "$1: log has no token or config"; fi
}

check_ran() { # <name>
  local name="$1" c="$WORK/$1"
  [[ "$(cat "$c/rc")" == 0 ]] && pass "$name: exits 0 after the runner" || { fail "$name: rc=$(cat "$c/rc")"; sed 's/^/    /' "$c/log"; }
  poweroff_once "$c" && pass "$name: powers off once" || fail "$name: power-off not once"
  if [[ -f "$c/runner-argv" && "$(sed -n 1p "$c/runner-argv")" == "--jitconfig" \
        && "$(sed -n 2p "$c/runner-argv")" == "$BLOB" && "$(wc -l < "$c/runner-argv")" == 2 ]]; then
    pass "$name: run.sh got exactly --jitconfig <config>"
  else
    fail "$name: run.sh argv wrong"
  fi
  [[ "$(cat "$c/runner-cwd" 2>/dev/null)" == "$c/runner" ]] && pass "$name: run from runner dir" || fail "$name: wrong cwd"
  [[ "$(cat "$c/runner-umask" 2>/dev/null)" == 0022 ]] && pass "$name: runner starts with umask 022" || fail "$name: runner umask $(cat "$c/runner-umask" 2>/dev/null)"
  [[ -f "$c/runner-saw-config" && ! -s "$c/runner-saw-config" ]] && pass "$name: config deleted before run.sh" || fail "$name: config file present at run.sh"
  if grep -rl -e "$BLOB" "$c/run" "$c/log" "$c/stubs.log" 2>/dev/null | grep -q .; then
    fail "$name: config found outside run.sh argv"
  else
    pass "$name: config only in run.sh argv"
  fi
  find "$c/run" -maxdepth 1 -name 'work.*' | grep -q . && fail "$name: workdir left" || pass "$name: workdir removed"
  no_secrets "$name" "$c"
}

check_refused() { # <name> <status>
  local name="$1" c="$WORK/$1"
  [[ "$(cat "$c/rc")" == 1 ]] && pass "$name: exits 1 (no retry)" || fail "$name: rc=$(cat "$c/rc")"
  poweroff_once "$c" && pass "$name: powers off once" || fail "$name: power-off not once"
  [[ -e "$c/runner-argv" ]] && fail "$name: runner started" || pass "$name: runner not started"
  find "$c/run" -name jit-config | grep -q . && fail "$name: config file written" || pass "$name: no config file"
  grep -q "step=config-request status=$2" "$c/log" && pass "$name: log names status only" || fail "$name: status line missing"
  no_secrets "$name" "$c"
}

run_case installed 200 installed; check_ran installed
grep -q "step=runner-install" "$WORK/installed/log" && fail "installed: tried to reinstall" || pass "installed: no download"
[[ -e "$WORK/installed/runner/.env" ]] && fail "installed: .env without runner-env" || pass "installed: no runner-env, no .env"

run_case install 200 install; check_ran install
grep -q "step=runner-install status=ok" "$WORK/install/log" && pass "install: runner downloaded and checksum verified" || fail "install: no install line"

run_case badsum 200 badsum
c="$WORK/badsum"
if [[ "$(cat "$c/rc")" == 1 && ! -e "$c/runner-argv" ]] && grep -q "status=checksum-mismatch" "$c/log" \
   && ! grep -q -e "-X POST" "$c/curl-argv"; then
  pass "badsum: checksum mismatch stops the boot before any config request"
else
  fail "badsum: unexpected outcome"
fi

run_case refused 409 installed; check_refused refused 409
run_case broken 500 installed; check_refused broken 500

printf 'BUILDX_BUILDER=remote\nFOO=bar baz\n' > "$WORK/ok.env"
run_case withenv 200 installed "$WORK/ok.env"
c="$WORK/withenv"
if [[ "$(cat "$c/rc")" == 0 ]] && cmp -s "$WORK/ok.env" "$c/runner/.env"; then
  pass "env: written verbatim to .env"
else
  fail "env: .env missing or different"
fi
grep -q "step=runner-env status=ok vars=2" "$c/log" && ! grep -q BUILDX "$c/log" && pass "env: log names the count only" || fail "env: log wrong"

printf 'GOOD=1\nnot a var\n' > "$WORK/bad.env"
run_case badenv 200 installed "$WORK/bad.env"
c="$WORK/badenv"
if [[ "$(cat "$c/rc")" == 1 && ! -e "$c/runner-argv" ]] && grep -q "step=runner-env status=malformed" "$c/log" \
   && ! grep -q -e "-X POST" "$c/curl-argv"; then
  pass "env: malformed line stops the boot before any config request"
else
  fail "env: malformed line not refused"
fi

echo
if (( fails == 0 )); then echo "ALL PASS"; exit 0; fi
echo "$fails FAILED"
exit 1
