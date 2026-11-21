package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	awsburst "github.com/cecil-the-coder/gha-webhook-scaler/internal/burst/aws"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst/gce"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// newBurstController builds the burst controller for cfg.Burst.Provider, or
// returns nil when burst runners are off.
func newBurstController(ctx context.Context, cfg *config.Config, clients map[string]*github.Client) (*burst.Controller, error) {
	if !cfg.Burst.Enabled() {
		return nil, nil
	}
	var (
		prov   burst.Provider
		verify burst.IdentityVerifier
	)
	switch cfg.Burst.Provider {
	case config.BurstProviderGCE:
		p, err := gce.New(ctx, cfg.Burst)
		if err != nil {
			return nil, err
		}
		prov = p
		verify = gce.NewVerifier(cfg.Burst.PublicURL, cfg.Burst.GCE.Project, cfg.Burst.GCE.Zone)
	case config.BurstProviderAWS:
		p, err := awsburst.New(ctx, cfg.Burst)
		if err != nil {
			return nil, err
		}
		v, err := awsburst.NewVerifier(ctx, cfg.Burst)
		if err != nil {
			return nil, err
		}
		prov, verify = p, v
	default:
		return nil, fmt.Errorf("burst provider %q is not supported", cfg.Burst.Provider)
	}
	c, err := burst.New(cfg, burst.GitHubForge{Clients: clients}, prov, verify, slog.Default())
	if err != nil {
		return nil, err
	}
	log.Printf("Burst runners: %s, %d pool(s), after %v, max %d VMs, endpoint %s (dry run: %v)",
		cfg.Burst.Provider, len(cfg.Burst.Pools), cfg.Burst.After, cfg.Burst.MaxVMsTotal,
		cfg.Burst.PublicURL, cfg.Burst.DryRun)
	return c, nil
}
