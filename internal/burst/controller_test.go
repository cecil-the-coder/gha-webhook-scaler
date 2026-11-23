package burst

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		in     ScaleInput
		n      int
		reason string
	}{
		{ScaleInput{Queued: 0, MaxConcurrent: 3, BudgetOK: true}, 0, "no_backlog"},
		{ScaleInput{Queued: 2, Pending: 2, MaxConcurrent: 3, BudgetOK: true}, 0, "covered"},
		{ScaleInput{Queued: 5, Active: 3, MaxConcurrent: 3, BudgetOK: true}, 0, "cap_concurrent"},
		{ScaleInput{Queued: 5, MaxConcurrent: 3, BudgetOK: false}, 0, "cap_budget"},
		{ScaleInput{Queued: 5, Pending: 1, Active: 1, MaxConcurrent: 3, BudgetOK: true}, 2, "scale_up"},
	}
	for _, c := range cases {
		n, r := Decide(c.in)
		if n != c.n || r != c.reason {
			t.Errorf("Decide(%+v) = %d %s, want %d %s", c.in, n, r, c.n, c.reason)
		}
	}
}

func TestDeleteReason(t *testing.T) {
	to := Timeouts{Idle: 10 * time.Minute, Boot: 15 * time.Minute, MaxAge: 90 * time.Minute}
	cases := []struct {
		v      VMState
		queued int
		want   string
	}{
		{VMState{Running: true, Known: true, Busy: true, Age: 91 * time.Minute}, 0, "max_age"},
		{VMState{Running: false, Known: true}, 0, "not_running"},
		{VMState{Running: true, Known: false, Age: time.Hour}, 0, ""}, // never guess
		{VMState{Running: true, Known: true, Busy: true, Registered: true}, 0, ""},
		{VMState{Running: true, Known: true, SeenBusy: true, Registered: true}, 5, "job_done"},
		{VMState{Running: true, Known: true, Registered: true, Age: 11 * time.Minute}, 0, "idle"},
		{VMState{Running: true, Known: true, Registered: true, Age: 11 * time.Minute}, 1, ""},
		{VMState{Running: true, Known: true, Age: 16 * time.Minute}, 0, "boot_timeout"},
	}
	for _, c := range cases {
		if got := DeleteReason(c.v, c.queued, to); got != c.want {
			t.Errorf("DeleteReason(%+v, %d) = %q, want %q", c.v, c.queued, got, c.want)
		}
	}
}

func TestCanRun(t *testing.T) {
	have := []string{"self-hosted", "Linux", "ARM64", "build"}
	if !canRun([]string{"self-hosted", "arm64"}, have) {
		t.Error("case-insensitive subset should match")
	}
	if canRun([]string{"self-hosted", "x64"}, have) {
		t.Error("arch mismatch must not match")
	}
	if canRun([]string{"ubuntu-latest"}, have) {
		t.Error("GitHub-hosted label must not match")
	}
	if canRun(nil, have) {
		t.Error("a job with no labels must not match")
	}
}

func TestTick_BurstAfterDelayAndArchMatching(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.c.NoteJob("acme/app")
	h.job("acme/app", 10*time.Second, "self-hosted", "arm64") // too fresh
	h.tick(t)
	if len(h.prov.vms) != 0 {
		t.Fatalf("fresh job must wait BURST_AFTER, got %d VMs", len(h.prov.vms))
	}
	h.clk.advance(time.Minute)
	h.tick(t)
	if h.vmsInPool("arm") != 1 || h.vmsInPool("x64") != 0 {
		t.Fatalf("arm64 job must start exactly one arm VM, got arm=%d x64=%d", h.vmsInPool("arm"), h.vmsInPool("x64"))
	}
	// Covered: the pending VM covers the job, no second VM.
	h.tick(t)
	if len(h.prov.vms) != 1 {
		t.Fatalf("pending VM should cover the job, got %d", len(h.prov.vms))
	}
	tgt, ok := h.c.reg.TargetOf("arm", h.only())
	if !ok || tgt != (Target{Owner: "acme"}) {
		t.Errorf("org owner target = %+v %v", tgt, ok)
	}
}

func TestTick_SaturatedSkipsDelay(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.job("acme/app", time.Second, "self-hosted", "x64", "build")
	h.c.Saturated("acme", "acme/app")
	h.tick(t)
	if h.vmsInPool("x64") != 1 {
		t.Fatalf("saturated owner: want 1 x64 VM, got %d", h.vmsInPool("x64"))
	}
}

func TestTick_UnpolledAndDisabledOwners(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.job("acme/app", time.Hour, "self-hosted") // AllRepos owner, no activity: not polled
	h.job("off/r", time.Hour, "self-hosted")    // burst disabled
	h.c.NoteJob("off/r")
	h.c.NoteJob("stranger/x")
	h.tick(t)
	if len(h.prov.vms) != 0 {
		t.Fatalf("got %d VMs, want 0", len(h.prov.vms))
	}
	// Activity expires after ActiveWindow.
	h.c.NoteJob("acme/app")
	h.clk.advance(31 * time.Minute)
	h.tick(t)
	if len(h.prov.vms) != 0 {
		t.Fatalf("expired activity must not be polled, got %d VMs", len(h.prov.vms))
	}
}

func TestTick_UserOwnerIsRepoScoped(t *testing.T) {
	h := newHarness(t, testConfig(t)) // bob is single-repo: always polled
	h.job("bob/proj", time.Hour, "self-hosted", "linux")
	h.tick(t)
	if len(h.prov.vms) != 2 {
		// A label-less-arch job fits both pools (documented overlap).
		t.Fatalf("want one VM per pool for an arch-agnostic job, got %d", len(h.prov.vms))
	}
	for name := range h.prov.vms {
		tgt, _ := h.c.reg.TargetOf(h.prov.pools[name], name)
		if tgt != (Target{Owner: "bob", Repo: "proj"}) {
			t.Errorf("user owner target = %+v", tgt)
		}
	}
}

func TestTick_CapsTotalAndPool(t *testing.T) {
	cfg := testConfig(t)
	cfg.Burst.MaxVMsTotal = 2
	h := newHarness(t, cfg)
	h.c.NoteJob("acme/app")
	for i := 0; i < 5; i++ {
		h.job("acme/app", time.Hour, "self-hosted", "x64")
	}
	h.tick(t)
	h.tick(t)
	if len(h.prov.vms) != 2 {
		t.Fatalf("total cap 2, got %d", len(h.prov.vms))
	}
}

func TestTick_ReapsJobDoneIdleAndBootTimeout(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	name := h.only()
	scope := github.RunnerScope{Owner: "acme"}

	// Registered and busy: kept.
	h.forge.runners[scope] = []github.Runner{{Name: name, Busy: true}}
	h.forge.jobs["acme/app"] = nil
	h.tick(t)
	if len(h.prov.vms) != 1 {
		t.Fatal("busy VM must be kept")
	}
	// Job over: runner gone. Deleted as job_done.
	h.forge.runners[scope] = nil
	h.tick(t)
	if len(h.prov.vms) != 0 || len(h.prov.deleted) != 1 {
		t.Fatalf("job_done VM must be deleted, vms=%d deleted=%v", len(h.prov.vms), h.prov.deleted)
	}
	if _, ok := h.c.reg.TargetOf("x64", name); ok {
		t.Error("deleted VM must leave pending")
	}

	// Never registers: boot_timeout.
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	h.forge.jobs["acme/app"] = nil
	h.clk.advance(16 * time.Minute)
	h.c.NoteJob("acme/app")
	h.tick(t)
	if len(h.prov.vms) != 0 {
		t.Fatal("unregistered VM must hit boot_timeout")
	}
}

func TestTick_CapacityBackoffStopsAllPools(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.job("acme/app", time.Hour, "self-hosted", "arm64")
	h.prov.createErr = capErr{"QUOTA_EXCEEDED"}
	h.tick(t)
	if len(h.prov.created) != 0 || h.c.Status().CreateFailures != 1 {
		t.Fatalf("one failed insert expected, created=%v failures=%d", h.prov.created, h.c.Status().CreateFailures)
	}
	if h.c.reg.PendingCount() != 0 {
		t.Error("failed create must leave pending")
	}
	h.prov.createErr = nil
	h.tick(t)
	if len(h.prov.vms) != 0 {
		t.Fatal("creates must pause during backoff")
	}
	h.clk.advance(createBackoff + time.Second)
	h.tick(t)
	if len(h.prov.vms) != 2 {
		t.Fatalf("after backoff both pools create, got %d", len(h.prov.vms))
	}
}

func TestTick_UnconfirmedCountsUntilBootTimeout(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.prov.createErr = fmt.Errorf("slow: %w", ErrOutcomeUnknown)
	h.tick(t)
	h.prov.createErr = nil
	h.tick(t)
	if len(h.prov.created) != 1 {
		t.Fatalf("unconfirmed create must cover the job, created=%v", h.prov.created)
	}
	h.clk.advance(16 * time.Minute)
	h.c.NoteJob("acme/app")
	h.tick(t)
	if len(h.prov.created) != 2 {
		t.Fatalf("lost create must be dropped and replaced, created=%v", h.prov.created)
	}
}

func TestTick_DryRunCreatesNothing(t *testing.T) {
	cfg := testConfig(t)
	cfg.Burst.DryRun = true
	h := newHarness(t, cfg)
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted")
	h.tick(t)
	if len(h.prov.created) != 0 {
		t.Fatal("dry run must not create")
	}
}

func TestTick_AllReadsFailIsError(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.forge.jobErr["bob/proj"] = errors.New("403")
	if err := h.c.Tick(context.Background()); err == nil {
		t.Fatal("want error when no queue can be read")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	h := newHarness(t, cfg)
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	name := h.only()
	h.clk.advance(10 * time.Minute) // under BOOT_TIMEOUT, so the VM stays
	h.tick(t)

	h2 := newHarness(t, cfg)
	if tgt, ok := h2.c.reg.TargetOf("x64", name); !ok || tgt.Owner != "acme" {
		t.Errorf("registry not restored: %+v %v", tgt, ok)
	}
	if h2.c.budget.Used <= 0 {
		t.Error("budget not restored")
	}
}

// --- pull endpoint ---

func pull(h *harness, v IdentityVerifier, method, auth string) *httptest.ResponseRecorder {
	h.c.verify = v
	req := httptest.NewRequest(method, "/v1/runner-config", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.c.ServeRunnerConfig(rec, req)
	return rec
}

func TestPull_Flow(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "arm64")
	h.tick(t)
	name := h.only()
	ok := fakeVerifier{name: name}

	if rec := pull(h, ok, http.MethodGet, "Bearer t"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
	if rec := pull(h, ok, http.MethodPost, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d", rec.Code)
	}
	if rec := pull(h, fakeVerifier{err: errors.New("bad sig")}, http.MethodPost, "Bearer t"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token = %d", rec.Code)
	}
	if rec := pull(h, fakeVerifier{name: "other-vm"}, http.MethodPost, "Bearer t"); rec.Code != http.StatusForbidden {
		t.Errorf("unknown pool = %d", rec.Code)
	}
	if rec := pull(h, fakeVerifier{name: "gha-burst-arm-deadbeef"}, http.MethodPost, "Bearer t"); rec.Code != http.StatusForbidden {
		t.Errorf("not pending = %d", rec.Code)
	}

	h.forge.mintErr = errors.New("github down")
	if rec := pull(h, ok, http.MethodPost, "Bearer t"); rec.Code != http.StatusBadGateway {
		t.Errorf("mint failure = %d", rec.Code)
	}
	h.forge.mintErr = nil
	rec := pull(h, ok, http.MethodPost, "Bearer t")
	if rec.Code != http.StatusOK {
		t.Fatalf("issue = %d", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body) != 1 || body["encoded_jit_config"] != "ENCODEDJITCONFIG0123456789" {
		t.Errorf("body = %v", body)
	}
	m := h.forge.minted[0]
	if m.scope != (github.RunnerScope{Owner: "acme"}) || m.group != 4 || m.name != name {
		t.Errorf("mint call = %+v", m)
	}
	if strings.Join(m.labels, ",") != "self-hosted,Linux,ARM64,build" {
		t.Errorf("labels = %v (JIT labels must include self-hosted, Linux and arch)", m.labels)
	}
	if rec := pull(h, ok, http.MethodPost, "Bearer t"); rec.Code != http.StatusConflict {
		t.Errorf("second pull = %d, want 409", rec.Code)
	}
}

func TestPull_UserOwnerRepoScopeDefaultGroup(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.job("bob/proj", time.Hour, "self-hosted", "x64")
	h.tick(t)
	name := h.only()
	if rec := pull(h, fakeVerifier{name: name}, http.MethodPost, "Bearer t"); rec.Code != http.StatusOK {
		t.Fatalf("issue = %d", rec.Code)
	}
	m := h.forge.minted[0]
	if m.scope != (github.RunnerScope{Owner: "bob", Repo: "proj"}) || m.group != 1 {
		t.Errorf("mint = %+v", m)
	}
}

func TestPull_DryRun(t *testing.T) {
	cfg := testConfig(t)
	cfg.Burst.DryRun = true
	h := newHarness(t, cfg)
	if rec := pull(h, fakeVerifier{name: "gha-burst-x64-00000000"}, http.MethodPost, "Bearer t"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("dry run = %d", rec.Code)
	}
}

func TestWakeNeverBlocks(t *testing.T) {
	h := newHarness(t, testConfig(t))
	for i := 0; i < 10; i++ {
		h.c.Wake()
	}
	if len(h.c.wake) != 1 {
		t.Errorf("wakes must merge, buffered=%d", len(h.c.wake))
	}
}

func TestTick_BurstReposAlwaysPolled(t *testing.T) {
	cfg := testConfig(t)
	cfg.Owners["acme"].BurstRepos = []string{"app"}
	h := newHarness(t, cfg)
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t) // no webhook activity, but app is in BURST_REPOS
	if h.vmsInPool("x64") != 1 {
		t.Fatalf("BURST_REPOS repo must be polled without activity, got %d VMs", h.vmsInPool("x64"))
	}
}

func TestTick_DeletedVMStillListedIsNotDeletedTwice(t *testing.T) {
	h := newHarness(t, testConfig(t))
	h.prov.lingerOnDelete = true
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	name := h.only()
	scope := github.RunnerScope{Owner: "acme"}
	h.forge.runners[scope] = []github.Runner{{Name: name, Busy: true}}
	h.forge.jobs["acme/app"] = nil
	h.tick(t)
	h.forge.runners[scope] = nil
	h.tick(t) // job_done -> delete; the VM stays listed, not running
	h.tick(t)
	h.tick(t)
	if len(h.prov.deleted) != 1 {
		t.Fatalf("deleted %v, want exactly one delete", h.prov.deleted)
	}
	if got := h.c.Status().VMsByPool["x64"]; got != 0 {
		t.Errorf("a VM being deleted must not count as active, got %d", got)
	}
	// Still listed after the grace period: treated as a VM again and retried.
	h.clk.advance(deleteGrace + time.Minute)
	h.tick(t)
	if len(h.prov.deleted) != 2 {
		t.Errorf("after grace the delete must be retried, deleted=%v", h.prov.deleted)
	}
}

func TestTick_DeletingVMStillHoldsRoom(t *testing.T) {
	cfg := testConfig(t)
	cfg.Burst.MaxVMsTotal = 1
	h := newHarness(t, cfg)
	h.prov.lingerOnDelete = true
	h.c.NoteJob("acme/app")
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	name := h.only()
	scope := github.RunnerScope{Owner: "acme"}
	h.forge.runners[scope] = []github.Runner{{Name: name, Busy: true}}
	h.forge.jobs["acme/app"] = nil
	h.tick(t)
	h.forge.runners[scope] = nil
	h.tick(t) // job_done -> delete; the VM stays listed while it winds down
	if len(h.prov.deleted) != 1 {
		t.Fatalf("deleted %v, want one", h.prov.deleted)
	}
	// A new job arrives while the old VM is still listed: its vCPUs are not
	// free yet, so no create may be attempted (it would be refused for quota).
	h.job("acme/app", time.Hour, "self-hosted", "x64")
	h.tick(t)
	h.tick(t)
	if len(h.prov.created) != 1 {
		t.Fatalf("create while a deleting VM holds the only slot: created=%v", h.prov.created)
	}
	// Once the provider stops listing it, the slot is free again.
	delete(h.prov.vms, name)
	h.tick(t)
	if len(h.prov.created) != 2 {
		t.Errorf("slot freed, want a second create: created=%v", h.prov.created)
	}
}
