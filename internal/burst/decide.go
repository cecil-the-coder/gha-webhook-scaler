package burst

import (
	"strings"
	"time"
)

// ScaleInput is everything one scale-up decision needs. It is for one target
// (owner, or owner/repo) in one pool.
type ScaleInput struct {
	Queued        int  // waiting jobs this target+pool's runners could take
	Pending       int  // this target+pool's VMs that have not run a job yet
	Active        int  // the pool's live VMs (all targets)
	MaxConcurrent int  // cap on Active for this decision
	BudgetOK      bool // daily VM-hour budget not yet spent
}

// Decide returns how many VMs to create now, and why. Pure: no I/O.
// One job per VM, so a pending VM already covers one waiting job.
func Decide(in ScaleInput) (int, string) {
	if in.Queued <= 0 {
		return 0, "no_backlog"
	}
	need := in.Queued - in.Pending
	if need <= 0 {
		return 0, "covered"
	}
	room := in.MaxConcurrent - in.Active
	switch {
	case room <= 0:
		return 0, "cap_concurrent"
	case !in.BudgetOK:
		return 0, "cap_budget"
	}
	return min(need, room), "scale_up"
}

// VMState is what the controller knows about one VM at this tick.
type VMState struct {
	Running    bool          // provider says it is provisioning or running
	Age        time.Duration // since creation
	Registered bool          // a runner with this VM's name is visible to GitHub
	Busy       bool          // that runner is running a job right now
	SeenBusy   bool          // a previous tick saw it busy
	Known      bool          // runner state for this VM could be read this tick
}

// Timeouts are the reaping limits.
type Timeouts struct {
	Idle, Boot, MaxAge time.Duration
}

// DeleteReason returns why a VM should be deleted, or "" to keep it. queued is
// the waiting-job count its runner could take.
func DeleteReason(v VMState, queued int, t Timeouts) string {
	switch {
	case v.Age > t.MaxAge:
		return "max_age"
	case !v.Running:
		return "not_running" // spot preemption or termination
	case !v.Known:
		return "" // GitHub unreadable for this VM: never guess
	case v.Busy:
		return ""
	case v.SeenBusy:
		return "job_done" // ran its one job; the ephemeral runner is gone or idle
	case v.Registered && queued == 0 && v.Age > t.Idle:
		return "idle"
	case !v.Registered && v.Age > t.Boot:
		return "boot_timeout"
	}
	return ""
}

// canRun applies GitHub's rule: a runner takes a job only when it carries every
// label the job asks for, ignoring case.
func canRun(jobLabels, runnerLabels []string) bool {
	for _, want := range jobLabels {
		found := false
		for _, have := range runnerLabels {
			if strings.EqualFold(want, have) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(jobLabels) > 0
}
