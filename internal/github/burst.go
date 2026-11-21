package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// This file holds the GitHub calls used by the burst controller
// (internal/burst). They take a context, read the API base URL from
// Client.BaseURL (tests point it at an httptest server) and never put the
// token anywhere but the Authorization header.

const defaultAPIBase = "https://api.github.com"

// maxRunsPerStatus bounds API cost: one jobs request per listed run, per poll.
// A backlog spread over more runs than this is under-counted.
const maxRunsPerStatus = 10

// WaitingJob is one workflow job waiting for a runner.
type WaitingJob struct {
	ID        int64
	RunID     int64
	Repo      string // owner/name
	Labels    []string
	CreatedAt time.Time
}

// Runner is one self-hosted runner as GitHub lists it.
type Runner struct {
	ID     int64
	Name   string
	Busy   bool
	Status string // online / offline
}

// OwnerType is "Organization" or "User".
type OwnerType string

const (
	OwnerOrganization OwnerType = "Organization"
	OwnerUser         OwnerType = "User"
)

// RunnerScope says where runners are registered: an organization, or a single
// repository (user accounts have no org-level runners).
type RunnerScope struct {
	Owner string
	Repo  string // empty = organization scope
}

func (s RunnerScope) path() string {
	if s.Repo == "" {
		return "/orgs/" + url.PathEscape(s.Owner) + "/actions/runners"
	}
	return "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + "/actions/runners"
}

// APIError is a non-2xx answer. It carries method, path and status only, so
// it is safe to log.
type APIError struct {
	Method, Path string
	Status       int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub %s %s: HTTP %d", e.Method, e.Path, e.Status)
}

// IsNotFound reports whether err is a GitHub 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func (g *Client) apiBase() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return defaultAPIBase
}

// callJSON sends an optional JSON body and decodes a JSON answer into out
// (out may be nil). Rate-limit handling is shared with the rest of the client.
func (g *Client) callJSON(ctx context.Context, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.apiBase()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.MakeRequestWithBackoff(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return &APIError{Method: method, Path: path, Status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// ListWaitingJobs returns the jobs in owner/repo that wait for a runner.
//
// A waiting job has status "queued" or "pending" (GitHub uses "pending" for
// some waiting jobs, e.g. in called workflows, even across cancel + rerun).
// Its run can be "queued", "pending" or already "in_progress", so all three
// run statuses are read. "waiting" (environment approval) is not runner
// backlog and is skipped.
func (g *Client) ListWaitingJobs(ctx context.Context, owner, repo string) ([]WaitingJob, error) {
	full := owner + "/" + repo
	seen := map[int64]bool{}
	var out []WaitingJob
	for _, status := range []string{"queued", "pending", "in_progress"} {
		var runs struct {
			Runs []struct {
				ID int64 `json:"id"`
			} `json:"workflow_runs"`
		}
		path := fmt.Sprintf("/repos/%s/%s/actions/runs?status=%s&per_page=%d",
			url.PathEscape(owner), url.PathEscape(repo), status, maxRunsPerStatus)
		if err := g.callJSON(ctx, http.MethodGet, path, nil, &runs); err != nil {
			return nil, err
		}
		for _, r := range runs.Runs {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			var jobs struct {
				Jobs []struct {
					ID        int64     `json:"id"`
					Status    string    `json:"status"`
					Labels    []string  `json:"labels"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"jobs"`
			}
			path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=100",
				url.PathEscape(owner), url.PathEscape(repo), r.ID)
			if err := g.callJSON(ctx, http.MethodGet, path, nil, &jobs); err != nil {
				return nil, err
			}
			for _, j := range jobs.Jobs {
				if j.Status == "queued" || j.Status == "pending" {
					out = append(out, WaitingJob{ID: j.ID, RunID: r.ID, Repo: full, Labels: j.Labels, CreatedAt: j.CreatedAt})
				}
			}
		}
	}
	return out, nil
}

// ListRunners returns every self-hosted runner in scope, following pagination.
func (g *Client) ListRunners(ctx context.Context, scope RunnerScope) ([]Runner, error) {
	var all []Runner
	for page := 1; page <= 50; page++ {
		var resp struct {
			Total   int `json:"total_count"`
			Runners []struct {
				ID     int64  `json:"id"`
				Name   string `json:"name"`
				Busy   bool   `json:"busy"`
				Status string `json:"status"`
			} `json:"runners"`
		}
		path := fmt.Sprintf("%s?per_page=100&page=%d", scope.path(), page)
		if err := g.callJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Runners {
			all = append(all, Runner{ID: r.ID, Name: r.Name, Busy: r.Busy, Status: r.Status})
		}
		if len(resp.Runners) < 100 || len(all) >= resp.Total {
			break
		}
	}
	return all, nil
}

var jitShape = regexp.MustCompile(`^[A-Za-z0-9+/=]{16,}$`)

// GenerateJITConfig mints a single-use, ephemeral runner config named name.
//
// labels REPLACE GitHub's defaults, so the caller must include self-hosted,
// Linux and the architecture itself. runnerGroupID is required by GitHub; use
// 1 (the default group) for repository scope.
func (g *Client) GenerateJITConfig(ctx context.Context, scope RunnerScope, name string, labels []string, runnerGroupID int64) (string, error) {
	if runnerGroupID < 1 {
		runnerGroupID = 1
	}
	in := map[string]any{"name": name, "runner_group_id": runnerGroupID, "labels": labels, "work_folder": "_work"}
	var out struct {
		EncodedJITConfig string `json:"encoded_jit_config"`
	}
	if err := g.callJSON(ctx, http.MethodPost, scope.path()+"/generate-jitconfig", in, &out); err != nil {
		return "", err
	}
	if !jitShape.MatchString(out.EncodedJITConfig) {
		return "", fmt.Errorf("GitHub returned a JIT config of unexpected shape")
	}
	return out.EncodedJITConfig, nil
}

// DeleteRunner removes a runner registration (used for runners whose VM is gone).
func (g *Client) DeleteRunner(ctx context.Context, scope RunnerScope, id int64) error {
	return g.callJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", scope.path(), id), nil, nil)
}

// GetOwnerType reports whether owner is an organization or a user account.
func (g *Client) GetOwnerType(ctx context.Context, owner string) (OwnerType, error) {
	var out struct {
		Type string `json:"type"`
	}
	if err := g.callJSON(ctx, http.MethodGet, "/users/"+url.PathEscape(owner), nil, &out); err != nil {
		return "", err
	}
	switch OwnerType(out.Type) {
	case OwnerOrganization, OwnerUser:
		return OwnerType(out.Type), nil
	}
	return "", fmt.Errorf("owner %s has unexpected type %q", owner, out.Type)
}
