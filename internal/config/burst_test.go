package config

import (
	"strings"
	"testing"
	"time"
)

func burstEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	base := map[string]string{
		"BURST_PROVIDER":    "gce",
		"BURST_GCE_PROJECT": "my-project",
		"BURST_GCE_ZONE":    "us-central1-a",
		"BURST_PUBLIC_URL":  "https://scaler.example.com",
	}
	for k, v := range kv {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

func TestBurstConfig_OffByDefault(t *testing.T) {
	t.Setenv("BURST_PROVIDER", "")
	b := loadBurstConfig()
	if b.Enabled() || b.Validate() != nil {
		t.Fatalf("burst must be off and valid by default: %+v", b)
	}
}

func TestBurstConfig_DefaultsAndPools(t *testing.T) {
	burstEnv(t, map[string]string{
		"BURST_POOLS":                 "x64,arm",
		"BURST_POOL_ARM_MACHINE_TYPE": "t2a-standard-4",
		"BURST_POOL_ARM_LABELS":       "build, big",
		"BURST_POOL_ARM_RUNNER_ENV":   "BUILDX_BUILDER=remote; FOO=bar baz",
		"BURST_POOL_X64_MAX_VMS":      "2",
		"BURST_AFTER":                 "45s",
		"BURST_MAX_VM_HOURS_PER_DAY":  "2.5",
	})
	b := loadBurstConfig()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if b.PublicURL != "https://scaler.example.com/v1/runner-config" {
		t.Errorf("public URL = %s (path should be filled in)", b.PublicURL)
	}
	if b.After != 45*time.Second || b.MaxVMHoursPerDay != 2.5 {
		t.Errorf("after=%v hours=%v", b.After, b.MaxVMHoursPerDay)
	}
	x64, arm := b.Pools[0], b.Pools[1]
	if x64.Arch != "x64" || x64.MaxVMs != 2 || !strings.Contains(x64.Image, "ubuntu-2404-lts-amd64") {
		t.Errorf("x64 pool = %+v", x64)
	}
	if arm.Arch != "arm64" || arm.MaxVMs != 3 || !strings.Contains(arm.Image, "arm64") || arm.Prefix != "gha-burst-arm-" {
		t.Errorf("arm pool = %+v", arm)
	}
	if got := strings.Join(arm.RunnerLabels([]string{"team"}), ","); got != "self-hosted,Linux,ARM64,build,big,team" {
		t.Errorf("runner labels = %s", got)
	}
	if len(arm.RunnerEnv) != 2 || arm.RunnerEnv[1] != "FOO=bar baz" {
		t.Errorf("runner env = %q", arm.RunnerEnv)
	}
	if p, ok := b.PoolFor("gha-burst-arm-0a1b2c3d"); !ok || p.Name != "arm" {
		t.Errorf("PoolFor = %v %v", p.Name, ok)
	}
}

func TestBurstConfig_Rejects(t *testing.T) {
	cases := map[string]map[string]string{
		"provider":     {"BURST_PROVIDER": "aws"},
		"project":      {"BURST_GCE_PROJECT": ""},
		"http url":     {"BURST_PUBLIC_URL": "http://scaler.example.com"},
		"bad path":     {"BURST_PUBLIC_URL": "https://scaler.example.com/hook"},
		"pool name":    {"BURST_POOLS": "X_64"},
		"dup pool":     {"BURST_POOLS": "a,a"},
		"bad arch":     {"BURST_POOL_X64_ARCH": "riscv"},
		"secret env":   {"BURST_POOL_X64_RUNNER_ENV": "NPM_TOKEN=abc"},
		"reserved env": {"BURST_POOL_X64_RUNNER_ENV": "RUNNER_TEMP=/x"},
		"dup env":      {"BURST_POOL_X64_RUNNER_ENV": "A=1;A=2"},
		"bad env":      {"BURST_POOL_X64_RUNNER_ENV": "1A=x"},
		"total cap":    {"BURST_MAX_VMS_TOTAL": "0"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			burstEnv(t, kv)
			if err := loadBurstConfig().Validate(); err == nil {
				t.Errorf("want validation error for %v", kv)
			}
		})
	}
}

func TestOwnerBurstDefaults(t *testing.T) {
	t.Setenv("OWNERS", "acme,bob")
	t.Setenv("OWNER_ACME_GITHUB_TOKEN", "x")
	t.Setenv("OWNER_ACME_ALL_REPOS", "true")
	t.Setenv("OWNER_ACME_RUNNER_GROUP_ID", "4")
	t.Setenv("OWNER_BOB_GITHUB_TOKEN", "x")
	t.Setenv("OWNER_BOB_REPO_NAME", "p")
	t.Setenv("OWNER_BOB_BURST", "false")
	c := NewConfig()
	if !c.Owners["acme"].Burst || c.Owners["acme"].RunnerGroupID != 4 {
		t.Errorf("acme = %+v", c.Owners["acme"])
	}
	if c.Owners["bob"].Burst || c.Owners["bob"].RunnerGroupID != 1 {
		t.Errorf("bob = %+v", c.Owners["bob"])
	}
}
