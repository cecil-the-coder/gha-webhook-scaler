#!/bin/bash
# runner-shutdown.sh: the shutdown script of one burst runner VM.
#
# GCE runs it whenever the VM stops, a spot preemption included, and gives a
# preemption about 30 s in all. Without it a preempted VM vanishes with its job
# running, and GitHub only gives up on the runner after its heartbeat times out
# ("lost communication with the server", ~10 min later). SIGINT makes the
# runner listener cancel its job and report it at once ("The runner has
# received a shutdown signal"), so the workflow fails, and is re-run, right away.
#
# When the VM powers itself off because the runner already exited (the
# bootstrap's normal end), there is no listener left and this does nothing.
set -u
PATTERN="${BURST_RUNNER_PATTERN:-Runner.Listener}"
WAIT_S="${BURST_SHUTDOWN_WAIT_S:-20}" # leaves the rest of the 30 s to the OS

log() { printf 'gha-burst-shutdown: %s\n' "$*" >&2; }

pkill -INT -f "$PATTERN" || { log "step=signal status=no-runner"; exit 0; }
log "step=signal status=sent"
for ((i = 0; i < WAIT_S; i++)); do
  pgrep -f "$PATTERN" >/dev/null || { log "step=wait status=exited after=${i}s"; exit 0; }
  # SIGTERM is also a graceful stop for the listener: the fallback if SIGINT
  # was ignored (a process started with SIGINT ignored cannot trap it).
  if ((i == WAIT_S / 2)); then pkill -TERM -f "$PATTERN" && log "step=signal status=term-sent"; fi
  sleep 1
done
log "step=wait status=timeout after=${WAIT_S}s"
exit 0
