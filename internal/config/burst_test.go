package config

import (
	"os"
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
		"BURST_POOL_ARM_PACKAGES":     "build-essential, libfreetype-dev,g++",
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
	if got := strings.Join(arm.Packages, " "); got != "build-essential libfreetype-dev g++" || len(x64.Packages) != 0 {
		t.Errorf("packages = %q / %q", arm.Packages, x64.Packages)
	}
	if p, ok := b.PoolFor("gha-burst-arm-0a1b2c3d"); !ok || p.Name != "arm" {
		t.Errorf("PoolFor = %v %v", p.Name, ok)
	}
}

func TestBurstConfig_Rejects(t *testing.T) {
	cases := map[string]map[string]string{
		"provider":     {"BURST_PROVIDER": "azure"},
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
		"pkg version":  {"BURST_POOL_X64_PACKAGES": "cmake=3.28"},
		"pkg option":   {"BURST_POOL_X64_PACKAGES": "-o,cmake"},
		"pkg upper":    {"BURST_POOL_X64_PACKAGES": "CMake"},
		"pkg space":    {"BURST_POOL_X64_PACKAGES": "cmake make"},
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

func awsBurstEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	base := map[string]string{
		"BURST_PROVIDER":             "aws",
		"BURST_AWS_REGION":           "us-east-1",
		"BURST_AWS_SUBNET_IDS":       "subnet-0123456789abcdef0, subnet-0fedcba9876543210",
		"BURST_AWS_INSTANCE_PROFILE": "gha-burst-runner",
		"BURST_PUBLIC_URL":           "https://scaler.example.com",
		"AWS_REGION":                 "",
		"AWS_DEFAULT_REGION":         "",
	}
	for k, v := range kv {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

func TestBurstConfig_AWSDefaults(t *testing.T) {
	awsBurstEnv(t, map[string]string{
		"BURST_POOLS":                 "x64,arm",
		"BURST_POOL_ARM_MACHINE_TYPE": "c7g.2xlarge",
		"BURST_AWS_VM_PREFIX":         "ci",
	})
	b := loadBurstConfig()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	a := b.AWS
	if len(a.SubnetIDs) != 2 || a.SubnetIDs[1] != "subnet-0fedcba9876543210" || !a.Spot || a.AssociatePublicIP != nil {
		t.Errorf("aws = %+v", a)
	}
	if a.InstanceRole != "gha-burst-runner" {
		t.Errorf("role defaults to the profile name, got %q", a.InstanceRole)
	}
	x64, arm := b.Pools[0], b.Pools[1]
	if x64.MachineType != "m7i.xlarge" || x64.Arch != "x64" || x64.Image != defaultAWSImageX64 || x64.Prefix != "ci-x64-" {
		t.Errorf("x64 pool = %+v", x64)
	}
	if arm.Arch != "arm64" || arm.Image != defaultAWSImageARM64 || arm.Prefix != "ci-arm-" {
		t.Errorf("arm pool = %+v", arm)
	}
}

func TestBurstConfig_AWSOptions(t *testing.T) {
	awsBurstEnv(t, map[string]string{
		"BURST_AWS_REGION":              "",
		"AWS_REGION":                    "eu-west-1",
		"BURST_AWS_INSTANCE_PROFILE":    "arn:aws:iam::123456789012:instance-profile/ci/gha-burst-profile",
		"BURST_AWS_SECURITY_GROUP_IDS":  "sg-0123456789abcdef0",
		"BURST_AWS_ASSOCIATE_PUBLIC_IP": "false",
		"BURST_AWS_SPOT":                "false",
		"BURST_POOL_X64_IMAGE":          "ami-0123456789abcdef0",
	})
	b := loadBurstConfig()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	a := b.AWS
	if a.Region != "eu-west-1" || a.Spot || a.AssociatePublicIP == nil || *a.AssociatePublicIP {
		t.Errorf("aws = %+v", a)
	}
	if a.InstanceRole != "gha-burst-profile" {
		t.Errorf("role from profile ARN = %q", a.InstanceRole)
	}
	if b.Pools[0].Image != "ami-0123456789abcdef0" {
		t.Errorf("image = %s", b.Pools[0].Image)
	}
}

func TestBurstConfig_AWSRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"no region":      {"BURST_AWS_REGION": ""},
		"no subnets":     {"BURST_AWS_SUBNET_IDS": ""},
		"bad subnet":     {"BURST_AWS_SUBNET_IDS": "vpc-0123456789abcdef0"},
		"bad sg":         {"BURST_AWS_SECURITY_GROUP_IDS": "default"},
		"no profile":     {"BURST_AWS_INSTANCE_PROFILE": ""},
		"role arn":       {"BURST_AWS_INSTANCE_ROLE": "arn:aws:iam::123456789012:role/x"},
		"bad public ip":  {"BURST_AWS_ASSOCIATE_PUBLIC_IP": "maybe"},
		"bad prefix":     {"BURST_AWS_VM_PREFIX": "CI"},
		"gce type":       {"BURST_POOL_X64_MACHINE_TYPE": "e2-standard-4"},
		"gce image":      {"BURST_POOL_X64_IMAGE": "projects/ubuntu-os-cloud/global/images/family/x"},
		"shared checks":  {"BURST_PUBLIC_URL": "http://scaler.example.com"},
		"secret env aws": {"BURST_POOL_X64_RUNNER_ENV": "AWS_SECRET_ACCESS_KEY=x"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			awsBurstEnv(t, kv)
			if err := loadBurstConfig().Validate(); err == nil {
				t.Errorf("want validation error for %v", kv)
			}
		})
	}
}

func TestArchForMachineType(t *testing.T) {
	cases := []struct{ provider, mt, want string }{
		{BurstProviderGCE, "t2a-standard-4", "arm64"},
		{BurstProviderGCE, "c4a-highcpu-8", "arm64"},
		{BurstProviderGCE, "n4a-standard-2", "arm64"},
		{BurstProviderGCE, "e2-standard-4", "x64"},
		{BurstProviderGCE, "t4g.large", "x64"}, // GCE rules only
		{BurstProviderAWS, "t4g.large", "arm64"},
		{BurstProviderAWS, "m7g.xlarge", "arm64"},
		{BurstProviderAWS, "c7g.2xlarge", "arm64"},
		{BurstProviderAWS, "c7gn.large", "arm64"},
		{BurstProviderAWS, "r8g.large", "arm64"},
		{BurstProviderAWS, "m6gd.large", "arm64"},
		{BurstProviderAWS, "im4gn.large", "arm64"},
		{BurstProviderAWS, "is4gen.large", "arm64"},
		{BurstProviderAWS, "g5g.xlarge", "arm64"},
		{BurstProviderAWS, "a1.large", "arm64"},
		{BurstProviderAWS, "m7i.xlarge", "x64"},
		{BurstProviderAWS, "m7i-flex.large", "x64"},
		{BurstProviderAWS, "c6a.large", "x64"},
		{BurstProviderAWS, "g4dn.xlarge", "x64"},
		{BurstProviderAWS, "g5.xlarge", "x64"},
		{BurstProviderAWS, "t3.medium", "x64"},
		{BurstProviderAWS, "t2a-standard-4", "x64"}, // EC2 rules only
	}
	for _, c := range cases {
		if got := archForMachineType(c.provider, c.mt); got != c.want {
			t.Errorf("%s %s: arch %s, want %s", c.provider, c.mt, got, c.want)
		}
	}
}

func TestRuntimeNoneSecretsFilesAndBurstRepos(t *testing.T) {
	dir := t.TempDir()
	tok := dir + "/pat"
	if err := os.WriteFile(tok, []byte("  ghp_fromfile\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	burstEnv(t, map[string]string{
		"RUNTIME":                        "none",
		"OWNERS":                         "my-org",
		"OWNER_MY_ORG_GITHUB_TOKEN_FILE": tok,
		"OWNER_MY_ORG_ALL_REPOS":         "true",
		"OWNER_MY_ORG_BURST_REPOS":       "app, lib",
		"BURST_POOL_X64_RUNNER_DIR":      "/home/ghrunner/actions-runner",
		"BURST_POOL_X64_RUNNER_USER":     "ghrunner",
	})
	c := NewConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	oc := c.Owners["my-org"]
	if c.Runtime != RuntimeTypeNone || oc.GitHubToken != "ghp_fromfile" {
		t.Errorf("runtime=%s token=%q", c.Runtime, oc.GitHubToken)
	}
	if strings.Join(oc.BurstRepos, ",") != "app,lib" {
		t.Errorf("burst repos = %v", oc.BurstRepos)
	}
	if p := c.Burst.Pools[0]; p.RunnerDir != "/home/ghrunner/actions-runner" || p.RunnerUser != "ghrunner" {
		t.Errorf("pool = %+v", p)
	}
}

func TestLoadErrorsAndPairs(t *testing.T) {
	t.Setenv("OWNERS", "acme")
	t.Setenv("OWNER_ACME_GITHUB_TOKEN_FILE", "/nonexistent/pat")
	t.Setenv("OWNER_ACME_ALL_REPOS", "true")
	if err := NewConfig().Validate(); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN_FILE") {
		t.Errorf("unreadable secret file must fail validation, got %v", err)
	}

	t.Setenv("OWNER_ACME_GITHUB_TOKEN_FILE", "")
	t.Setenv("OWNER_ACME_GITHUB_TOKEN", "x")
	t.Setenv("RUNTIME", "none")
	t.Setenv("BURST_PROVIDER", "")
	if err := NewConfig().Validate(); err == nil {
		t.Error("RUNTIME=none without burst must fail")
	}
	t.Setenv("RUNTIME", "docker")
	t.Setenv("TLS_CERT_FILE", "/x.crt")
	if err := NewConfig().Validate(); err == nil {
		t.Error("TLS cert without key must fail")
	}
}

func TestRunnerDirUserValidation(t *testing.T) {
	for _, kv := range []map[string]string{
		{"BURST_POOL_X64_RUNNER_DIR": "relative/path"},
		{"BURST_POOL_X64_RUNNER_DIR": "/opt/../etc"},
		{"BURST_POOL_X64_RUNNER_DIR": "/opt/a b"},
		{"BURST_POOL_X64_RUNNER_USER": "root"},
		{"BURST_POOL_X64_RUNNER_USER": "Bad User"},
	} {
		burstEnv(t, kv)
		if err := loadBurstConfig().Validate(); err == nil {
			t.Errorf("want error for %v", kv)
		}
	}
}
