package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newBurstTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient("test-token")
	c.BaseURL = srv.URL
	return c
}

func TestListWaitingJobs_StatusesAndDedup(t *testing.T) {
	var runCalls []string
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q", got)
		}
		switch {
		case r.URL.Path == "/repos/acme/app/actions/runs":
			st := r.URL.Query().Get("status")
			runCalls = append(runCalls, st)
			switch st {
			case "queued":
				fmt.Fprint(w, `{"workflow_runs":[{"id":1}]}`)
			case "pending":
				fmt.Fprint(w, `{"workflow_runs":[{"id":2},{"id":1}]}`) // 1 again: dedup
			default:
				fmt.Fprint(w, `{"workflow_runs":[{"id":3}]}`)
			}
		case r.URL.Path == "/repos/acme/app/actions/runs/1/jobs":
			if r.URL.Query().Get("filter") != "latest" {
				t.Errorf("missing filter=latest")
			}
			fmt.Fprint(w, `{"jobs":[{"id":10,"status":"queued","labels":["self-hosted","x"],"created_at":"2026-01-01T00:00:00Z"},{"id":11,"status":"completed","labels":["self-hosted"]}]}`)
		case r.URL.Path == "/repos/acme/app/actions/runs/2/jobs":
			fmt.Fprint(w, `{"jobs":[{"id":20,"status":"pending","labels":["self-hosted","arm64"]},{"id":21,"status":"waiting","labels":["self-hosted"]}]}`)
		case r.URL.Path == "/repos/acme/app/actions/runs/3/jobs":
			fmt.Fprint(w, `{"jobs":[{"id":30,"status":"in_progress","labels":["self-hosted"]},{"id":31,"status":"queued","labels":["self-hosted"]}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	jobs, err := c.ListWaitingJobs(context.Background(), "acme", "app")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(runCalls, ",") != "queued,pending,in_progress" {
		t.Errorf("run statuses read = %v", runCalls)
	}
	var ids []int64
	for _, j := range jobs {
		ids = append(ids, j.ID)
		if j.Repo != "acme/app" {
			t.Errorf("repo = %q", j.Repo)
		}
	}
	if fmt.Sprint(ids) != "[10 20 31]" {
		t.Errorf("waiting job ids = %v, want [10 20 31] (queued+pending, not waiting/completed/in_progress)", ids)
	}
	if jobs[0].CreatedAt.IsZero() {
		t.Errorf("created_at not parsed")
	}
}

func TestListWaitingJobs_ErrorCarriesNoToken(t *testing.T) {
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "boom test-token")
	})
	_, err := c.ListWaitingJobs(context.Background(), "acme", "app")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "boom") {
		t.Errorf("error leaks body or token: %v", err)
	}
}

func TestListRunners_PaginatesOrgAndRepoScope(t *testing.T) {
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/acme/actions/runners":
			page := r.URL.Query().Get("page")
			var rs []map[string]any
			n := 100
			if page == "2" {
				n = 5
			}
			for i := 0; i < n; i++ {
				rs = append(rs, map[string]any{"id": i, "name": fmt.Sprintf("r%s-%d", page, i), "busy": i == 0, "status": "online"})
			}
			json.NewEncoder(w).Encode(map[string]any{"total_count": 105, "runners": rs})
		case "/repos/bob/proj/actions/runners":
			fmt.Fprint(w, `{"total_count":1,"runners":[{"id":7,"name":"vm-1","busy":true,"status":"online"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	org, err := c.ListRunners(context.Background(), RunnerScope{Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(org) != 105 || !org[0].Busy {
		t.Errorf("org runners = %d (first busy=%v)", len(org), org[0].Busy)
	}
	repo, err := c.ListRunners(context.Background(), RunnerScope{Owner: "bob", Repo: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo) != 1 || repo[0].Name != "vm-1" {
		t.Errorf("repo runners = %+v", repo)
	}
}

func TestGenerateJITConfig(t *testing.T) {
	var body map[string]any
	var path string
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"runner":{"id":1},"encoded_jit_config":"QUJDREVGR0hJSktMTU5PUA=="}`)
	})
	got, err := c.GenerateJITConfig(context.Background(), RunnerScope{Owner: "acme"}, "vm-1",
		[]string{"self-hosted", "Linux", "X64"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got != "QUJDREVGR0hJSktMTU5PUA==" {
		t.Errorf("config = %q", got)
	}
	if path != "/orgs/acme/actions/runners/generate-jitconfig" {
		t.Errorf("path = %s", path)
	}
	if body["name"] != "vm-1" || body["runner_group_id"] != float64(4) {
		t.Errorf("body = %v", body)
	}

	// Repo scope defaults the group to 1.
	if _, err := c.GenerateJITConfig(context.Background(), RunnerScope{Owner: "bob", Repo: "proj"}, "vm-2", nil, 0); err != nil {
		t.Fatal(err)
	}
	if path != "/repos/bob/proj/actions/runners/generate-jitconfig" || body["runner_group_id"] != float64(1) {
		t.Errorf("repo scope path=%s body=%v", path, body)
	}
}

func TestGenerateJITConfig_RejectsOddShape(t *testing.T) {
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"encoded_jit_config":"<html>"}`)
	})
	if _, err := c.GenerateJITConfig(context.Background(), RunnerScope{Owner: "acme"}, "vm", nil, 1); err == nil {
		t.Fatal("want shape error")
	}
}

func TestGetOwnerType(t *testing.T) {
	c := newBurstTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/acme":
			fmt.Fprint(w, `{"type":"Organization"}`)
		case "/users/bob":
			fmt.Fprint(w, `{"type":"User"}`)
		default:
			w.WriteHeader(404)
		}
	})
	if got, _ := c.GetOwnerType(context.Background(), "acme"); got != OwnerOrganization {
		t.Errorf("acme = %q", got)
	}
	if got, _ := c.GetOwnerType(context.Background(), "bob"); got != OwnerUser {
		t.Errorf("bob = %q", got)
	}
	if _, err := c.GetOwnerType(context.Background(), "nobody"); !IsNotFound(err) {
		t.Errorf("want not found, got %v", err)
	}
}
