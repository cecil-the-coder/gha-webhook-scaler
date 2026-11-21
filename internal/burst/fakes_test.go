package burst

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

type fakeForge struct {
	mu      sync.Mutex
	types   map[string]github.OwnerType
	jobs    map[string][]github.WaitingJob // owner/repo -> jobs
	jobErr  map[string]error
	runners map[github.RunnerScope][]github.Runner
	mintErr error
	minted  []mintCall
}

type mintCall struct {
	scope  github.RunnerScope
	name   string
	labels []string
	group  int64
}

func newFakeForge() *fakeForge {
	return &fakeForge{types: map[string]github.OwnerType{}, jobs: map[string][]github.WaitingJob{},
		jobErr: map[string]error{}, runners: map[github.RunnerScope][]github.Runner{}}
}

func (f *fakeForge) OwnerType(_ context.Context, owner string) (github.OwnerType, error) {
	if t, ok := f.types[owner]; ok {
		return t, nil
	}
	return "", errors.New("unknown owner")
}

func (f *fakeForge) WaitingJobs(_ context.Context, owner, repo string) ([]github.WaitingJob, error) {
	k := owner + "/" + repo
	return f.jobs[k], f.jobErr[k]
}

func (f *fakeForge) Runners(_ context.Context, s github.RunnerScope) ([]github.Runner, error) {
	return f.runners[s], nil
}

func (f *fakeForge) MintJIT(_ context.Context, s github.RunnerScope, name string, labels []string, group int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintErr != nil {
		return "", f.mintErr
	}
	f.minted = append(f.minted, mintCall{s, name, labels, group})
	return "ENCODEDJITCONFIG0123456789", nil
}

type fakeProvider struct {
	vms            map[string]Instance
	pools          map[string]string // vm -> pool
	createErr      error
	lingerOnDelete bool
	created        []string
	deleted        []string
	now            func() time.Time
}

func newFakeProvider(now func() time.Time) *fakeProvider {
	return &fakeProvider{vms: map[string]Instance{}, pools: map[string]string{}, now: now}
}

func (p *fakeProvider) Create(_ context.Context, pool config.BurstPool, name string) error {
	if p.createErr != nil && !errors.Is(p.createErr, ErrOutcomeUnknown) {
		return p.createErr
	}
	p.created = append(p.created, name)
	if p.createErr == nil {
		p.vms[name] = Instance{Name: name, Running: true, Created: p.now()}
		p.pools[name] = pool.Name
	}
	return p.createErr
}

func (p *fakeProvider) Delete(_ context.Context, name string) error {
	if p.lingerOnDelete {
		v := p.vms[name]
		v.Running = false // GCE keeps listing it (STOPPING/TERMINATED) for a while
		p.vms[name] = v
	} else {
		delete(p.vms, name)
	}
	p.deleted = append(p.deleted, name)
	return nil
}

func (p *fakeProvider) List(context.Context) ([]Instance, error) {
	out := make([]Instance, 0, len(p.vms))
	for _, v := range p.vms {
		out = append(out, v)
	}
	return out, nil
}

type capErr struct{ code string }

func (e capErr) Error() string        { return "capacity: " + e.code }
func (e capErr) CapacityCode() string { return e.code }

type fakeVerifier struct {
	name string
	err  error
}

func (v fakeVerifier) Verify(context.Context, string) (Identity, error) {
	return Identity{InstanceName: v.name}, v.err
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	pool := func(name, arch string, labels ...string) config.BurstPool {
		return config.BurstPool{Name: name, MachineType: "m", Image: "i", Arch: arch, Labels: labels,
			MaxVMs: 3, DiskGB: 20, Prefix: "gha-burst-" + name + "-"}
	}
	return &config.Config{
		Owners: map[string]*config.OwnerConfig{
			"acme": {Owner: "acme", GitHubToken: "x", AllRepos: true, Burst: true, RunnerGroupID: 4},
			"bob":  {Owner: "bob", GitHubToken: "x", RepoName: "proj", Burst: true},
			"off":  {Owner: "off", GitHubToken: "x", RepoName: "r", Burst: false},
		},
		Burst: config.BurstConfig{
			Provider: "gce", After: time.Minute, PollInterval: 30 * time.Second, ActiveWindow: 30 * time.Minute,
			IdleTimeout: 10 * time.Minute, BootTimeout: 15 * time.Minute, MaxVMAge: 90 * time.Minute,
			MaxVMsTotal: 3, MaxVMHoursPerDay: 8, StateDir: t.TempDir(),
			PublicURL: "https://scaler.example.com/v1/runner-config",
			Pools:     []config.BurstPool{pool("x64", "x64", "build"), pool("arm", "arm64", "build")},
		},
	}
}

type harness struct {
	c     *Controller
	forge *fakeForge
	prov  *fakeProvider
	clk   *clock
}

func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()
	clk := &clock{t: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)}
	forge := newFakeForge()
	forge.types["acme"] = github.OwnerOrganization
	forge.types["bob"] = github.OwnerUser
	prov := newFakeProvider(clk.now)
	c, err := New(cfg, forge, prov, fakeVerifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.now = clk.now
	return &harness{c: c, forge: forge, prov: prov, clk: clk}
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	if err := h.c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) job(repo string, age time.Duration, labels ...string) {
	h.forge.jobs[repo] = append(h.forge.jobs[repo], github.WaitingJob{
		ID: int64(len(h.forge.jobs[repo]) + 1), Repo: repo, Labels: labels, CreatedAt: h.clk.t.Add(-age)})
}

func (h *harness) vmsInPool(pool string) int {
	n := 0
	for name := range h.prov.vms {
		if h.prov.pools[name] == pool {
			n++
		}
	}
	return n
}

func (h *harness) only() string {
	if len(h.prov.vms) != 1 {
		panic(fmt.Sprintf("want 1 VM, have %d", len(h.prov.vms)))
	}
	for n := range h.prov.vms {
		return n
	}
	return ""
}
