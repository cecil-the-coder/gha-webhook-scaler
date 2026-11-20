package aws

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

const (
	testAMI     = "ami-0123456789abcdef0"
	testSSMPath = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
)

// fakeEC2 implements ec2API.
type fakeEC2 struct {
	runs       []*ec2.RunInstancesInput
	runErrs    map[string]error // by subnet
	terminated [][]string
	describes  []*ec2.DescribeInstancesInput
	pages      []*ec2.DescribeInstancesOutput // returned in turn; last one repeats
	describeFn func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	images     int
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.runs = append(f.runs, in)
	if err := f.runErrs[awssdk.ToString(in.NetworkInterfaces[0].SubnetId)]; err != nil {
		return nil, err
	}
	return &ec2.RunInstancesOutput{}, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminated = append(f.terminated, in.InstanceIds)
	return &ec2.TerminateInstancesOutput{}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	cp := *in
	f.describes = append(f.describes, &cp)
	if f.describeFn != nil {
		return f.describeFn(in)
	}
	if len(f.pages) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	i := min(len(f.describes)-1, len(f.pages)-1)
	return f.pages[i], nil
}

func (f *fakeEC2) DescribeImages(_ context.Context, in *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	f.images++
	return &ec2.DescribeImagesOutput{Images: []ec2types.Image{{ImageId: awssdk.String(in.ImageIds[0]), RootDeviceName: awssdk.String("/dev/sda1")}}}, nil
}

type fakeSSM struct{ calls int }

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	if awssdk.ToString(in.Name) != testSSMPath {
		return nil, errors.New("ParameterNotFound")
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: awssdk.String(testAMI)}}, nil
}

func testCfg() config.BurstConfig {
	return config.BurstConfig{
		Provider:      config.BurstProviderAWS,
		PublicURL:     "https://scaler.example.com/v1/runner-config",
		RunnerVersion: "2.330.0",
		InstallDocker: true,
		AWS: config.AWSConfig{
			Region:           "us-east-1",
			SubnetIDs:        []string{"subnet-0000000000000000a", "subnet-0000000000000000b"},
			SecurityGroupIDs: []string{"sg-0123456789abcdef0"},
			InstanceProfile:  "gha-burst-runner",
			InstanceRole:     "gha-burst-runner",
			Spot:             true,
			VMPrefix:         "gha-burst",
		},
	}
}

func testPool() config.BurstPool {
	return config.BurstPool{
		Name: "x64", MachineType: "m7i.xlarge", Image: testSSMPath, Arch: "x64",
		DiskGB: 50, Prefix: "gha-burst-x64-", RunnerEnv: []string{"BUILDX_BUILDER=remote", "FOO=bar baz"},
		Packages: []string{"cmake", "fonts-dejavu-core"},
	}
}

func apiErr(code string) error {
	return &smithy.OperationError{ServiceID: "EC2", OperationName: "RunInstances",
		Err: &smithy.GenericAPIError{Code: code, Message: "test " + code}}
}

func TestCreate_RequestShape(t *testing.T) {
	fe, fs := &fakeEC2{}, &fakeSSM{}
	p := newProvider(testCfg(), fe, fs)
	pool := testPool()
	name := "gha-burst-x64-0a1b2c3d"
	if err := p.Create(context.Background(), pool, name); err != nil {
		t.Fatal(err)
	}
	if len(fe.runs) != 1 {
		t.Fatalf("runs = %d", len(fe.runs))
	}
	in := fe.runs[0]
	if awssdk.ToString(in.ImageId) != testAMI || in.InstanceType != "m7i.xlarge" ||
		awssdk.ToInt32(in.MinCount) != 1 || awssdk.ToInt32(in.MaxCount) != 1 || awssdk.ToString(in.ClientToken) != name {
		t.Errorf("basics: image=%s type=%s token=%s", awssdk.ToString(in.ImageId), in.InstanceType, awssdk.ToString(in.ClientToken))
	}
	if in.InstanceInitiatedShutdownBehavior != ec2types.ShutdownBehaviorTerminate {
		t.Errorf("shutdown behavior = %s", in.InstanceInitiatedShutdownBehavior)
	}
	mo := in.MetadataOptions
	if mo == nil || mo.HttpTokens != ec2types.HttpTokensStateRequired || mo.HttpEndpoint != ec2types.InstanceMetadataEndpointStateEnabled ||
		awssdk.ToInt32(mo.HttpPutResponseHopLimit) != 1 {
		t.Errorf("metadata options = %+v", mo)
	}
	mk := in.InstanceMarketOptions
	if mk == nil || mk.MarketType != ec2types.MarketTypeSpot || mk.SpotOptions.SpotInstanceType != ec2types.SpotInstanceTypeOneTime ||
		mk.SpotOptions.InstanceInterruptionBehavior != ec2types.InstanceInterruptionBehaviorTerminate {
		t.Errorf("market options = %+v", mk)
	}
	if awssdk.ToString(in.IamInstanceProfile.Name) != "gha-burst-runner" || in.IamInstanceProfile.Arn != nil {
		t.Errorf("profile = %+v", in.IamInstanceProfile)
	}
	nic := in.NetworkInterfaces[0]
	if awssdk.ToString(nic.SubnetId) != "subnet-0000000000000000a" || len(nic.Groups) != 1 || nic.AssociatePublicIpAddress != nil ||
		!awssdk.ToBool(nic.DeleteOnTermination) {
		t.Errorf("nic = %+v", nic)
	}
	bd := in.BlockDeviceMappings
	if len(bd) != 1 || awssdk.ToString(bd[0].DeviceName) != "/dev/sda1" || awssdk.ToInt32(bd[0].Ebs.VolumeSize) != 50 ||
		!awssdk.ToBool(bd[0].Ebs.DeleteOnTermination) {
		t.Errorf("block devices = %+v", bd)
	}
	if len(in.TagSpecifications) != 2 {
		t.Fatalf("tag specs = %+v", in.TagSpecifications)
	}
	tags := in.TagSpecifications[0].Tags
	if in.TagSpecifications[0].ResourceType != ec2types.ResourceTypeInstance ||
		tagValueOf(tags, "Name") != name || tagValueOf(tags, "managed-by") != "gha-webhook-scaler" || tagValueOf(tags, "gha-scaler-pool") != "x64" {
		t.Errorf("tags = %+v", tags)
	}

	// User-data: gzipped bootstrap with public settings only.
	script := gunzipUserData(t, awssdk.ToString(in.UserData))
	if !strings.HasPrefix(script, "#!/bin/bash\n") || strings.Contains(script, settingsPlaceholder) {
		t.Fatal("user-data is not the rendered bootstrap")
	}
	s := settingsOf(t, script)
	if s.ConfigURL != "https://scaler.example.com/v1/runner-config" || s.RunnerVersion != "2.330.0" || !s.InstallDocker ||
		strings.Join(s.RunnerEnv, "|") != "BUILDX_BUILDER=remote|FOO=bar baz" ||
		strings.Join(s.Packages, " ") != "cmake fonts-dejavu-core" {
		t.Errorf("settings = %+v", s)
	}

	// The second create starts at the next subnet; the image is cached.
	if err := p.Create(context.Background(), pool, "gha-burst-x64-11111111"); err != nil {
		t.Fatal(err)
	}
	if got := awssdk.ToString(fe.runs[1].NetworkInterfaces[0].SubnetId); got != "subnet-0000000000000000b" {
		t.Errorf("second subnet = %s", got)
	}
	if fs.calls != 1 || fe.images != 1 {
		t.Errorf("image lookups: ssm=%d ec2=%d, want 1 each", fs.calls, fe.images)
	}
}

func TestCreate_OnDemandProfileARNAndPublicIP(t *testing.T) {
	cfg := testCfg()
	cfg.AWS.Spot = false
	cfg.AWS.InstanceProfile = "arn:aws:iam::123456789012:instance-profile/gha-burst-runner"
	on := true
	cfg.AWS.AssociatePublicIP = &on
	fe := &fakeEC2{}
	p := newProvider(cfg, fe, &fakeSSM{})
	pool := testPool()
	pool.Image = testAMI
	if err := p.Create(context.Background(), pool, "gha-burst-x64-0a1b2c3d"); err != nil {
		t.Fatal(err)
	}
	in := fe.runs[0]
	if in.InstanceMarketOptions != nil {
		t.Error("on-demand must not set market options")
	}
	if awssdk.ToString(in.IamInstanceProfile.Arn) != cfg.AWS.InstanceProfile || in.IamInstanceProfile.Name != nil {
		t.Errorf("profile = %+v", in.IamInstanceProfile)
	}
	if !awssdk.ToBool(in.NetworkInterfaces[0].AssociatePublicIpAddress) {
		t.Error("public IP not requested")
	}
	if awssdk.ToString(in.ImageId) != testAMI {
		t.Errorf("image = %s", awssdk.ToString(in.ImageId))
	}
}

func TestCreate_Errors(t *testing.T) {
	a, b := "subnet-0000000000000000a", "subnet-0000000000000000b"
	cases := []struct {
		name     string
		errs     map[string]error
		wantRuns int
		wantCode string
		unknown  bool
		refused  bool
	}{
		{"zonal capacity then ok", map[string]error{a: apiErr("InsufficientInstanceCapacity")}, 2, "", false, false},
		{"zonal capacity everywhere", map[string]error{a: apiErr("InsufficientInstanceCapacity"), b: apiErr("InsufficientInstanceCapacity")}, 2, "InsufficientInstanceCapacity", false, true},
		{"spot price", map[string]error{a: apiErr("SpotMaxPriceTooLow"), b: apiErr("SpotMaxPriceTooLow")}, 2, "SpotMaxPriceTooLow", false, true},
		{"vcpu limit stops at once", map[string]error{a: apiErr("VcpuLimitExceeded")}, 1, "VcpuLimitExceeded", false, true},
		{"spot count", map[string]error{a: apiErr("MaxSpotInstanceCountExceeded")}, 1, "MaxSpotInstanceCountExceeded", false, true},
		{"unsupported then capacity", map[string]error{a: apiErr("Unsupported"), b: apiErr("InsufficientInstanceCapacity")}, 2, "InsufficientInstanceCapacity", false, true},
		{"unsupported everywhere", map[string]error{a: apiErr("Unsupported"), b: apiErr("Unsupported")}, 2, "", false, true},
		{"bad parameter", map[string]error{a: apiErr("InvalidParameterValue")}, 1, "", false, true},
		{"no answer", map[string]error{a: errors.New("dial tcp: i/o timeout")}, 1, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fe := &fakeEC2{runErrs: c.errs}
			p := newProvider(testCfg(), fe, &fakeSSM{})
			err := p.Create(context.Background(), testPool(), "gha-burst-x64-0a1b2c3d")
			if len(fe.runs) != c.wantRuns {
				t.Errorf("runs = %d, want %d", len(fe.runs), c.wantRuns)
			}
			if got := errors.Is(err, burst.ErrOutcomeUnknown); got != c.unknown {
				t.Errorf("unknown = %v (err %v)", got, err)
			}
			if c.refused != (err != nil && !c.unknown) {
				t.Errorf("err = %v", err)
			}
			var ce burst.CapacityError
			code := ""
			if errors.As(err, &ce) {
				code = ce.CapacityCode()
			}
			if code != c.wantCode {
				t.Errorf("capacity code = %q, want %q", code, c.wantCode)
			}
		})
	}
}

func TestList_Mapping(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	inst := func(id, name string, st ec2types.InstanceStateName) ec2types.Instance {
		in := ec2types.Instance{InstanceId: awssdk.String(id), LaunchTime: &t0, State: &ec2types.InstanceState{Name: st}}
		if name != "" {
			in.Tags = []ec2types.Tag{{Key: awssdk.String("Name"), Value: awssdk.String(name)}}
		}
		return in
	}
	fe := &fakeEC2{pages: []*ec2.DescribeInstancesOutput{
		{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{
			inst("i-1", "gha-burst-x64-00000001", ec2types.InstanceStateNamePending),
			inst("i-2", "gha-burst-x64-00000002", ec2types.InstanceStateNameRunning),
		}}}, NextToken: awssdk.String("page2")},
		{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{
			inst("i-3", "gha-burst-x64-00000003", ec2types.InstanceStateNameStopped),
			inst("i-4", "", ec2types.InstanceStateNameRunning), // no Name: skipped
		}}}},
	}}
	p := newProvider(testCfg(), fe, &fakeSSM{})
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	want := []bool{true, true, false}
	for i, g := range got {
		if g.Running != want[i] || !g.Created.Equal(t0) {
			t.Errorf("%d: %+v", i, g)
		}
	}
	if len(fe.describes) != 2 || awssdk.ToString(fe.describes[1].NextToken) != "page2" {
		t.Errorf("pagination: %d calls", len(fe.describes))
	}
	f := fe.describes[0].Filters
	if awssdk.ToString(f[0].Name) != "tag:managed-by" || f[0].Values[0] != "gha-webhook-scaler" ||
		awssdk.ToString(f[1].Name) != "instance-state-name" || strings.Contains(strings.Join(f[1].Values, ","), "terminated") {
		t.Errorf("filters = %+v", f)
	}
}

func TestDelete_ByNameTag(t *testing.T) {
	fe := &fakeEC2{describeFn: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		for _, f := range in.Filters {
			if awssdk.ToString(f.Name) == "tag:Name" && f.Values[0] == "gha-burst-x64-0a1b2c3d" {
				return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{
					{InstanceId: awssdk.String("i-0123456789abcdef0")}}}}}, nil
			}
		}
		return &ec2.DescribeInstancesOutput{}, nil
	}}
	p := newProvider(testCfg(), fe, &fakeSSM{})
	if err := p.Delete(context.Background(), "gha-burst-x64-0a1b2c3d"); err != nil {
		t.Fatal(err)
	}
	if len(fe.terminated) != 1 || fe.terminated[0][0] != "i-0123456789abcdef0" {
		t.Errorf("terminated = %v", fe.terminated)
	}
	if err := p.Delete(context.Background(), "gha-burst-x64-ffffffff"); err != nil || len(fe.terminated) != 1 {
		t.Errorf("deleting a gone VM: err=%v terminated=%v", err, fe.terminated)
	}
	managed := false
	for _, f := range fe.describes[0].Filters {
		managed = managed || awssdk.ToString(f.Name) == "tag:managed-by"
	}
	if !managed {
		t.Error("delete must only look at managed instances")
	}
}

func TestUserData_FitsAndHasOnePlaceholder(t *testing.T) {
	if strings.Count(BootstrapScript(), settingsPlaceholder) != 1 {
		t.Fatal("bootstrap needs exactly one settings placeholder")
	}
	p := newProvider(testCfg(), &fakeEC2{}, &fakeSSM{})
	ud, err := p.userData(testPool())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(ud)
	if len(raw) > maxUserData {
		t.Errorf("user-data %d bytes", len(raw))
	}
}

func gunzipUserData(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func settingsOf(t *testing.T, script string) vmSettings {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		if v, ok := strings.CutPrefix(line, "SETTINGS_B64=\""); ok {
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(v, "\""))
			if err != nil {
				t.Fatal(err)
			}
			var s vmSettings
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			return s
		}
	}
	t.Fatal("no SETTINGS_B64 line")
	return vmSettings{}
}
