// Package gce is the Compute Engine provider for burst runners. It uses the
// official Google Cloud SDK and Application Default Credentials.
package gce

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

//go:embed bootstrap/runner-bootstrap.sh
var bootstrapScript string

// BootstrapScript returns the startup script every burst VM runs.
func BootstrapScript() string { return bootstrapScript }

const (
	// opWaitTimeout bounds the wait for an insert operation. Inserts usually
	// finish in 1-10 s; past this the outcome is unknown.
	opWaitTimeout = 30 * time.Second

	labelManagedBy = "managed-by"
	labelValue     = "gha-webhook-scaler"
	labelPool      = "gha-scaler-pool"
)

// capacityCodes mean no VM can be had right now in any pool.
var capacityCodes = []string{"QUOTA_EXCEEDED", "ZONE_RESOURCE_POOL_EXHAUSTED", "ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS"}

// OpError is an insert that was accepted and whose operation then failed.
type OpError struct {
	Op    string
	Codes []string
	Msgs  []string
}

func (e *OpError) Error() string {
	parts := make([]string, len(e.Codes))
	for i := range e.Codes {
		parts[i] = e.Codes[i] + ": " + e.Msgs[i]
	}
	return fmt.Sprintf("operation %s failed: %s", e.Op, strings.Join(parts, "; "))
}

// CapacityCode implements burst.CapacityError.
func (e *OpError) CapacityCode() string {
	for _, c := range e.Codes {
		if slices.Contains(capacityCodes, c) {
			return c
		}
	}
	return ""
}

// insertError is an insert refused outright. Quota can show up here too.
type insertError struct{ err error }

func (e *insertError) Error() string        { return "insert refused: " + e.err.Error() }
func (e *insertError) Unwrap() error        { return e.err }
func (e *insertError) CapacityCode() string { return capacityCodeIn(e.err.Error()) }

// operation is the part of an SDK operation the provider uses.
type operation interface {
	Wait(ctx context.Context) error
	Proto() *computepb.Operation
}

// computeAPI is the slice of the Compute SDK the provider calls. Tests fake it.
type computeAPI interface {
	Insert(ctx context.Context, req *computepb.InsertInstanceRequest) (operation, error)
	Delete(ctx context.Context, req *computepb.DeleteInstanceRequest) error
	List(ctx context.Context, req *computepb.ListInstancesRequest) ([]*computepb.Instance, error)
	Close() error
}

// Provider implements burst.Provider on Compute Engine.
type Provider struct {
	cfg    config.BurstConfig
	api    computeAPI
	opWait time.Duration
}

// New connects to Compute Engine with Application Default Credentials.
func New(ctx context.Context, cfg config.BurstConfig) (*Provider, error) {
	c, err := compute.NewInstancesRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("compute client (check Application Default Credentials): %w", err)
	}
	return &Provider{cfg: cfg, api: sdkAPI{c}, opWait: opWaitTimeout}, nil
}

// Close releases the SDK client.
func (p *Provider) Close() error { return p.api.Close() }

// Create asks for one VM of the pool's machine type and image. Its metadata
// holds only PUBLIC items: the bootstrap script, the URL it pulls its runner
// config from (also the identity-token audience), the runner version, the
// Docker switch and the pool's non-secret runner env. No token, key or JIT
// config is ever put in metadata.
func (p *Provider) Create(ctx context.Context, pool config.BurstPool, name string) error {
	op, err := p.api.Insert(ctx, &computepb.InsertInstanceRequest{
		Project:          p.cfg.GCE.Project,
		Zone:             p.cfg.GCE.Zone,
		InstanceResource: p.instance(pool, name),
	})
	if err != nil {
		return &insertError{err}
	}
	wctx, cancel := context.WithTimeout(ctx, p.opWait)
	defer cancel()
	waitErr := op.Wait(wctx)
	// The SDK's Wait returns an error when the operation finished FAILED, not
	// only when the wait itself ran out. Telling them apart matters: a failed
	// operation (quota, zone exhaustion) means the VM does not exist and the
	// controller must back off, while a timeout means it may still appear.
	if errs := op.Proto().GetError().GetErrors(); len(errs) > 0 {
		oe := &OpError{Op: op.Proto().GetName()}
		for _, e := range errs {
			oe.Codes = append(oe.Codes, e.GetCode())
			oe.Msgs = append(oe.Msgs, e.GetMessage())
		}
		return oe
	}
	if waitErr != nil {
		// No error detail on the operation; fall back to the error text.
		if code := capacityCodeIn(waitErr.Error()); code != "" {
			return &waitCapacityError{op: op.Proto().GetName(), err: waitErr, code: code}
		}
		return fmt.Errorf("%w: operation %s: %v", burst.ErrOutcomeUnknown, op.Proto().GetName(), waitErr)
	}
	return nil
}

// capacityCodeIn returns the capacity code named in msg, or "".
func capacityCodeIn(msg string) string {
	for _, c := range capacityCodes {
		if strings.Contains(msg, c) {
			return c
		}
	}
	if strings.Contains(msg, "quotaExceeded") {
		return "QUOTA_EXCEEDED"
	}
	return ""
}

// waitCapacityError is a Wait failure whose text names a capacity code.
type waitCapacityError struct {
	op   string
	err  error
	code string
}

func (e *waitCapacityError) Error() string {
	return fmt.Sprintf("operation %s failed: %v", e.op, e.err)
}
func (e *waitCapacityError) Unwrap() error        { return e.err }
func (e *waitCapacityError) CapacityCode() string { return e.code }

func (p *Provider) instance(pool config.BurstPool, name string) *computepb.Instance {
	g := p.cfg.GCE
	sa := g.VMServiceAccount
	if sa == "" {
		sa = "default"
	}
	nic := &computepb.NetworkInterface{Network: proto.String(qualify(g.Network, "global/networks/"))}
	if g.Subnetwork != "" {
		nic.Subnetwork = proto.String(qualify(g.Subnetwork, "regions/"+region(g.Zone)+"/subnetworks/"))
	}
	if g.ExternalIP {
		nic.AccessConfigs = []*computepb.AccessConfig{{
			Name: proto.String("External NAT"), Type: proto.String("ONE_TO_ONE_NAT"),
		}}
	}
	sched := &computepb.Scheduling{AutomaticRestart: proto.Bool(false), OnHostMaintenance: proto.String("TERMINATE")}
	if g.Spot {
		sched.ProvisioningModel = proto.String("SPOT")
		sched.InstanceTerminationAction = proto.String("DELETE")
	}
	// Never set MinCpuPlatform: GCE rejects it for E2 and T2A machine types.
	return &computepb.Instance{
		Name:        proto.String(name),
		MachineType: proto.String(fmt.Sprintf("zones/%s/machineTypes/%s", g.Zone, pool.MachineType)),
		Disks: []*computepb.AttachedDisk{{
			Boot: proto.Bool(true), AutoDelete: proto.Bool(true),
			InitializeParams: diskParams(pool),
		}},
		NetworkInterfaces: []*computepb.NetworkInterface{nic},
		ServiceAccounts: []*computepb.ServiceAccount{{
			Email:  proto.String(sa),
			Scopes: []string{"https://www.googleapis.com/auth/logging.write"},
		}},
		Scheduling: sched,
		Labels:     map[string]string{labelManagedBy: labelValue, labelPool: pool.Name},
		Metadata:   &computepb.Metadata{Items: p.metadata(pool)},
	}
}

func (p *Provider) metadata(pool config.BurstPool) []*computepb.Items {
	item := func(k, v string) *computepb.Items {
		return &computepb.Items{Key: proto.String(k), Value: proto.String(v)}
	}
	items := []*computepb.Items{
		item("startup-script", bootstrapScript),
		item("runner-config-url", p.cfg.PublicURL),
		item("runner-version", p.cfg.RunnerVersion),
		item("install-docker", strconv.FormatBool(p.cfg.InstallDocker)),
		item("runner-dir", pool.RunnerDir),
		item("runner-user", pool.RunnerUser),
	}
	if len(pool.RunnerEnv) > 0 {
		items = append(items, item("runner-env", strings.Join(pool.RunnerEnv, "\n")))
	}
	if len(pool.Packages) > 0 {
		items = append(items, item("packages", strings.Join(pool.Packages, " ")))
	}
	return items
}

// Delete removes a VM. It returns once GCE accepts the request.
func (p *Provider) Delete(ctx context.Context, name string) error {
	return p.api.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project: p.cfg.GCE.Project, Zone: p.cfg.GCE.Zone, Instance: name,
	})
}

// List returns this scaler's VMs in the zone (by label). The controller sorts
// them into pools by name prefix.
func (p *Provider) List(ctx context.Context) ([]burst.Instance, error) {
	items, err := p.api.List(ctx, &computepb.ListInstancesRequest{
		Project: p.cfg.GCE.Project, Zone: p.cfg.GCE.Zone,
		Filter: proto.String(fmt.Sprintf("labels.%s=%s", labelManagedBy, labelValue)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]burst.Instance, 0, len(items))
	for _, it := range items {
		created, err := time.Parse(time.RFC3339, it.GetCreationTimestamp())
		if err != nil {
			return nil, fmt.Errorf("instance %s: bad creationTimestamp %q", it.GetName(), it.GetCreationTimestamp())
		}
		st := it.GetStatus()
		out = append(out, burst.Instance{
			Name:    it.GetName(),
			Running: st == "PROVISIONING" || st == "STAGING" || st == "RUNNING",
			Created: created,
		})
	}
	return out, nil
}

func diskParams(pool config.BurstPool) *computepb.AttachedDiskInitializeParams {
	d := &computepb.AttachedDiskInitializeParams{SourceImage: proto.String(qualify(pool.Image, "global/images/"))}
	if pool.DiskGB > 0 {
		d.DiskSizeGb = proto.Int64(int64(pool.DiskGB))
	}
	return d
}

// qualify returns v as-is when it is already a path or URL, else prefix+v.
func qualify(v, prefix string) string {
	if strings.Contains(v, "/") {
		return v
	}
	return prefix + v
}

// region returns the region of a zone ("us-central1-a" -> "us-central1").
func region(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

// sdkAPI adapts the SDK client to computeAPI.
type sdkAPI struct{ c *compute.InstancesClient }

type sdkOp struct{ op *compute.Operation }

func (o sdkOp) Wait(ctx context.Context) error { return o.op.Wait(ctx) }
func (o sdkOp) Proto() *computepb.Operation    { return o.op.Proto() }
func (s sdkAPI) Close() error                  { return s.c.Close() }

func (s sdkAPI) Insert(ctx context.Context, req *computepb.InsertInstanceRequest) (operation, error) {
	op, err := s.c.Insert(ctx, req)
	if err != nil {
		return nil, err
	}
	return sdkOp{op}, nil
}

func (s sdkAPI) Delete(ctx context.Context, req *computepb.DeleteInstanceRequest) error {
	_, err := s.c.Delete(ctx, req)
	return err
}

func (s sdkAPI) List(ctx context.Context, req *computepb.ListInstancesRequest) ([]*computepb.Instance, error) {
	var out []*computepb.Instance
	it := s.c.List(ctx, req)
	for {
		inst, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
}
