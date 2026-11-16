package gce

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
)

// Verifier checks GCE instance identity tokens (requested with format=full).
// The token proves which VM is calling: Google signs it, and its
// google.compute_engine claims name the project, zone and instance.
type Verifier struct {
	Audience string // the pull URL; the VM requests its token for this audience
	Project  string
	Zone     string

	validate func(ctx context.Context, token, audience string) (*idtoken.Payload, error)
	now      func() time.Time
}

// NewVerifier returns a verifier backed by Google's published signing keys.
func NewVerifier(audience, project, zone string) *Verifier {
	return &Verifier{Audience: audience, Project: project, Zone: zone, validate: idtoken.Validate, now: time.Now}
}

var errIdentity = errors.New("identity token rejected")

// Verify checks signature, audience, expiry, issuer and the compute claims.
// Errors name the failed check only, never the token.
func (v *Verifier) Verify(ctx context.Context, token string) (burst.Identity, error) {
	if v.Audience == "" {
		return burst.Identity{}, fmt.Errorf("%w: no audience configured", errIdentity)
	}
	p, err := v.validate(ctx, token, v.Audience)
	if err != nil {
		return burst.Identity{}, fmt.Errorf("%w: signature, audience or expiry", errIdentity)
	}
	if p.Issuer != "accounts.google.com" && p.Issuer != "https://accounts.google.com" {
		return burst.Identity{}, fmt.Errorf("%w: issuer", errIdentity)
	}
	if p.IssuedAt > v.now().Add(time.Minute).Unix() {
		return burst.Identity{}, fmt.Errorf("%w: issued in the future", errIdentity)
	}
	g, _ := p.Claims["google"].(map[string]any)
	ce, _ := g["compute_engine"].(map[string]any)
	project, _ := ce["project_id"].(string)
	zone, _ := ce["zone"].(string)
	name, _ := ce["instance_name"].(string)
	switch {
	case ce == nil:
		return burst.Identity{}, fmt.Errorf("%w: no compute_engine claims (token must use format=full)", errIdentity)
	case project != v.Project:
		return burst.Identity{}, fmt.Errorf("%w: project", errIdentity)
	case zone != v.Zone:
		return burst.Identity{}, fmt.Errorf("%w: zone", errIdentity)
	case name == "":
		return burst.Identity{}, fmt.Errorf("%w: instance name", errIdentity)
	}
	return burst.Identity{InstanceName: name}, nil
}
