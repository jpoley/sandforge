// Package forge handles Forgejo: the idempotent bootstrap (health-gate -> admin -> tokens ->
// runner registration -> online, design §14) plus a small REST client for repo/PR operations.
package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client talks to a Forgejo instance over its REST API using an access token.
type Client struct {
	Base  string // e.g. http://127.0.0.1:3000
	Token string
	http  *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, r)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

// WaitHealthy polls /api/healthz until it reports pass or the deadline elapses (design §14:
// the clone URL is not printed until the forge answers).
func WaitHealthy(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 3 * time.Second}
	base = strings.TrimRight(base, "/")
	for time.Now().Before(deadline) {
		resp, err := hc.Get(base + "/api/healthz")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(b), "\"pass\"") {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("forge not healthy at %s within %s", base, timeout)
}

// Repo is a minimal Forgejo repo representation.
type Repo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
}

// CreateRepo creates a repo under the authenticated user (idempotent: returns existing on 409).
func (c *Client) CreateRepo(name, defaultBranch string, autoInit bool) (*Repo, error) {
	resp, err := c.do("POST", "/api/v1/user/repos", map[string]any{
		"name": name, "private": false, "auto_init": autoInit, "default_branch": defaultBranch,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 201 {
		var r Repo
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("create repo %s: decode response: %w", name, err)
		}
		return &r, nil
	}
	if resp.StatusCode == 409 {
		return c.GetRepo(c.authUser(), name)
	}
	return nil, fmt.Errorf("create repo %s: %d %s", name, resp.StatusCode, string(b))
}

// DeleteRepo removes a repo under the given owner (idempotent: a 404 is treated as success).
func (c *Client) DeleteRepo(owner, name string) error {
	resp, err := c.do("DELETE", "/api/v1/repos/"+owner+"/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 || resp.StatusCode == 404 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete repo %s/%s: %d %s", owner, name, resp.StatusCode, string(b))
}

// GetRepoOK reports whether the token authenticates (GET /api/v1/user == 200).
func (c *Client) GetRepoOK() bool {
	resp, err := c.do("GET", "/api/v1/user", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func (c *Client) authUser() string {
	resp, err := c.do("GET", "/api/v1/user", nil)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	json.NewDecoder(resp.Body).Decode(&u)
	return u.Login
}

func (c *Client) GetRepo(owner, name string) (*Repo, error) {
	resp, err := c.do("GET", "/api/v1/repos/"+owner+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("get repo: %d", resp.StatusCode)
	}
	var r Repo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("get repo %s/%s: decode response: %w", owner, name, err)
	}
	return &r, nil
}

// ActionTask is one workflow run as seen by the actions/tasks API.
type ActionTask struct {
	RunNumber  int    `json:"run_number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	WorkflowID string `json:"workflow_id"`
}

// LatestTaskFor returns the most recent action task for a branch (or overall if branch == "").
func (c *Client) Tasks(owner, repo string) ([]ActionTask, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/v1/repos/%s/%s/actions/tasks", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tasks: %d %s", resp.StatusCode, string(b))
	}
	var out struct {
		Runs []ActionTask `json:"workflow_runs"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Runs, nil
}

// WaitRunComplete polls until the latest run for headSHA reaches a terminal status, returning
// whether it succeeded and the elapsed time. The Forgejo tasks API encodes a finished-OK run as
// status "success"; a finished-failed run as status "failure"; in-flight as "running"/"waiting".
func (c *Client) WaitRunComplete(owner, repo, headSHA string, timeout time.Duration) (ok bool, elapsed time.Duration, status string, err error) {
	start := time.Now()
	deadline := start.Add(timeout)
	terminal := map[string]bool{"success": true, "failure": true, "cancelled": true, "skipped": true}
	for time.Now().Before(deadline) {
		tasks, e := c.Tasks(owner, repo)
		if e == nil {
			for _, t := range tasks {
				// A task with an empty head_sha (queued/unassigned) must NOT match — otherwise
				// HasPrefix(x, "") is always true and we'd report another run's status as ours.
				if t.HeadSHA == "" {
					continue
				}
				if headSHA == "" || strings.HasPrefix(t.HeadSHA, headSHA) || strings.HasPrefix(headSHA, t.HeadSHA) {
					if terminal[t.Status] {
						return t.Status == "success", time.Since(start), t.Status, nil
					}
					// Non-terminal match: keep scanning in case a later task for the same SHA is
					// already terminal; otherwise we poll again after the sleep.
					continue
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return false, time.Since(start), "timeout", fmt.Errorf("run for %s did not complete within %s", headSHA, timeout)
}

// PullRequest is a minimal PR representation.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"html_url"`
}

// CreatePR opens a pull request (used for the local-Forgejo upstream path).
func (c *Client) CreatePR(owner, repo, head, base, title, body string) (*PullRequest, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/repos/%s/%s/pulls", owner, repo), map[string]any{
		"head": head, "base": base, "title": title, "body": body,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 201 {
		var pr PullRequest
		if err := json.Unmarshal(b, &pr); err != nil {
			return nil, fmt.Errorf("create PR: decode response: %w", err)
		}
		return &pr, nil
	}
	return nil, fmt.Errorf("create PR: %d %s", resp.StatusCode, string(b))
}

// Comment posts an issue/PR comment (review handoff, AC-3).
func (c *Client) Comment(owner, repo string, index int, body string) error {
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", owner, repo, index), map[string]any{"body": body})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("comment: %d %s", resp.StatusCode, string(b))
	}
	return nil
}

// ListRepos returns the full-names ("owner/name") of every repo the token can see. It paginates
// through ALL pages: a single limit=50 page would silently drop repos for a token with >50 repos,
// so those repos would never get a webhook and their @mentions would never route.
func (c *Client) ListRepos() ([]string, error) {
	const pageSize = 50
	var out []string
	for page := 1; ; page++ {
		resp, err := c.do("GET", fmt.Sprintf("/api/v1/user/repos?limit=%d&page=%d", pageSize, page), nil)
		if err != nil {
			return nil, err
		}
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("list repos (page %d): read response: %w", page, rerr)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("list repos (page %d): %d %s", page, resp.StatusCode, string(b))
		}
		var repos []Repo
		if err := json.Unmarshal(b, &repos); err != nil {
			return nil, fmt.Errorf("list repos (page %d): decode response: %w", page, err)
		}
		for _, r := range repos {
			if r.FullName != "" {
				out = append(out, r.FullName)
			}
		}
		// A short (or empty) page means we've reached the last one — stop before an empty page.
		if len(repos) < pageSize {
			break
		}
	}
	return out, nil
}

// Hook is a Forgejo repo webhook (Gitea-compatible API).
type Hook struct {
	ID     int64             `json:"id"`
	Type   string            `json:"type"`
	Active bool              `json:"active"`
	Events []string          `json:"events"`
	Config map[string]string `json:"config"` // url, content_type, secret
}

// ListHooks returns the repo's webhooks (used to register idempotently — no duplicate on restart).
func (c *Client) ListHooks(owner, repo string) ([]Hook, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/v1/repos/%s/%s/hooks?limit=50", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list hooks %s/%s: %d %s", owner, repo, resp.StatusCode, string(b))
	}
	var hooks []Hook
	if err := json.NewDecoder(resp.Body).Decode(&hooks); err != nil {
		return nil, fmt.Errorf("list hooks %s/%s: decode response: %w", owner, repo, err)
	}
	return hooks, nil
}

// EnsureHook registers a webhook pointing at targetURL for the given events. If a hook with the
// same URL already exists it is UPDATED (PATCH) to the current secret + events + active=true rather
// than reused as-is: a hook left with a stale secret (or deactivated, or missing events) would make
// the router permanently reject deliveries (signature mismatch) until edited by hand. Idempotent.
func (c *Client) EnsureHook(owner, repo, targetURL, secret string, events []string) (*Hook, error) {
	existing, err := c.ListHooks(owner, repo)
	if err != nil {
		return nil, err
	}
	for i := range existing {
		if existing[i].Config["url"] == targetURL {
			return c.updateHook(owner, repo, existing[i].ID, targetURL, secret, events)
		}
	}
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/repos/%s/%s/hooks", owner, repo), map[string]any{
		"type":   "gitea",
		"active": true,
		"events": events,
		"config": map[string]string{"url": targetURL, "content_type": "json", "secret": secret},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 201 {
		var h Hook
		if err := json.Unmarshal(b, &h); err != nil {
			return nil, fmt.Errorf("create hook %s/%s: decode response: %w", owner, repo, err)
		}
		return &h, nil
	}
	return nil, fmt.Errorf("create hook %s/%s: %d %s", owner, repo, resp.StatusCode, string(b))
}

// updateHook PATCHes an existing webhook to the desired secret/events/active state.
func (c *Client) updateHook(owner, repo string, id int64, targetURL, secret string, events []string) (*Hook, error) {
	resp, err := c.do("PATCH", fmt.Sprintf("/api/v1/repos/%s/%s/hooks/%d", owner, repo, id), map[string]any{
		"active": true,
		"events": events,
		"config": map[string]string{"url": targetURL, "content_type": "json", "secret": secret},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		var h Hook
		if err := json.Unmarshal(b, &h); err != nil {
			return nil, fmt.Errorf("update hook %s/%s#%d: decode response: %w", owner, repo, id, err)
		}
		return &h, nil
	}
	return nil, fmt.Errorf("update hook %s/%s#%d: %d %s", owner, repo, id, resp.StatusCode, string(b))
}

// DeleteHook removes a webhook by id (idempotent: a 404 is treated as success).
func (c *Client) DeleteHook(owner, repo string, id int64) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/v1/repos/%s/%s/hooks/%d", owner, repo, id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 || resp.StatusCode == 404 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete hook %s/%s#%d: %d %s", owner, repo, id, resp.StatusCode, string(b))
}

// PullRef is the head/base of a pull request (the head ref is the branch an agent checks out + pushes to).
type PullRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Head   struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// GetPull fetches a single pull request (to resolve its head branch for an agent handoff).
func (c *Client) GetPull(owner, repo string, index int) (*PullRef, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", owner, repo, index), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get pull %s/%s#%d: %d %s", owner, repo, index, resp.StatusCode, string(b))
	}
	var pr PullRef
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("get pull %s/%s#%d: decode response: %w", owner, repo, index, err)
	}
	return &pr, nil
}

// Credentials persisted at ~/.sandforge/<project>/credentials (0600).
type Credentials struct {
	URL      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

func LoadCredentials(stateDir string) (*Credentials, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "credentials"))
	if err != nil {
		return nil, err
	}
	var cr Credentials
	if err := json.Unmarshal(b, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

func SaveCredentials(stateDir string, cr *Credentials) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cr, "", "  ")
	return os.WriteFile(filepath.Join(stateDir, "credentials"), b, 0o600)
}

// SaveDockerGID persists the detected docker-socket gid so non-init commands (down/status/logs)
// can supply it to compose without re-probing — letting the compose file REQUIRE it
// (${SANDFORGE_DOCKER_GID:?}) instead of guessing a default.
func SaveDockerGID(stateDir, gid string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, "docker_gid"), []byte(gid), 0o600)
}

// LoadDockerGID reads the persisted docker-socket gid ("" if absent).
func LoadDockerGID(stateDir string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, "docker_gid"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveRunnerMode persists the resolved runner mode (socket|tcp) and, for tcp, the runner-facing
// DOCKER_HOST — so non-init commands (down/status/logs) select the SAME runner-mode overlay and
// endpoint the instance was brought up with, without re-detecting. One line each, "" if none.
func SaveRunnerMode(stateDir, mode, dockerHost string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runner_mode"), []byte(mode), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, "runner_docker_host"), []byte(dockerHost), 0o600)
}

// LoadRunnerMode reads the persisted runner mode and tcp DOCKER_HOST ("" if absent — callers then
// default to socket for back-compat with instances created before runner modes existed).
func LoadRunnerMode(stateDir string) (mode, dockerHost string) {
	if b, err := os.ReadFile(filepath.Join(stateDir, "runner_mode")); err == nil {
		mode = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(stateDir, "runner_docker_host")); err == nil {
		dockerHost = strings.TrimSpace(string(b))
	}
	return mode, dockerHost
}
