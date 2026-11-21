package burst

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

var (
	errNotPending = errors.New("instance was not created by this scaler")
	errIssued     = errors.New("config already issued for this instance")
)

// Target is who a VM's runner registers with: an organization (Repo empty) or
// one repository of a user account.
type Target struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo,omitempty"`
}

func (t Target) String() string {
	if t.Repo == "" {
		return t.Owner
	}
	return t.Owner + "/" + t.Repo
}

// Registry records, per pool, which VMs the controller created (pending, with
// their target) and which have been given their one JIT config (issued). Both
// live in STATE_DIR/issued.json, so a restart neither forgets a VM nor
// re-issues a config. A deleted VM leaves pending but stays issued forever.
type Registry struct {
	mu    sync.Mutex
	path  string
	pools map[string]*poolSets
}

type poolSets struct {
	pending map[string]Target
	issued  map[string]bool
}

type registryFile struct {
	Pools map[string]poolFile `json:"pools"`
}

type poolFile struct {
	Pending map[string]Target `json:"pending"`
	Issued  []string          `json:"issued"`
}

// LoadRegistry reads dir/issued.json, or starts empty when it does not exist.
func LoadRegistry(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, "issued.json"), pools: map[string]*poolSets{}}
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", r.path, err)
	}
	for name, pf := range f.Pools {
		ps := r.set(name)
		maps.Copy(ps.pending, pf.Pending)
		for _, n := range pf.Issued {
			ps.issued[n] = true
		}
	}
	return r, nil
}

func (r *Registry) set(pool string) *poolSets {
	ps := r.pools[pool]
	if ps == nil {
		ps = &poolSets{pending: map[string]Target{}, issued: map[string]bool{}}
		r.pools[pool] = ps
	}
	return ps
}

// save writes the file atomically. The caller holds mu.
func (r *Registry) save() error {
	f := registryFile{Pools: make(map[string]poolFile, len(r.pools))}
	for name, ps := range r.pools {
		f.Pools[name] = poolFile{Pending: ps.pending, Issued: slices.Sorted(maps.Keys(ps.issued))}
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// AddPending records a VM about to be created. It is saved before the create
// call, so every VM the controller makes is recognised by the pull endpoint.
func (r *Registry) AddPending(pool, name string, t Target) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps := r.set(pool)
	ps.pending[name] = t
	if err := r.save(); err != nil {
		delete(ps.pending, name)
		return err
	}
	return nil
}

// TargetOf returns the target a pending VM was created for.
func (r *Registry) TargetOf(pool, name string) (Target, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.set(pool).pending[name]
	return t, ok
}

// Forget drops a deleted VM from pending. Its issued entry stays.
func (r *Registry) Forget(pool, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps := r.set(pool)
	if _, ok := ps.pending[name]; !ok {
		return nil
	}
	delete(ps.pending, name)
	return r.save()
}

// Claim reserves the one config for name and returns its target. It fails
// with errIssued if already claimed, or errNotPending if the controller never
// created it in this pool. The claim is saved before the caller mints.
func (r *Registry) Claim(pool, name string) (Target, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps := r.set(pool)
	if ps.issued[name] {
		return Target{}, errIssued
	}
	t, ok := ps.pending[name]
	if !ok {
		return Target{}, errNotPending
	}
	ps.issued[name] = true
	if err := r.save(); err != nil {
		delete(ps.issued, name)
		return Target{}, err
	}
	return t, nil
}

// Release undoes a claim whose mint failed (no config left the scaler). If
// the save fails the name stays issued, which is the safe direction.
func (r *Registry) Release(pool, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps := r.set(pool)
	delete(ps.issued, name)
	if err := r.save(); err != nil {
		ps.issued[name] = true
		return err
	}
	return nil
}

// PendingCount returns how many VMs are recorded as pending, across pools.
func (r *Registry) PendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ps := range r.pools {
		n += len(ps.pending)
	}
	return n
}
