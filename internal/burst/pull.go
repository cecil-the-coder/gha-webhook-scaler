package burst

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// ServeRunnerConfig is POST /v1/runner-config. Each VM the controller created
// gets its one JIT config here, once.
//
// Order of checks (the first failure decides the reply):
//  1. Bearer instance identity token, verified (401).
//  2. Instance name matches a pool prefix (403).
//  3. The VM is pending in that pool (403), and not yet issued (409). The claim
//     is saved before minting.
//  4. Mint the JIT config for the VM's target. On failure the claim is released
//     and the reply is 502, so the VM may ask again.
//
// The reply is {"encoded_jit_config": "..."} and nothing else. The token and
// the config are never logged.
func (c *Controller) ServeRunnerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.reply(w, "", "", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" || c.verify == nil {
		c.reply(w, "", "", "unauthenticated", http.StatusUnauthorized)
		return
	}
	id, err := c.verify.Verify(r.Context(), tok)
	if err != nil {
		// The error names the failed check only, never the token.
		c.log.Info("identity_rejected_reason", "reason", err.Error())
		c.reply(w, "", "", "identity_rejected", http.StatusUnauthorized)
		return
	}
	name := id.InstanceName
	pool, ok := c.cfg.PoolFor(name)
	if !ok {
		c.reply(w, "", "", "unknown_pool", http.StatusForbidden)
		return
	}
	if c.cfg.DryRun {
		c.reply(w, pool.Name, name, "dry_run", http.StatusServiceUnavailable)
		return
	}
	t, err := c.reg.Claim(pool.Name, name)
	switch {
	case errors.Is(err, errIssued):
		c.reply(w, pool.Name, name, "already_issued", http.StatusConflict)
		return
	case errors.Is(err, errNotPending):
		c.reply(w, pool.Name, name, "not_pending", http.StatusForbidden)
		return
	case err != nil:
		c.reply(w, pool.Name, name, "state_error", http.StatusInternalServerError)
		return
	}
	var group int64 = 1
	if oc := c.owners[t.Owner]; oc != nil && t.Repo == "" && oc.RunnerGroupID > 0 {
		group = oc.RunnerGroupID
	}
	scope := github.RunnerScope{Owner: t.Owner, Repo: t.Repo}
	enc, err := c.forge.MintJIT(r.Context(), scope, name, c.runnerLabels(pool, t.Owner), group)
	if err != nil {
		_ = c.reg.Release(pool.Name, name)
		c.log.Error("mint_failed", "pool", pool.Name, "instance", name, "target", t.String(), "err", err)
		c.reply(w, pool.Name, name, "mint_failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"encoded_jit_config": enc})
	c.bump(func(s *Status) { s.ConfigsIssued++ })
	c.log.Info("runner_config", "outcome", "issued", "pool", pool.Name, "instance", name,
		"target", t.String(), "status", http.StatusOK)
}

// reply logs the request's one line and sends the status text only.
func (c *Controller) reply(w http.ResponseWriter, pool, instance, outcome string, code int) {
	c.log.Info("runner_config", "outcome", outcome, "pool", pool, "instance", instance, "status", code)
	http.Error(w, http.StatusText(code), code)
}
