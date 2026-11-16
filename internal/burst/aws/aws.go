// Package aws is the EC2 provider for burst runners. It uses the official AWS
// SDK for Go v2 and the SDK's default credential chain.
package aws

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

//go:embed bootstrap/runner-bootstrap.sh
var bootstrapScript string

// settingsPlaceholder is replaced, once, by the VM's base64 JSON settings.
const settingsPlaceholder = "__GHA_BURST_SETTINGS__"

// BootstrapScript returns the user-data script template every burst VM runs.
func BootstrapScript() string { return bootstrapScript }

const (
	tagName      = "Name"
	tagManagedBy = "managed-by"
	tagValue     = "gha-webhook-scaler"
	tagPool      = "gha-scaler-pool"

	// imageCacheTTL bounds how long an SSM-resolved AMI is reused.
	imageCacheTTL = time.Hour
	// maxUserData is EC2's limit on user-data before base64.
	maxUserData = 16 * 1024
)

// capacityCodes mean no VM can be had right now. zonalCodes are specific to
// one subnet's AZ, so Create tries the next subnet before giving up.
var (
	accountCapacityCodes = []string{
		"MaxSpotInstanceCountExceeded", "VcpuLimitExceeded", "InstanceLimitExceeded",
	}
	zonalCapacityCodes = []string{
		"InsufficientInstanceCapacity", "InsufficientHostCapacity", "InsufficientCapacity",
		"InsufficientReservedInstanceCapacity", "SpotMaxPriceTooLow",
		"InsufficientFreeAddressesInSubnet",
	}
	// zonalRetryCodes move on to the next subnet but are not capacity.
	zonalRetryCodes = []string{"Unsupported"}
)

// RunError is a RunInstances call EC2 refused.
type RunError struct {
	Subnet  string
	Code    string
	Message string
}

func (e *RunError) Error() string {
	return fmt.Sprintf("RunInstances in %s refused: %s: %s", e.Subnet, e.Code, e.Message)
}

// CapacityCode implements burst.CapacityError.
func (e *RunError) CapacityCode() string {
	if slices.Contains(accountCapacityCodes, e.Code) || slices.Contains(zonalCapacityCodes, e.Code) {
		return e.Code
	}
	return ""
}

func (e *RunError) zonal() bool {
	return slices.Contains(zonalCapacityCodes, e.Code) || slices.Contains(zonalRetryCodes, e.Code)
}

// ec2API is the slice of the EC2 SDK the provider and verifier call. Tests fake it.
type ec2API interface {
	RunInstances(ctx context.Context, in *ec2.RunInstancesInput, opts ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, opts ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeImages(ctx context.Context, in *ec2.DescribeImagesInput, opts ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
}

// ssmAPI resolves SSM image parameters. Tests fake it.
type ssmAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type image struct {
	ami, rootDevice string
	expires         time.Time
}

// Provider implements burst.Provider on EC2.
type Provider struct {
	cfg  config.BurstConfig
	ec2  ec2API
	ssm  ssmAPI
	now  func() time.Time
	next atomic.Uint32 // round-robin subnet start

	mu     sync.Mutex
	images map[string]image // pool image -> resolved AMI
}

// New connects to EC2 and SSM in cfg.AWS.Region with the default credential chain.
func New(ctx context.Context, cfg config.BurstConfig) (*Provider, error) {
	ac, err := loadSDKConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newProvider(cfg, ec2.NewFromConfig(ac), ssm.NewFromConfig(ac)), nil
}

func newProvider(cfg config.BurstConfig, e ec2API, s ssmAPI) *Provider {
	return &Provider{cfg: cfg, ec2: e, ssm: s, now: time.Now, images: map[string]image{}}
}

func loadSDKConfig(ctx context.Context, cfg config.BurstConfig) (awssdk.Config, error) {
	ac, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWS.Region))
	if err != nil {
		return awssdk.Config{}, fmt.Errorf("aws config (check the default credential chain): %w", err)
	}
	return ac, nil
}

// Close is a no-op; the SDK clients hold nothing to release.
func (p *Provider) Close() error { return nil }

// Create launches one VM of the pool's instance type and image. Its user-data
// holds only PUBLIC items: the bootstrap script and its settings (the URL it
// pulls its runner config from, which is also the identity audience, the
// runner version, the Docker switch and the pool's non-secret runner env).
// No token, key or JIT config is ever put in user-data or tags.
//
// Subnets are tried in turn, starting at a rotating index; a capacity error
// specific to one AZ moves on to the next. The VM name is the idempotency
// token, so an SDK retry never launches a second VM.
func (p *Provider) Create(ctx context.Context, pool config.BurstPool, name string) error {
	ami, root, err := p.resolveImage(ctx, pool.Image)
	if err != nil {
		return fmt.Errorf("image %s: %w", pool.Image, err)
	}
	userData, err := p.userData(pool)
	if err != nil {
		return err
	}
	subnets := p.cfg.AWS.SubnetIDs
	if len(subnets) == 0 {
		return errors.New("no subnets configured")
	}
	start := int(p.next.Add(1)-1) % len(subnets)
	var firstCap *RunError
	var last error
	for i := range subnets {
		subnet := subnets[(start+i)%len(subnets)]
		_, err := p.ec2.RunInstances(ctx, p.runInput(pool, name, ami, root, subnet, userData))
		if err == nil {
			return nil
		}
		var api smithy.APIError
		if !errors.As(err, &api) {
			// No answer from EC2 (timeout, connection reset). The launch may
			// have happened; the client token makes a later retry safe.
			return fmt.Errorf("%w: RunInstances in %s: %v", burst.ErrOutcomeUnknown, subnet, err)
		}
		re := &RunError{Subnet: subnet, Code: api.ErrorCode(), Message: api.ErrorMessage()}
		if !re.zonal() {
			return re
		}
		if firstCap == nil && re.CapacityCode() != "" {
			firstCap = re
		}
		last = re
	}
	if firstCap != nil {
		return firstCap
	}
	return last
}

func (p *Provider) runInput(pool config.BurstPool, name, ami, root, subnet, userData string) *ec2.RunInstancesInput {
	a := p.cfg.AWS
	nic := ec2types.InstanceNetworkInterfaceSpecification{
		DeviceIndex:              awssdk.Int32(0),
		SubnetId:                 awssdk.String(subnet),
		DeleteOnTermination:      awssdk.Bool(true),
		Groups:                   a.SecurityGroupIDs,
		AssociatePublicIpAddress: a.AssociatePublicIP,
	}
	profile := &ec2types.IamInstanceProfileSpecification{}
	if strings.HasPrefix(a.InstanceProfile, "arn:") {
		profile.Arn = awssdk.String(a.InstanceProfile)
	} else {
		profile.Name = awssdk.String(a.InstanceProfile)
	}
	tags := []ec2types.Tag{
		{Key: awssdk.String(tagName), Value: awssdk.String(name)},
		{Key: awssdk.String(tagManagedBy), Value: awssdk.String(tagValue)},
		{Key: awssdk.String(tagPool), Value: awssdk.String(pool.Name)},
	}
	in := &ec2.RunInstancesInput{
		ImageId:                           awssdk.String(ami),
		InstanceType:                      ec2types.InstanceType(pool.MachineType),
		MinCount:                          awssdk.Int32(1),
		MaxCount:                          awssdk.Int32(1),
		ClientToken:                       awssdk.String(name),
		NetworkInterfaces:                 []ec2types.InstanceNetworkInterfaceSpecification{nic},
		IamInstanceProfile:                profile,
		InstanceInitiatedShutdownBehavior: ec2types.ShutdownBehaviorTerminate, // poweroff = gone
		MetadataOptions: &ec2types.InstanceMetadataOptionsRequest{
			HttpEndpoint: ec2types.InstanceMetadataEndpointStateEnabled,
			HttpTokens:   ec2types.HttpTokensStateRequired, // IMDSv2 only
			// 1 hop: containers on a bridge network cannot get an IMDS token.
			HttpPutResponseHopLimit: awssdk.Int32(1),
			InstanceMetadataTags:    ec2types.InstanceMetadataTagsStateDisabled,
		},
		UserData: awssdk.String(userData),
		TagSpecifications: []ec2types.TagSpecification{
			{ResourceType: ec2types.ResourceTypeInstance, Tags: tags},
			{ResourceType: ec2types.ResourceTypeVolume, Tags: tags},
		},
	}
	if root != "" && pool.DiskGB > 0 {
		in.BlockDeviceMappings = []ec2types.BlockDeviceMapping{{
			DeviceName: awssdk.String(root),
			Ebs: &ec2types.EbsBlockDevice{
				VolumeSize:          awssdk.Int32(int32(pool.DiskGB)),
				VolumeType:          ec2types.VolumeTypeGp3,
				DeleteOnTermination: awssdk.Bool(true),
			},
		}}
	}
	if a.Spot {
		in.InstanceMarketOptions = &ec2types.InstanceMarketOptionsRequest{
			MarketType: ec2types.MarketTypeSpot,
			SpotOptions: &ec2types.SpotMarketOptions{
				SpotInstanceType:             ec2types.SpotInstanceTypeOneTime,
				InstanceInterruptionBehavior: ec2types.InstanceInterruptionBehaviorTerminate,
			},
		}
	}
	return in
}

// vmSettings is what the bootstrap reads from its user-data. All PUBLIC.
type vmSettings struct {
	ConfigURL     string   `json:"config_url"`
	RunnerVersion string   `json:"runner_version"`
	InstallDocker bool     `json:"install_docker"`
	RunnerEnv     []string `json:"runner_env"`
}

// userData renders the bootstrap with the pool's settings, gzipped (cloud-init
// unpacks it) and base64-encoded for RunInstances.
func (p *Provider) userData(pool config.BurstPool) (string, error) {
	raw, err := RenderUserData(p.cfg, pool)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write([]byte(raw))
	if err := zw.Close(); err != nil {
		return "", err
	}
	if buf.Len() > maxUserData {
		return "", fmt.Errorf("user-data is %d bytes gzipped, over EC2's %d", buf.Len(), maxUserData)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// RenderUserData returns the bootstrap script with the pool's settings filled
// in (uncompressed).
func RenderUserData(cfg config.BurstConfig, pool config.BurstPool) (string, error) {
	s, err := json.Marshal(vmSettings{
		ConfigURL:     cfg.PublicURL,
		RunnerVersion: cfg.RunnerVersion,
		InstallDocker: cfg.InstallDocker,
		RunnerEnv:     append([]string{}, pool.RunnerEnv...),
	})
	if err != nil {
		return "", err
	}
	if strings.Count(bootstrapScript, settingsPlaceholder) != 1 {
		return "", errors.New("bootstrap script has no single settings placeholder")
	}
	return strings.Replace(bootstrapScript, settingsPlaceholder, base64.StdEncoding.EncodeToString(s), 1), nil
}

// resolveImage returns the AMI ID and its root device name for a pool image:
// an AMI ID, an SSM parameter path, or resolve:ssm:<path>.
func (p *Provider) resolveImage(ctx context.Context, img string) (ami, root string, err error) {
	p.mu.Lock()
	c, ok := p.images[img]
	p.mu.Unlock()
	if ok && p.now().Before(c.expires) {
		return c.ami, c.rootDevice, nil
	}
	ami = img
	if path, isSSM := ssmPath(img); isSSM {
		out, err := p.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: awssdk.String(path)})
		if err != nil {
			return "", "", fmt.Errorf("ssm parameter: %w", err)
		}
		ami = strings.TrimSpace(awssdk.ToString(out.Parameter.Value))
		if !strings.HasPrefix(ami, "ami-") {
			return "", "", fmt.Errorf("ssm parameter %s is not an AMI ID", path)
		}
	}
	imgs, err := p.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{ami}})
	if err != nil {
		return "", "", fmt.Errorf("describe image %s: %w", ami, err)
	}
	if len(imgs.Images) != 1 {
		return "", "", fmt.Errorf("image %s not found", ami)
	}
	root = awssdk.ToString(imgs.Images[0].RootDeviceName)
	p.mu.Lock()
	p.images[img] = image{ami: ami, rootDevice: root, expires: p.now().Add(imageCacheTTL)}
	p.mu.Unlock()
	return ami, root, nil
}

func ssmPath(img string) (string, bool) {
	if s, ok := strings.CutPrefix(img, "resolve:ssm:"); ok {
		return s, true
	}
	return img, strings.HasPrefix(img, "/")
}

// Delete terminates the VM with this Name tag. A VM that is already gone is
// not an error.
func (p *Provider) Delete(ctx context.Context, name string) error {
	insts, err := p.describe(ctx, []ec2types.Filter{
		{Name: awssdk.String("tag:" + tagName), Values: []string{name}},
	})
	if err != nil {
		return err
	}
	var ids []string
	for _, in := range insts {
		ids = append(ids, awssdk.ToString(in.InstanceId))
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = p.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids})
	return err
}

// List returns this scaler's VMs in the region (by tag) that are not
// terminated. The controller sorts them into pools by name prefix.
func (p *Provider) List(ctx context.Context) ([]burst.Instance, error) {
	insts, err := p.describe(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]burst.Instance, 0, len(insts))
	for _, in := range insts {
		name := tagValueOf(in.Tags, tagName)
		if name == "" {
			continue
		}
		var created time.Time
		if in.LaunchTime != nil {
			created = *in.LaunchTime
		}
		st := stateOf(in)
		out = append(out, burst.Instance{
			Name:    name,
			Running: st == ec2types.InstanceStateNamePending || st == ec2types.InstanceStateNameRunning,
			Created: created,
		})
	}
	return out, nil
}

// describe lists managed, non-terminated instances matching extra filters.
func (p *Provider) describe(ctx context.Context, extra []ec2types.Filter) ([]ec2types.Instance, error) {
	filters := append([]ec2types.Filter{
		{Name: awssdk.String("tag:" + tagManagedBy), Values: []string{tagValue}},
		{Name: awssdk.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
	}, extra...)
	in := &ec2.DescribeInstancesInput{Filters: filters}
	var out []ec2types.Instance
	for {
		page, err := p.ec2.DescribeInstances(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Reservations {
			out = append(out, r.Instances...)
		}
		if awssdk.ToString(page.NextToken) == "" {
			return out, nil
		}
		in.NextToken = page.NextToken
	}
}

func stateOf(in ec2types.Instance) ec2types.InstanceStateName {
	if in.State == nil {
		return ""
	}
	return in.State.Name
}

func tagValueOf(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if awssdk.ToString(t.Key) == key {
			return awssdk.ToString(t.Value)
		}
	}
	return ""
}
