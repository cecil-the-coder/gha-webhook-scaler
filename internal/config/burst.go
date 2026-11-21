package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Burst runners: ephemeral cloud VMs that take queued jobs the primary
// runtime (Docker/Kubernetes) has not picked up. Off unless BURST_PROVIDER is set.
// See docs/burst-runners.md.

const (
	BurstProviderNone = ""
	BurstProviderGCE  = "gce"
	BurstProviderAWS  = "aws"

	// RunnerConfigPath is the path VMs POST to for their one JIT runner config.
	RunnerConfigPath = "/v1/runner-config"

	defaultImageX64   = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64"
	defaultImageARM64 = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-arm64"

	// Canonical's public SSM parameters for the current Ubuntu 24.04 AMIs.
	defaultAWSImageX64   = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
	defaultAWSImageARM64 = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"

	defaultMachineTypeGCE = "e2-standard-4"
	defaultMachineTypeAWS = "m7i.xlarge"
)

// BurstConfig holds the provider-neutral burst settings plus the provider ones.
type BurstConfig struct {
	Provider string

	// After is how long a job must wait before burst counts it. A job of an
	// owner the primary runtime reported as saturated counts at once. 0 makes
	// burst take every matching job (GCE-only setups).
	After        time.Duration
	PollInterval time.Duration
	ActiveWindow time.Duration // how long a repo stays polled after webhook activity

	IdleTimeout time.Duration // delete a registered, idle VM when its queue is empty
	BootTimeout time.Duration // delete a VM that never registered
	MaxVMAge    time.Duration // delete any VM older than this

	MaxVMsTotal      int
	MaxVMHoursPerDay float64 // 0 = unlimited

	StateDir string
	DryRun   bool

	// PublicURL is the https URL VMs call for their runner config. It is also
	// the audience of the identity token they present.
	PublicURL string

	RunnerVersion string // GitHub runner version installed on stock images ("latest" or e.g. "2.330.0")
	InstallDocker bool   // install Docker on stock images that lack it

	Pools []BurstPool
	GCE   GCEConfig
	AWS   AWSConfig
}

// BurstPool is one group of identical VMs.
type BurstPool struct {
	Name        string
	MachineType string   // GCE machine type or EC2 instance type
	Image       string   // GCE image; on AWS an AMI ID or an SSM parameter path
	Arch        string   // "x64" or "arm64"
	Labels      []string // extra runner labels, beyond self-hosted, Linux and the arch
	MaxVMs      int
	DiskGB      int    // 0 = the image's own size
	Prefix      string // VM-name prefix, ends with "-"; names are <Prefix><8 hex>

	// RunnerEnv is non-secret job environment written to the runner's .env.
	// JIT runners skip config.sh/env.sh, so without it jobs start with no
	// image-specific env (e.g. BUILDX_BUILDER). It travels in PUBLIC instance
	// metadata, so credential-looking names are refused.
	RunnerEnv []string // NAME=VALUE

	// Packages are extra apt packages the bootstrap installs on stock images,
	// for jobs that expect them on the host (compilers, -dev headers, fonts).
	// Debian package names only; they travel in PUBLIC instance metadata.
	Packages []string

	// Where a baked image keeps the runner, and the user it runs as. Stock
	// images get the runner installed here.
	RunnerDir  string
	RunnerUser string
}

// GCEConfig holds the Compute Engine settings. Credentials come from
// Application Default Credentials (GOOGLE_APPLICATION_CREDENTIALS, workload
// identity, or the metadata server).
type GCEConfig struct {
	Project          string
	Zone             string
	Network          string
	Subnetwork       string
	VMServiceAccount string // empty = the project's default compute SA
	Spot             bool
	ExternalIP       bool // give VMs an ephemeral public IP (needed for egress without Cloud NAT)
	VMPrefix         string
}

// AWSConfig holds the EC2 settings. Credentials come from the SDK's default
// chain (env, shared config, web identity, ECS/EC2 role).
type AWSConfig struct {
	Region           string
	SubnetIDs        []string // one is picked per VM; capacity errors move on to the next
	SecurityGroupIDs []string // empty = the VPC's default group
	// InstanceProfile is the IAM instance profile (name or ARN) the VMs run as.
	// Its role needs no permissions: VMs only use it to prove who they are.
	InstanceProfile string
	// InstanceRole is the role inside InstanceProfile, as it appears in the
	// VM's assumed-role ARN. Defaults to the profile's name.
	InstanceRole      string
	Spot              bool
	AssociatePublicIP *bool // nil = the subnet's default
	VMPrefix          string

	publicIPRaw string // unparseable BURST_AWS_ASSOCIATE_PUBLIC_IP, reported by Validate
}

// Enabled reports whether burst runners are on.
func (b BurstConfig) Enabled() bool { return b.Provider != BurstProviderNone }

// RunnerLabels returns the labels a pool's JIT runner registers with. A JIT
// config's labels replace GitHub's defaults, so self-hosted, Linux and the
// architecture are added here; without them no `runs-on: [self-hosted, ...]`
// job would ever match.
func (p BurstPool) RunnerLabels(extra []string) []string {
	arch := "X64"
	if p.Arch == "arm64" {
		arch = "ARM64"
	}
	out := []string{"self-hosted", "Linux", arch}
	out = append(out, p.Labels...)
	return append(out, extra...)
}

// PoolFor returns the pool whose prefix name starts with.
func (b BurstConfig) PoolFor(name string) (BurstPool, bool) {
	for _, p := range b.Pools {
		if strings.HasPrefix(name, p.Prefix) {
			return p, true
		}
	}
	return BurstPool{}, false
}

var poolNameRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,11}$`)
var vmPrefixRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// armFamilies are GCE machine families with Arm CPUs.
var armFamilies = []string{"t2a-", "c4a-", "n4a-"}

// ec2FamilyRE splits an EC2 instance family ("c7gn") into its series letters,
// generation and attribute letters ("gn").
var ec2FamilyRE = regexp.MustCompile(`^([a-z]+)([0-9]+)([a-z-]*)$`)

func archForMachineType(provider, mt string) string {
	if provider == BurstProviderAWS {
		// Graviton types carry a "g" in the attributes after the generation
		// (t4g, m7g, c7gn, r8gd, im4gn, is4gen, g5g); a1 is the first Graviton.
		family, _, _ := strings.Cut(strings.ToLower(mt), ".")
		if family == "a1" {
			return "arm64"
		}
		if m := ec2FamilyRE.FindStringSubmatch(family); m != nil && strings.Contains(m[3], "g") {
			return "arm64"
		}
		return "x64"
	}
	for _, f := range armFamilies {
		if strings.HasPrefix(mt, f) {
			return "arm64"
		}
	}
	return "x64"
}

// vmPrefix is the provider's VM-name prefix setting.
func (b BurstConfig) vmPrefix() string {
	if b.Provider == BurstProviderAWS {
		return b.AWS.VMPrefix
	}
	return b.GCE.VMPrefix
}

func loadAWSConfig() AWSConfig {
	a := AWSConfig{
		Region:           getEnv("BURST_AWS_REGION", getEnv("AWS_REGION", getEnv("AWS_DEFAULT_REGION", ""))),
		SubnetIDs:        getEnvList("BURST_AWS_SUBNET_IDS", nil),
		SecurityGroupIDs: getEnvList("BURST_AWS_SECURITY_GROUP_IDS", nil),
		InstanceProfile:  strings.TrimSpace(getEnv("BURST_AWS_INSTANCE_PROFILE", "")),
		InstanceRole:     strings.TrimSpace(getEnv("BURST_AWS_INSTANCE_ROLE", "")),
		Spot:             getEnvBool("BURST_AWS_SPOT", true),
		VMPrefix:         getEnv("BURST_AWS_VM_PREFIX", "gha-burst"),
	}
	if a.InstanceRole == "" && a.InstanceProfile != "" {
		a.InstanceRole = a.InstanceProfile[strings.LastIndex(a.InstanceProfile, "/")+1:]
	}
	if v := strings.TrimSpace(getEnv("BURST_AWS_ASSOCIATE_PUBLIC_IP", "")); v != "" {
		if on, err := strconv.ParseBool(v); err == nil {
			a.AssociatePublicIP = &on
		} else {
			a.publicIPRaw = v
		}
	}
	return a
}

func loadBurstConfig() BurstConfig {
	b := BurstConfig{
		Provider:         strings.ToLower(strings.TrimSpace(getEnv("BURST_PROVIDER", ""))),
		After:            getEnvDuration("BURST_AFTER", 60*time.Second),
		PollInterval:     getEnvDuration("BURST_POLL_INTERVAL", 30*time.Second),
		ActiveWindow:     getEnvDuration("BURST_ACTIVE_WINDOW", 30*time.Minute),
		IdleTimeout:      getEnvDuration("BURST_IDLE_TIMEOUT", 10*time.Minute),
		BootTimeout:      getEnvDuration("BURST_BOOT_TIMEOUT", 15*time.Minute),
		MaxVMAge:         getEnvDuration("BURST_MAX_VM_AGE", 90*time.Minute),
		MaxVMsTotal:      getEnvInt("BURST_MAX_VMS_TOTAL", 3),
		MaxVMHoursPerDay: getEnvFloat("BURST_MAX_VM_HOURS_PER_DAY", 8),
		StateDir:         getEnv("BURST_STATE_DIR", "/var/lib/gha-webhook-scaler/burst"),
		DryRun:           getEnvBool("BURST_DRY_RUN", false),
		PublicURL:        strings.TrimSpace(getEnv("BURST_PUBLIC_URL", "")),
		RunnerVersion:    getEnv("BURST_RUNNER_VERSION", "latest"),
		InstallDocker:    getEnvBool("BURST_INSTALL_DOCKER", true),
		GCE: GCEConfig{
			Project:          getEnv("BURST_GCE_PROJECT", ""),
			Zone:             getEnv("BURST_GCE_ZONE", ""),
			Network:          getEnv("BURST_GCE_NETWORK", "default"),
			Subnetwork:       getEnv("BURST_GCE_SUBNETWORK", ""),
			VMServiceAccount: getEnv("BURST_GCE_VM_SERVICE_ACCOUNT", ""),
			Spot:             getEnvBool("BURST_GCE_SPOT", true),
			ExternalIP:       getEnvBool("BURST_GCE_EXTERNAL_IP", true),
			VMPrefix:         getEnv("BURST_GCE_VM_PREFIX", "gha-burst"),
		},
		AWS: loadAWSConfig(),
	}
	defMachine, defX64, defARM := defaultMachineTypeGCE, defaultImageX64, defaultImageARM64
	if b.Provider == BurstProviderAWS {
		defMachine, defX64, defARM = defaultMachineTypeAWS, defaultAWSImageX64, defaultAWSImageARM64
	}
	if b.PublicURL != "" {
		if u, err := url.Parse(b.PublicURL); err == nil && (u.Path == "" || u.Path == "/") {
			u.Path = RunnerConfigPath
			b.PublicURL = u.String()
		}
	}
	for _, name := range getEnvList("BURST_POOLS", []string{"x64"}) {
		name = strings.ToLower(name)
		env := "BURST_POOL_" + strings.ToUpper(name) + "_"
		p := BurstPool{
			Name:        name,
			MachineType: getEnv(env+"MACHINE_TYPE", defMachine),
			Image:       getEnv(env+"IMAGE", ""),
			Arch:        strings.ToLower(getEnv(env+"ARCH", "")),
			Labels:      getEnvList(env+"LABELS", nil),
			MaxVMs:      getEnvInt(env+"MAX_VMS", 0),
			DiskGB:      getEnvInt(env+"DISK_GB", 50),
			Prefix:      fmt.Sprintf("%s-%s-", b.vmPrefix(), name),
			RunnerEnv:   splitEnvPairs(getEnv(env+"RUNNER_ENV", "")),
			Packages:    getEnvList(env+"PACKAGES", nil),
			RunnerDir:   getEnv(env+"RUNNER_DIR", "/opt/actions-runner"),
			RunnerUser:  getEnv(env+"RUNNER_USER", "runner"),
		}
		if p.Arch == "" {
			p.Arch = archForMachineType(b.Provider, p.MachineType)
		}
		if p.Image == "" {
			p.Image = defX64
			if p.Arch == "arm64" {
				p.Image = defARM
			}
		}
		if p.MaxVMs <= 0 {
			p.MaxVMs = b.MaxVMsTotal
		}
		b.Pools = append(b.Pools, p)
	}
	return b
}

// Validate checks the burst settings. It does nothing when burst is off.
func (b BurstConfig) Validate() error {
	if !b.Enabled() {
		return nil
	}
	switch b.Provider {
	case BurstProviderGCE:
		if b.GCE.Project == "" || b.GCE.Zone == "" {
			return fmt.Errorf("BURST_GCE_PROJECT and BURST_GCE_ZONE are required")
		}
		if !vmPrefixRE.MatchString(b.GCE.VMPrefix) {
			return fmt.Errorf("BURST_GCE_VM_PREFIX %q must be lowercase letters, digits and dashes", b.GCE.VMPrefix)
		}
	case BurstProviderAWS:
		if err := b.AWS.validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("BURST_PROVIDER %q is not supported (use %q or %q)", b.Provider, BurstProviderGCE, BurstProviderAWS)
	}
	u, err := url.Parse(b.PublicURL)
	if b.PublicURL == "" || err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("BURST_PUBLIC_URL must be an https URL that VMs can reach (e.g. https://scaler.example.com%s)", RunnerConfigPath)
	}
	if u.Path != RunnerConfigPath {
		return fmt.Errorf("BURST_PUBLIC_URL path must be %s, got %q", RunnerConfigPath, u.Path)
	}
	if b.MaxVMsTotal <= 0 {
		return fmt.Errorf("BURST_MAX_VMS_TOTAL must be greater than 0")
	}
	if b.MaxVMHoursPerDay < 0 {
		return fmt.Errorf("BURST_MAX_VM_HOURS_PER_DAY must not be negative")
	}
	if b.PollInterval < 5*time.Second {
		return fmt.Errorf("BURST_POLL_INTERVAL must be at least 5s")
	}
	if b.BootTimeout <= 0 || b.IdleTimeout <= 0 || b.MaxVMAge <= 0 {
		return fmt.Errorf("BURST_BOOT_TIMEOUT, BURST_IDLE_TIMEOUT and BURST_MAX_VM_AGE must be positive")
	}
	if b.StateDir == "" {
		return fmt.Errorf("BURST_STATE_DIR is required")
	}
	if len(b.Pools) == 0 {
		return fmt.Errorf("BURST_POOLS must name at least one pool")
	}
	seen := map[string]bool{}
	for _, p := range b.Pools {
		if !poolNameRE.MatchString(p.Name) {
			return fmt.Errorf("burst pool name %q must be 1-12 lowercase letters/digits, starting with a letter", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("burst pool %q is listed twice", p.Name)
		}
		seen[p.Name] = true
		if p.MachineType == "" || p.Image == "" {
			return fmt.Errorf("burst pool %s needs a machine type and an image", p.Name)
		}
		if b.Provider == BurstProviderAWS {
			if !ec2TypeRE.MatchString(p.MachineType) {
				return fmt.Errorf("burst pool %s: %q is not an EC2 instance type (e.g. m7i.xlarge)", p.Name, p.MachineType)
			}
			if !awsImageRE.MatchString(p.Image) {
				return fmt.Errorf("burst pool %s: image must be an AMI ID (ami-...) or an SSM parameter path (/...), got %q", p.Name, p.Image)
			}
		}
		if p.Arch != "x64" && p.Arch != "arm64" {
			return fmt.Errorf("burst pool %s: arch must be x64 or arm64, got %q", p.Name, p.Arch)
		}
		if p.DiskGB != 0 && p.DiskGB < 10 {
			return fmt.Errorf("burst pool %s: disk must be at least 10 GB (or 0 for the image's size)", p.Name)
		}
		if !runnerDirRE.MatchString(p.RunnerDir) || strings.Contains(p.RunnerDir, "..") {
			return fmt.Errorf("BURST_POOL_%s_RUNNER_DIR %q must be an absolute path", strings.ToUpper(p.Name), p.RunnerDir)
		}
		if !runnerUserRE.MatchString(p.RunnerUser) || p.RunnerUser == "root" {
			return fmt.Errorf("BURST_POOL_%s_RUNNER_USER %q must be a non-root user name", strings.ToUpper(p.Name), p.RunnerUser)
		}
		if err := validateRunnerEnv(p.RunnerEnv); err != nil {
			return fmt.Errorf("BURST_POOL_%s_RUNNER_ENV: %w", strings.ToUpper(p.Name), err)
		}
		if len(p.Packages) > maxPoolPackages {
			return fmt.Errorf("BURST_POOL_%s_PACKAGES lists %d packages, over %d", strings.ToUpper(p.Name), len(p.Packages), maxPoolPackages)
		}
		for _, pkg := range p.Packages {
			if !aptPackageRE.MatchString(pkg) {
				return fmt.Errorf("BURST_POOL_%s_PACKAGES: %q is not a Debian package name", strings.ToUpper(p.Name), pkg)
			}
		}
	}
	return nil
}

// aptPackageRE is a Debian package name (policy 5.6.1), no version or arch suffix.
var aptPackageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

const maxPoolPackages = 64

var (
	awsRegionRE   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	awsSubnetRE   = regexp.MustCompile(`^subnet-[0-9a-f]{8,17}$`)
	awsSGRE       = regexp.MustCompile(`^sg-[0-9a-f]{8,17}$`)
	awsProfileRE  = regexp.MustCompile(`^([A-Za-z0-9+=,.@_-]{1,128}|arn:aws[a-z-]*:iam::[0-9]{12}:instance-profile/([A-Za-z0-9+=,.@_/-]+/)?[A-Za-z0-9+=,.@_-]{1,128})$`)
	awsRoleNameRE = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
	ec2TypeRE     = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z0-9-]+$`)
	awsImageRE    = regexp.MustCompile(`^(ami-[0-9a-f]{8,17}|(resolve:ssm:)?/[A-Za-z0-9_./-]+)$`)
)

func (a AWSConfig) validate() error {
	if !awsRegionRE.MatchString(a.Region) {
		return fmt.Errorf("BURST_AWS_REGION (or AWS_REGION) must be a region such as us-east-1, got %q", a.Region)
	}
	if len(a.SubnetIDs) == 0 {
		return fmt.Errorf("BURST_AWS_SUBNET_IDS is required (comma-separated subnet IDs)")
	}
	for _, s := range a.SubnetIDs {
		if !awsSubnetRE.MatchString(s) {
			return fmt.Errorf("BURST_AWS_SUBNET_IDS: %q is not a subnet ID", s)
		}
	}
	for _, s := range a.SecurityGroupIDs {
		if !awsSGRE.MatchString(s) {
			return fmt.Errorf("BURST_AWS_SECURITY_GROUP_IDS: %q is not a security group ID", s)
		}
	}
	if !awsProfileRE.MatchString(a.InstanceProfile) {
		return fmt.Errorf("BURST_AWS_INSTANCE_PROFILE must be an instance profile name or ARN, got %q", a.InstanceProfile)
	}
	if !awsRoleNameRE.MatchString(a.InstanceRole) {
		return fmt.Errorf("BURST_AWS_INSTANCE_ROLE %q must be a role name (not an ARN)", a.InstanceRole)
	}
	if a.publicIPRaw != "" {
		return fmt.Errorf("BURST_AWS_ASSOCIATE_PUBLIC_IP %q must be true or false (or unset for the subnet default)", a.publicIPRaw)
	}
	if !vmPrefixRE.MatchString(a.VMPrefix) {
		return fmt.Errorf("BURST_AWS_VM_PREFIX %q must be lowercase letters, digits and dashes", a.VMPrefix)
	}
	return nil
}

var runnerDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
var runnerUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var envLineRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=[^[:cntrl:]]*$`)
var secretNameRE = regexp.MustCompile(`(?i)TOKEN|SECRET|KEY|PASSW|AUTH|CREDENTIAL|PAT`)

// splitEnvPairs splits "A=1;B=2" into ["A=1", "B=2"].
func splitEnvPairs(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// validateRunnerEnv refuses malformed lines, duplicates, credential-looking
// names and the GITHUB_/RUNNER_/ACTIONS_ namespaces the runner owns.
func validateRunnerEnv(lines []string) error {
	seen := map[string]bool{}
	for _, l := range lines {
		if !envLineRE.MatchString(l) {
			return fmt.Errorf("%q is not NAME=VALUE without control characters", l)
		}
		name, _, _ := strings.Cut(l, "=")
		up := strings.ToUpper(name)
		if secretNameRE.MatchString(up) {
			return fmt.Errorf("%s looks like a credential; it would be public in instance metadata", name)
		}
		for _, ns := range []string{"GITHUB_", "RUNNER_", "ACTIONS_"} {
			if strings.HasPrefix(up, ns) {
				return fmt.Errorf("%s is in the reserved %s namespace", name, ns)
			}
		}
		if seen[name] {
			return fmt.Errorf("%s is set twice", name)
		}
		seen[name] = true
	}
	return nil
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(getEnv(key, ""))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil { // bare number = seconds
		return time.Duration(n) * time.Second
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(getEnv(key, "")), 64); err == nil {
		return f
	}
	return def
}

// getEnvList splits a comma-separated value, trimming blanks and dropping empties.
func getEnvList(key string, def []string) []string {
	v := getEnv(key, "")
	if v == "" {
		return def
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
