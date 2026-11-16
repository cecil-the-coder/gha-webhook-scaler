// Package burst starts ephemeral cloud VMs as single-job GitHub Actions
// runners for jobs the primary runtime (Docker/Kubernetes) has not picked up.
//
// Each VM pulls its own single-use JIT runner config from the scaler over
// HTTPS (POST /v1/runner-config), authenticated by its cloud instance identity
// token. No GitHub token is ever put on a VM.
package burst

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/github"
)

// Instance is one burst VM as the provider reports it.
type Instance struct {
	Name    string
	Running bool // provisioning, staging or running
	Created time.Time
}

// Provider creates, deletes and lists burst VMs.
//
// Create returns once the outcome is known or its wait ran out:
//   - nil: the VM exists.
//   - an error wrapping ErrOutcomeUnknown: accepted, but not confirmed in time.
//     The VM may still appear.
//   - an error implementing CapacityError with a non-empty code: no capacity
//     (quota or zone exhaustion). The VM does not exist.
//   - any other error: refused. The VM does not exist.
type Provider interface {
	Create(ctx context.Context, pool config.BurstPool, name string) error
	Delete(ctx context.Context, name string) error
	List(ctx context.Context) ([]Instance, error)
}

// ErrOutcomeUnknown marks a create whose result is not known yet.
var ErrOutcomeUnknown = errors.New("create outcome unknown")

// CapacityError is implemented by create errors that mean no VM can be had
// right now in any pool. CapacityCode returns "" for other failures.
type CapacityError interface {
	error
	CapacityCode() string
}

func capacityCode(err error) string {
	var ce CapacityError
	if errors.As(err, &ce) {
		return ce.CapacityCode()
	}
	return ""
}

// Identity is what a verified instance identity token says about its VM.
type Identity struct {
	InstanceName string
}

// IdentityVerifier checks a VM's identity token. The error never contains the token.
type IdentityVerifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}

// Forge is what the controller needs from GitHub. Tests use a fake.
type Forge interface {
	OwnerType(ctx context.Context, owner string) (github.OwnerType, error)
	WaitingJobs(ctx context.Context, owner, repo string) ([]github.WaitingJob, error)
	Runners(ctx context.Context, scope github.RunnerScope) ([]github.Runner, error)
	MintJIT(ctx context.Context, scope github.RunnerScope, name string, labels []string, groupID int64) (string, error)
}

// GitHubForge implements Forge over the per-owner GitHub clients.
type GitHubForge struct {
	Clients map[string]*github.Client
}

func (f GitHubForge) client(owner string) (*github.Client, error) {
	c := f.Clients[owner]
	if c == nil {
		return nil, fmt.Errorf("no GitHub client for owner %s", owner)
	}
	return c, nil
}

func (f GitHubForge) OwnerType(ctx context.Context, owner string) (github.OwnerType, error) {
	c, err := f.client(owner)
	if err != nil {
		return "", err
	}
	return c.GetOwnerType(ctx, owner)
}

func (f GitHubForge) WaitingJobs(ctx context.Context, owner, repo string) ([]github.WaitingJob, error) {
	c, err := f.client(owner)
	if err != nil {
		return nil, err
	}
	return c.ListWaitingJobs(ctx, owner, repo)
}

func (f GitHubForge) Runners(ctx context.Context, scope github.RunnerScope) ([]github.Runner, error) {
	c, err := f.client(scope.Owner)
	if err != nil {
		return nil, err
	}
	return c.ListRunners(ctx, scope)
}

func (f GitHubForge) MintJIT(ctx context.Context, scope github.RunnerScope, name string, labels []string, groupID int64) (string, error) {
	c, err := f.client(scope.Owner)
	if err != nil {
		return "", err
	}
	return c.GenerateJITConfig(ctx, scope, name, labels, groupID)
}
