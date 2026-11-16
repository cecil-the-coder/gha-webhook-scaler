package gce

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/idtoken"
	"google.golang.org/protobuf/proto"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

type fakeOp struct {
	waitErr error
	pb      *computepb.Operation
}

func (o fakeOp) Wait(context.Context) error  { return o.waitErr }
func (o fakeOp) Proto() *computepb.Operation { return o.pb }

type fakeAPI struct {
	insertReq *computepb.InsertInstanceRequest
	insertErr error
	op        fakeOp
	listReq   *computepb.ListInstancesRequest
	items     []*computepb.Instance
	deleted   string
}

func (f *fakeAPI) Insert(_ context.Context, r *computepb.InsertInstanceRequest) (operation, error) {
	f.insertReq = r
	if f.insertErr != nil {
		return nil, f.insertErr
	}
	return f.op, nil
}
func (f *fakeAPI) Delete(_ context.Context, r *computepb.DeleteInstanceRequest) error {
	f.deleted = r.GetInstance()
	return nil
}
func (f *fakeAPI) List(_ context.Context, r *computepb.ListInstancesRequest) ([]*computepb.Instance, error) {
	f.listReq = r
	return f.items, nil
}
func (f *fakeAPI) Close() error { return nil }

func testProvider(api *fakeAPI) *Provider {
	return &Provider{api: api, opWait: time.Second, cfg: config.BurstConfig{
		PublicURL: "https://scaler.example.com/v1/runner-config", RunnerVersion: "latest", InstallDocker: true,
		GCE: config.GCEConfig{Project: "proj", Zone: "europe-west4-b", Network: "default",
			Subnetwork: "runners", Spot: true, ExternalIP: true},
	}}
}

var pool = config.BurstPool{Name: "arm", MachineType: "t2a-standard-4",
	Image: "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-arm64", Arch: "arm64",
	DiskGB: 40, Prefix: "gha-burst-arm-", RunnerEnv: []string{"A=1", "B=two"}}

func TestCreate_RequestShape(t *testing.T) {
	api := &fakeAPI{op: fakeOp{pb: &computepb.Operation{Name: proto.String("op-1")}}}
	if err := testProvider(api).Create(context.Background(), pool, "gha-burst-arm-0a0b0c0d"); err != nil {
		t.Fatal(err)
	}
	r := api.insertReq
	if r.GetProject() != "proj" || r.GetZone() != "europe-west4-b" {
		t.Errorf("project/zone = %s/%s", r.GetProject(), r.GetZone())
	}
	in := r.GetInstanceResource()
	if in.GetMachineType() != "zones/europe-west4-b/machineTypes/t2a-standard-4" {
		t.Errorf("machine type = %s", in.GetMachineType())
	}
	if in.MinCpuPlatform != nil {
		t.Error("minCpuPlatform must never be set (rejected for E2/T2A)")
	}
	s := in.GetScheduling()
	if s.GetProvisioningModel() != "SPOT" || s.GetInstanceTerminationAction() != "DELETE" || s.GetAutomaticRestart() {
		t.Errorf("scheduling = %v", s)
	}
	d := in.GetDisks()[0].GetInitializeParams()
	if d.GetSourceImage() != pool.Image || d.GetDiskSizeGb() != 40 {
		t.Errorf("disk = %v", d)
	}
	nic := in.GetNetworkInterfaces()[0]
	if nic.GetNetwork() != "global/networks/default" || nic.GetSubnetwork() != "regions/europe-west4/subnetworks/runners" ||
		len(nic.GetAccessConfigs()) != 1 {
		t.Errorf("nic = %v", nic)
	}
	if sa := in.GetServiceAccounts()[0]; sa.GetEmail() != "default" || len(sa.GetScopes()) != 1 ||
		!strings.HasSuffix(sa.GetScopes()[0], "logging.write") {
		t.Errorf("service account = %v", sa)
	}
	if in.GetLabels()["managed-by"] != "gha-webhook-scaler" || in.GetLabels()["gha-scaler-pool"] != "arm" {
		t.Errorf("labels = %v", in.GetLabels())
	}
	meta := map[string]string{}
	for _, it := range in.GetMetadata().GetItems() {
		meta[it.GetKey()] = it.GetValue()
	}
	want := map[string]string{"runner-config-url": "https://scaler.example.com/v1/runner-config",
		"runner-version": "latest", "install-docker": "true", "runner-env": "A=1\nB=two"}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("metadata %s = %q, want %q", k, meta[k], v)
		}
	}
	if !strings.HasPrefix(meta["startup-script"], "#!/bin/bash") || len(meta) != 5 {
		t.Errorf("metadata keys = %d, startup-script ok = %v", len(meta), strings.HasPrefix(meta["startup-script"], "#!"))
	}
}

func TestCreate_NoRunnerEnvNoItem(t *testing.T) {
	api := &fakeAPI{op: fakeOp{pb: &computepb.Operation{}}}
	p := pool
	p.RunnerEnv = nil
	testProvider(api).Create(context.Background(), p, "n")
	for _, it := range api.insertReq.GetInstanceResource().GetMetadata().GetItems() {
		if it.GetKey() == "runner-env" {
			t.Error("runner-env item must be absent when empty")
		}
	}
}

func TestCreate_Outcomes(t *testing.T) {
	opErr := func(codes ...string) *computepb.Operation {
		var es []*computepb.Errors
		for _, c := range codes {
			es = append(es, &computepb.Errors{Code: proto.String(c), Message: proto.String("m")})
		}
		return &computepb.Operation{Name: proto.String("op-9"), Error: &computepb.Error{Errors: es}}
	}
	cases := []struct {
		name     string
		api      *fakeAPI
		unknown  bool
		capacity string
		ok       bool
	}{
		{"done", &fakeAPI{op: fakeOp{pb: &computepb.Operation{}}}, false, "", true},
		{"wait timeout", &fakeAPI{op: fakeOp{waitErr: context.DeadlineExceeded, pb: &computepb.Operation{}}}, true, "", false},
		{"quota in op", &fakeAPI{op: fakeOp{pb: opErr("QUOTA_EXCEEDED")}}, false, "QUOTA_EXCEEDED", false},
		{"zone exhausted", &fakeAPI{op: fakeOp{pb: opErr("OTHER", "ZONE_RESOURCE_POOL_EXHAUSTED")}}, false, "ZONE_RESOURCE_POOL_EXHAUSTED", false},
		{"other op error", &fakeAPI{op: fakeOp{pb: opErr("INVALID")}}, false, "", false},
		{"insert quota", &fakeAPI{insertErr: errors.New("googleapi: Error 403: quotaExceeded")}, false, "QUOTA_EXCEEDED", false},
		{"insert refused", &fakeAPI{insertErr: errors.New("googleapi: Error 400: bad image")}, false, "", false},
	}
	for _, c := range cases {
		err := testProvider(c.api).Create(context.Background(), pool, "n")
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if got := errors.Is(err, burst.ErrOutcomeUnknown); got != c.unknown {
			t.Errorf("%s: unknown = %v", c.name, got)
		}
		var ce burst.CapacityError
		code := ""
		if errors.As(err, &ce) {
			code = ce.CapacityCode()
		}
		if code != c.capacity {
			t.Errorf("%s: capacity code = %q, want %q", c.name, code, c.capacity)
		}
	}
}

func TestListAndDelete(t *testing.T) {
	api := &fakeAPI{items: []*computepb.Instance{
		{Name: proto.String("a"), Status: proto.String("RUNNING"), CreationTimestamp: proto.String("2026-01-02T03:04:05.000-07:00")},
		{Name: proto.String("b"), Status: proto.String("TERMINATED"), CreationTimestamp: proto.String("2026-01-02T03:04:05Z")},
	}}
	p := testProvider(api)
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if api.listReq.GetFilter() != "labels.managed-by=gha-webhook-scaler" {
		t.Errorf("filter = %s", api.listReq.GetFilter())
	}
	if len(got) != 2 || !got[0].Running || got[1].Running || got[0].Created.IsZero() {
		t.Errorf("list = %+v", got)
	}
	p.Delete(context.Background(), "a")
	if api.deleted != "a" {
		t.Errorf("deleted = %s", api.deleted)
	}
}

func TestVerifier(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	payload := func(iss string, iat int64, ce map[string]any) *idtoken.Payload {
		claims := map[string]any{}
		if ce != nil {
			claims["google"] = map[string]any{"compute_engine": ce}
		}
		return &idtoken.Payload{Issuer: iss, IssuedAt: iat, Claims: claims}
	}
	good := map[string]any{"project_id": "proj", "zone": "europe-west4-b", "instance_name": "gha-burst-arm-1"}
	cases := []struct {
		name    string
		p       *idtoken.Payload
		valErr  error
		wantErr bool
	}{
		{"ok", payload("https://accounts.google.com", now.Unix(), good), nil, false},
		{"ok bare issuer", payload("accounts.google.com", now.Unix(), good), nil, false},
		{"bad signature", nil, errors.New("sig"), true},
		{"issuer", payload("https://evil.example.com", now.Unix(), good), nil, true},
		{"future iat", payload("accounts.google.com", now.Add(5*time.Minute).Unix(), good), nil, true},
		{"no compute claims", payload("accounts.google.com", now.Unix(), nil), nil, true},
		{"other project", payload("accounts.google.com", now.Unix(), map[string]any{"project_id": "x", "zone": "europe-west4-b", "instance_name": "n"}), nil, true},
		{"other zone", payload("accounts.google.com", now.Unix(), map[string]any{"project_id": "proj", "zone": "us-east1-b", "instance_name": "n"}), nil, true},
	}
	for _, c := range cases {
		var gotAud string
		v := &Verifier{Audience: "https://scaler.example.com/v1/runner-config", Project: "proj", Zone: "europe-west4-b",
			now: func() time.Time { return now },
			validate: func(_ context.Context, tok, aud string) (*idtoken.Payload, error) {
				gotAud = aud
				return c.p, c.valErr
			}}
		id, err := v.Verify(context.Background(), "secret-token-value")
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-token-value") {
			t.Errorf("%s: error leaks the token", c.name)
		}
		if err == nil && id.InstanceName != "gha-burst-arm-1" {
			t.Errorf("%s: name = %q", c.name, id.InstanceName)
		}
		if gotAud != "" && gotAud != v.Audience {
			t.Errorf("%s: audience passed = %q", c.name, gotAud)
		}
	}
}
