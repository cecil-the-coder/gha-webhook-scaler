package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runner"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/runtime"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/scanner"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/webhook"
)

type nopProvider struct{}

func (nopProvider) Create(context.Context, config.BurstPool, string) error { return nil }
func (nopProvider) Delete(context.Context, string) error                   { return nil }
func (nopProvider) List(context.Context) ([]burst.Instance, error)         { return nil, nil }

// stubRuntime answers only what /metrics asks; other calls panic.
type stubRuntime struct{ runtime.ContainerRuntime }

func (stubRuntime) GetRunningRunners(string) ([]runtime.RunnerContainer, error) { return nil, nil }

type nopForge struct{}

func (nopForge) OwnerType(context.Context, string) (github.OwnerType, error) {
	return github.OwnerOrganization, nil
}
func (nopForge) WaitingJobs(context.Context, string, string) ([]github.WaitingJob, error) {
	return nil, nil
}
func (nopForge) Runners(context.Context, github.RunnerScope) ([]github.Runner, error) {
	return nil, nil
}
func (nopForge) MintJIT(context.Context, github.RunnerScope, string, []string, int64) (string, error) {
	return "", nil
}

func TestRouter_BurstEndpointAndMetrics(t *testing.T) {
	cfg := &config.Config{
		Owners: map[string]*config.OwnerConfig{"acme": {Owner: "acme", GitHubToken: "x", AllRepos: true, Burst: true}},
		Burst: config.BurstConfig{Provider: "gce", PollInterval: time.Minute, MaxVMsTotal: 1, StateDir: t.TempDir(),
			Pools: []config.BurstPool{{Name: "x64", Prefix: "gha-burst-x64-", MaxVMs: 1}}},
	}
	bc, err := burst.New(cfg, nopForge{}, nopProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rm := runner.NewManager(cfg, map[string]*github.Client{}, stubRuntime{})
	app := &Application{
		Config: cfg, RunnerManager: rm, Burst: bc,
		WebhookHandler: webhook.NewHandler(cfg, rm),
		JobScanner:     scanner.NewJobScanner(cfg, map[string]*github.Client{}, rm),
	}
	r := app.SetupRouter()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, config.RunnerConfigPath, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("runner-config without token = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	var body struct {
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body.Metrics["burst"]; !ok {
		t.Errorf("metrics lack a burst section: %s", rec.Body.String())
	}
}

func TestRouter_NoBurstNoEndpoint(t *testing.T) {
	cfg := &config.Config{Owners: map[string]*config.OwnerConfig{}}
	rm := runner.NewManager(cfg, map[string]*github.Client{}, nil)
	app := &Application{Config: cfg, RunnerManager: rm, WebhookHandler: webhook.NewHandler(cfg, rm),
		JobScanner: scanner.NewJobScanner(cfg, map[string]*github.Client{}, rm)}
	rec := httptest.NewRecorder()
	app.SetupRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, config.RunnerConfigPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("runner-config with burst off = %d, want 404", rec.Code)
	}
}
