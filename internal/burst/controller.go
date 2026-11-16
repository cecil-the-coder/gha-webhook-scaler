package burst

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

const (
	// createBackoff pauses creates in every pool after a capacity failure:
	// vCPU quota is project-wide, so the other pools would fail too.
	createBackoff = 2 * time.Minute
	// defaultWakeGap allows at most one wake-triggered tick per 3 s, so a
	// webhook flood cannot burn the GitHub API budget.
	defaultWakeGap = 3 * time.Second
	// minSaturation is the least time an owner stays "saturated" after the
	// primary runtime reports it at capacity.
	minSaturation = 2 * time.Minute
)

// Controller reconciles burst VMs against the GitHub queue, one tick at a time.
type Controller struct {
	cfg    config.BurstConfig
	owners map[string]*config.OwnerConfig
	forge  Forge
	prov   Provider
	verify IdentityVerifier
	reg    *Registry
	budget *Budget
	log    *slog.Logger
	now    func() time.Time

	mu         sync.Mutex           // guards active, saturated, ownerTypes and status
	active     map[string]time.Time // owner/repo -> last activity
	saturated  map[string]time.Time // owner -> saturated until
	ownerTypes map[string]github.OwnerType
	status     Status

	// Below: touched by the tick goroutine only.
	seenBusy     map[string]bool
	lastTick     time.Time
	turn         int
	lastOutcome  map[string]string
	unconfirmed  map[string]unconfirmedVM
	backoffUntil time.Time

	wake    chan struct{}
	wakeGap time.Duration
}

type unconfirmedVM struct {
	pool   string
	target Target
	since  time.Time
}

// Status is a snapshot for /metrics.
type Status struct {
	Provider       string         `json:"provider"`
	DryRun         bool           `json:"dry_run"`
	LastTick       time.Time      `json:"last_tick"`
	VMsByPool      map[string]int `json:"vms_by_pool"`
	Pending        int            `json:"pending"`
	WaitingJobs    int            `json:"waiting_jobs_eligible"`
	BudgetUsedH    float64        `json:"budget_used_hours"`
	BudgetCapH     float64        `json:"budget_cap_hours"`
	BackoffUntil   *time.Time     `json:"create_backoff_until,omitempty"`
	VMsCreated     int            `json:"vms_created_total"`
	VMsDeleted     int            `json:"vms_deleted_total"`
	CreateFailures int            `json:"create_failures_total"`
	ConfigsIssued  int            `json:"configs_issued_total"`
}

// New builds a controller. It loads (or starts) the registry and budget in
// cfg.Burst.StateDir.
func New(cfg *config.Config, forge Forge, prov Provider, verify IdentityVerifier, log *slog.Logger) (*Controller, error) {
	if log == nil {
		log = slog.Default()
	}
	bc := cfg.Burst
	if err := os.MkdirAll(bc.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("burst state dir: %w", err)
	}
	reg, err := LoadRegistry(bc.StateDir)
	if err != nil {
		return nil, fmt.Errorf("burst registry: %w", err)
	}
	bud, err := loadBudget(bc.StateDir)
	if err != nil {
		return nil, fmt.Errorf("burst budget: %w", err)
	}
	return &Controller{
		cfg: bc, owners: cfg.Owners, forge: forge, prov: prov, verify: verify, reg: reg, budget: bud,
		log: log.With("component", "burst"), now: time.Now,
		active: map[string]time.Time{}, saturated: map[string]time.Time{}, ownerTypes: map[string]github.OwnerType{},
		seenBusy: map[string]bool{}, lastOutcome: map[string]string{}, unconfirmed: map[string]unconfirmedVM{},
		wake: make(chan struct{}, 1), wakeGap: defaultWakeGap,
		status: Status{Provider: bc.Provider, DryRun: bc.DryRun, BudgetCapH: bc.MaxVMHoursPerDay},
	}, nil
}

// Wake asks for a tick now. It never blocks; wakes merge.
func (c *Controller) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Controller) ownerOf(repoFullName string) (*config.OwnerConfig, string, bool) {
	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, "", false
	}
	oc := c.owners[owner]
	if oc == nil || !oc.Burst || !oc.IsRepoAllowed(repoFullName) {
		return nil, "", false
	}
	return oc, repo, true
}

// NoteJob records that repoFullName has a job waiting (from a webhook or the
// scanner), so the next ticks poll it, and wakes the loop.
func (c *Controller) NoteJob(repoFullName string) {
	if _, _, ok := c.ownerOf(repoFullName); !ok {
		return
	}
	c.mu.Lock()
	c.active[repoFullName] = c.now()
	c.mu.Unlock()
	c.Wake()
}

// Saturated is called by the primary runtime when owner is at its runner cap:
// that owner's waiting jobs count for burst at once, without BURST_AFTER.
func (c *Controller) Saturated(owner, repoFullName string) {
	if oc := c.owners[owner]; oc == nil || !oc.Burst {
		return
	}
	hold := max(2*c.cfg.PollInterval, minSaturation)
	c.mu.Lock()
	c.saturated[owner] = c.now().Add(hold)
	c.mu.Unlock()
	c.NoteJob(repoFullName)
}

// Status returns a snapshot for /metrics.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.VMsByPool = make(map[string]int, len(c.status.VMsByPool))
	for k, v := range c.status.VMsByPool {
		s.VMsByPool[k] = v
	}
	return s
}

// Run ticks every PollInterval until ctx ends, and after Wake (at most one
// wake-triggered tick per wakeGap). Ticks never overlap.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.PollInterval)
	defer t.Stop()
	c.log.Info("burst_start", "provider", c.cfg.Provider, "pools", len(c.cfg.Pools), "dry_run", c.cfg.DryRun)
	for {
		started := time.Now()
		if err := c.Tick(ctx); err != nil {
			c.log.Error("tick_failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
			if wait := c.wakeGap - time.Since(started); wait > 0 {
				d := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					d.Stop()
					return
				case <-t.C:
				case <-d.C:
				}
				d.Stop()
			}
		}
	}
}

// vm is one listed VM in a pool, with the target it was created for.
type vm struct {
	Instance
	target Target
	known  bool // target known from the registry
}

// waiting is one eligible job, resolved to its target.
type waiting struct {
	target Target
	labels []string
}

// pollRepos returns the repos to read this tick: single-repo owners always,
// others while they had activity within ActiveWindow.
func (c *Controller) pollRepos(now time.Time) []string {
	set := map[string]bool{}
	for _, oc := range c.owners {
		if oc.Burst && !oc.AllRepos && oc.RepoName != "" {
			set[oc.Owner+"/"+oc.RepoName] = true
		}
		if oc.Burst {
			for _, r := range oc.BurstRepos {
				if full := oc.Owner + "/" + r; oc.IsRepoAllowed(full) {
					set[full] = true
				}
			}
		}
	}
	c.mu.Lock()
	for repo, at := range c.active {
		if now.Sub(at) > c.cfg.ActiveWindow {
			delete(c.active, repo)
			continue
		}
		set[repo] = true
	}
	c.mu.Unlock()
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

func (c *Controller) isSaturated(owner string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Before(c.saturated[owner])
}

// targetFor returns where a runner for owner/repo must register: the org, or
// the repo for user accounts. The owner type is looked up once and cached.
func (c *Controller) targetFor(ctx context.Context, owner, repo string) (Target, error) {
	c.mu.Lock()
	typ, ok := c.ownerTypes[owner]
	c.mu.Unlock()
	if !ok {
		var err error
		if typ, err = c.forge.OwnerType(ctx, owner); err != nil {
			return Target{}, err
		}
		c.mu.Lock()
		c.ownerTypes[owner] = typ
		c.mu.Unlock()
	}
	if typ == github.OwnerOrganization {
		return Target{Owner: owner}, nil
	}
	return Target{Owner: owner, Repo: repo}, nil
}

func (t Target) scope() github.RunnerScope { return github.RunnerScope{Owner: t.Owner, Repo: t.Repo} }

func (c *Controller) runnerLabels(p config.BurstPool, owner string) []string {
	var extra []string
	if oc := c.owners[owner]; oc != nil {
		extra = oc.RunnerLabels
	}
	return p.RunnerLabels(extra)
}

func (c *Controller) queuedFor(jobs []waiting, t Target, p config.BurstPool) int {
	have := c.runnerLabels(p, t.Owner)
	n := 0
	for _, j := range jobs {
		if j.target == t && canRun(j.labels, have) {
			n++
		}
	}
	return n
}

// Tick runs one reconcile pass over every pool.
func (c *Controller) Tick(ctx context.Context) error {
	now := c.now()

	// 1. Waiting jobs that burst should take.
	var jobs []waiting
	readOK := 0
	repos := c.pollRepos(now)
	for _, full := range repos {
		oc, repo, ok := c.ownerOf(full)
		if !ok {
			continue
		}
		t, err := c.targetFor(ctx, oc.Owner, repo)
		if err != nil {
			c.log.Warn("tick_api_partial", "api", "owner_type", "owner", oc.Owner, "err", err)
			continue
		}
		got, err := c.forge.WaitingJobs(ctx, oc.Owner, repo)
		if err != nil {
			c.log.Warn("tick_api_partial", "api", "waiting_jobs", "repo", full, "err", err)
			continue
		}
		readOK++
		sat := c.isSaturated(oc.Owner, now)
		for _, j := range got {
			if sat || c.cfg.After <= 0 || (!j.CreatedAt.IsZero() && now.Sub(j.CreatedAt) >= c.cfg.After) {
				jobs = append(jobs, waiting{target: t, labels: j.Labels})
			}
		}
	}
	if len(repos) > 0 && readOK == 0 {
		return fmt.Errorf("no repository queue could be read")
	}

	// 2. The VMs, sorted into pools and resolved to their targets.
	all, err := c.prov.List(ctx)
	if err != nil {
		return fmt.Errorf("list VMs: %w", err)
	}
	byPool := map[string][]vm{}
	total := 0
	scopes := map[Target]bool{}
	for _, in := range all {
		p, ok := c.cfg.PoolFor(in.Name)
		if !ok {
			continue
		}
		t, known := c.reg.TargetOf(p.Name, in.Name)
		byPool[p.Name] = append(byPool[p.Name], vm{Instance: in, target: t, known: known})
		total++
		if known {
			scopes[t] = true
		}
	}

	// 3. Runner state, per scope that has VMs.
	runnerIn := map[Target]map[string]bool{} // scope -> runner name -> busy
	anyRunner := map[string]bool{}           // all scopes read: name -> busy
	for t := range scopes {
		rs, err := c.forge.Runners(ctx, t.scope())
		if err != nil {
			c.log.Warn("tick_api_partial", "api", "runners", "scope", t.String(), "err", err)
			continue
		}
		m := make(map[string]bool, len(rs))
		for _, r := range rs {
			m[r.Name] = r.Busy
			anyRunner[r.Name] = r.Busy
		}
		runnerIn[t] = m
	}

	if !c.lastTick.IsZero() {
		c.budget.Accrue(now, now.Sub(c.lastTick), total)
	}
	c.lastTick = now

	// 4. Reconcile each pool. Deletes first, then creates within the caps.
	next := make(map[string]bool, total)
	room := max(c.cfg.MaxVMsTotal-total, 0) // combined room; deletes free none this tick
	vmsByPool := map[string]int{}
	timeouts := Timeouts{Idle: c.cfg.IdleTimeout, Boot: c.cfg.BootTimeout, MaxAge: c.cfg.MaxVMAge}
	for _, p := range rotate(c.cfg.Pools, c.turn) {
		active := 0
		pending := map[Target]int{}
		for _, v := range byPool[p.Name] {
			var busy, registered, stateKnown bool
			if m, ok := runnerIn[v.target]; ok && v.known {
				busy, registered = m[v.Name]
				stateKnown = true
			} else if b, ok := anyRunner[v.Name]; ok {
				busy, registered, stateKnown = b, true, true
			}
			queued := 0
			if v.known {
				queued = c.queuedFor(jobs, v.target, p)
			}
			st := VMState{
				Running: v.Running, Age: now.Sub(v.Created), Registered: registered,
				Busy: busy, SeenBusy: c.seenBusy[v.Name], Known: stateKnown,
			}
			if reason := DeleteReason(st, queued, timeouts); reason != "" && c.deleteVM(ctx, p.Name, v.Name, reason) {
				continue
			}
			seen := c.seenBusy[v.Name] || busy
			next[v.Name] = seen
			active++
			if !seen && v.known {
				pending[v.target]++
			}
		}
		for t, n := range c.unconfirmedIn(now, p.Name, byPool[p.Name]) {
			active += n
			pending[t] += n
		}

		for _, t := range targetsOf(jobs) {
			queued := c.queuedFor(jobs, t, p)
			maxHere := min(p.MaxVMs, active+room)
			n, reason := Decide(ScaleInput{
				Queued: queued, Pending: pending[t], Active: active, MaxConcurrent: maxHere,
				BudgetOK: c.budget.OK(now, c.cfg.MaxVMHoursPerDay),
			})
			if n > 0 && now.Before(c.backoffUntil) {
				n, reason = 0, "create_backoff"
			}
			key := p.Name + "|" + t.String()
			if n > 0 || reason != c.lastOutcome[key] {
				c.log.Info("decision", "pool", p.Name, "target", t.String(), "reason", reason, "wanted", n,
					"queued", queued, "active", active, "pending", pending[t], "max_vms", p.MaxVMs,
					"budget_used_h", c.budget.Used, "dry_run", c.cfg.DryRun)
			}
			c.lastOutcome[key] = reason
			n = min(n, room)
			for i := 0; i < n && !c.cfg.DryRun; i++ {
				created, more := c.createVM(ctx, p, t)
				if created {
					active++
					pending[t]++
					room--
				}
				if !more {
					break
				}
			}
		}
		vmsByPool[p.Name] = active
	}
	c.turn++
	c.seenBusy = next
	if err := c.budget.Save(); err != nil {
		c.log.Error("budget_save_failed", "err", err)
	}

	c.mu.Lock()
	c.status.LastTick = now
	c.status.VMsByPool = vmsByPool
	c.status.Pending = c.reg.PendingCount()
	c.status.WaitingJobs = len(jobs)
	c.status.BudgetUsedH = c.budget.Used
	c.status.BackoffUntil = nil
	if now.Before(c.backoffUntil) {
		bu := c.backoffUntil
		c.status.BackoffUntil = &bu
	}
	c.mu.Unlock()
	return nil
}

// createVM creates one VM for target t in pool p. created says whether the VM
// may exist (and so counts toward caps); more says whether to keep creating
// this tick.
func (c *Controller) createVM(ctx context.Context, p config.BurstPool, t Target) (created, more bool) {
	name, err := newVMName(p.Prefix)
	if err != nil {
		c.log.Error("create_failed", "pool", p.Name, "err", err)
		return false, false
	}
	if err := c.reg.AddPending(p.Name, name, t); err != nil {
		c.log.Error("create_failed", "pool", p.Name, "vm", name, "err", err)
		return false, false
	}
	err = c.prov.Create(ctx, p, name)
	switch {
	case err == nil:
		c.log.Info("create", "pool", p.Name, "vm", name, "target", t.String(), "confirmed", true)
		c.bump(func(s *Status) { s.VMsCreated++ })
		return true, true
	case errors.Is(err, ErrOutcomeUnknown):
		c.unconfirmed[name] = unconfirmedVM{pool: p.Name, target: t, since: c.now()}
		c.log.Warn("create_unconfirmed", "pool", p.Name, "vm", name, "target", t.String(), "err", err)
		c.bump(func(s *Status) { s.VMsCreated++ })
		return true, false // slow provider: no more creates this tick
	}
	c.log.Error("create_failed", "pool", p.Name, "vm", name, "target", t.String(), "err", err)
	c.bump(func(s *Status) { s.CreateFailures++ })
	if ferr := c.reg.Forget(p.Name, name); ferr != nil {
		c.log.Error("registry_failed", "pool", p.Name, "vm", name, "err", ferr)
	}
	code := capacityCode(err)
	if code == "" {
		return false, true
	}
	if until := c.now().Add(createBackoff); until.After(c.backoffUntil) {
		c.backoffUntil = until
	}
	c.log.Warn("create_backoff", "reason", code, "pools", "all", "until", c.backoffUntil.UTC().Format(time.RFC3339))
	return false, false
}

func (c *Controller) deleteVM(ctx context.Context, pool, name, reason string) bool {
	if c.cfg.DryRun {
		c.log.Info("decision", "pool", pool, "vm", name, "reason", reason, "dry_run", true, "would_delete", true)
		return false
	}
	if err := c.prov.Delete(ctx, name); err != nil {
		c.log.Error("delete_failed", "pool", pool, "vm", name, "reason", reason, "err", err)
		return false
	}
	if err := c.reg.Forget(pool, name); err != nil {
		c.log.Error("registry_failed", "pool", pool, "vm", name, "err", err)
	}
	c.bump(func(s *Status) { s.VMsDeleted++ })
	c.log.Info("delete", "pool", pool, "vm", name, "reason", reason)
	return true
}

// unconfirmedIn returns, per target, the pool's unconfirmed creates that the
// listing does not show yet. Listed ones are dropped (the listing counts them);
// ones older than BootTimeout are dropped and forgotten.
func (c *Controller) unconfirmedIn(now time.Time, pool string, listed []vm) map[Target]int {
	out := map[Target]int{}
	if len(c.unconfirmed) == 0 {
		return out
	}
	seen := make(map[string]bool, len(listed))
	for _, v := range listed {
		seen[v.Name] = true
	}
	for name, u := range c.unconfirmed {
		switch {
		case u.pool != pool:
		case seen[name]:
			delete(c.unconfirmed, name)
		case now.Sub(u.since) > c.cfg.BootTimeout:
			delete(c.unconfirmed, name)
			if err := c.reg.Forget(pool, name); err != nil {
				c.log.Error("registry_failed", "pool", pool, "vm", name, "err", err)
			}
			c.log.Warn("create_lost", "pool", pool, "vm", name, "reason", "boot_timeout")
		default:
			out[u.target]++
		}
	}
	return out
}

func (c *Controller) bump(f func(*Status)) {
	c.mu.Lock()
	f(&c.status)
	c.mu.Unlock()
}

// targetsOf returns the distinct targets of jobs, sorted.
func targetsOf(jobs []waiting) []Target {
	seen := map[Target]bool{}
	var out []Target
	for _, j := range jobs {
		if !seen[j.target] {
			seen[j.target] = true
			out = append(out, j.target)
		}
	}
	slices.SortFunc(out, func(a, b Target) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// rotate returns pools starting at turn mod len, so when the combined cap
// binds, a different pool goes first each tick.
func rotate(pools []config.BurstPool, turn int) []config.BurstPool {
	if len(pools) == 0 {
		return nil
	}
	s := turn % len(pools)
	out := append([]config.BurstPool{}, pools[s:]...)
	return append(out, pools[:s]...)
}

// newVMName appends 8 random hex digits to the pool's prefix.
func newVMName(prefix string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%x", prefix, b), nil
}
