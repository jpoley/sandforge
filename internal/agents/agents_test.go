package agents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

const sampleComment = `{
  "action":"created","is_pull":true,
  "comment":{"id":4,"body":"@claude please review --focus=security","user":{"login":"alice"}},
  "issue":{"number":2,"title":"spike"},
  "pull_request":{"number":2,"title":"spike","head":{"ref":"work/claude/x","sha":"abc"}},
  "repository":{"full_name":"sandforge/tasks","name":"tasks","owner":{"login":"sandforge"}},
  "sender":{"login":"alice"}
}`

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(sampleComment)
	good := sign("s3cret", sampleComment)
	if err := VerifySignature("s3cret", body, good); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifySignature("s3cret", body, "sha256="+good); err != nil {
		t.Fatalf("sha256= prefixed signature rejected: %v", err)
	}
	if err := VerifySignature("s3cret", body, sign("wrong", sampleComment)); err == nil {
		t.Fatal("expected mismatch error for wrong secret")
	}
	if err := VerifySignature("", body, ""); err != nil {
		t.Fatalf("empty secret should disable verification: %v", err)
	}
	if err := VerifySignature("s3cret", body, ""); err == nil {
		t.Fatal("expected error for missing signature when secret set")
	}
}

func TestMentions(t *testing.T) {
	cases := map[string][]string{
		"@claude please review":          {"claude"},
		"hey @Claude and @codex go":      {"claude", "codex"},
		"email foo@bar.com is not a tag": nil,
		"path a/@notatag":                nil,
		"@claude @claude dup":            {"claude"},
		"start of line\n@codex fix it":   {"codex"},
	}
	for body, want := range cases {
		got := Mentions(body)
		if len(got) != len(want) {
			t.Errorf("Mentions(%q) = %v, want %v", body, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Mentions(%q)[%d] = %q, want %q", body, i, got[i], want[i])
			}
		}
	}
}

func TestParsePayload(t *testing.T) {
	p, err := ParsePayload([]byte(sampleComment))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != "created" || !p.IsPull || p.Issue.Number != 2 {
		t.Fatalf("bad parse: %+v", p)
	}
	if p.Repository.Owner.Login != "sandforge" || p.Repository.Name != "tasks" {
		t.Fatalf("bad repo: %+v", p.Repository)
	}
	if p.PullRequest == nil || p.PullRequest.Head.Ref != "work/claude/x" {
		t.Fatalf("bad pr head: %+v", p.PullRequest)
	}
	if _, err := ParsePayload([]byte(`{"bogus":true}`)); err == nil {
		t.Fatal("expected error for payload with no repository")
	}
}

// fakeDispatcher records jobs and returns a canned result.
type fakeDispatcher struct{ jobs []Job }

func (f *fakeDispatcher) Dispatch(j Job) Result {
	f.jobs = append(f.jobs, j)
	return Result{Output: "ok @" + j.Handle}
}

func newTestRouter(t *testing.T) (*Router, *fakeDispatcher) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Secret = "" // disable sig verify for routing tests
	fd := &fakeDispatcher{}
	return &Router{Cfg: cfg, Store: NewStore(dir), Dispatcher: fd}, fd
}

func TestRouteMentionDispatches(t *testing.T) {
	r, fd := newTestRouter(t)
	if _, err := r.HandleDelivery("issue_comment", []byte(sampleComment), ""); err != nil {
		t.Fatal(err)
	}
	if len(fd.jobs) != 1 {
		t.Fatalf("expected 1 dispatched job, got %d", len(fd.jobs))
	}
	j := fd.jobs[0]
	if j.Handle != "claude" || j.IssueNumber != 2 || !j.IsPull || j.Branch != "work/claude/x" {
		t.Fatalf("bad job: %+v", j)
	}
	// A routed delivery must carry an explicit status (never blank) so the UI doesn't render an
	// empty state (PR review fixes #4/#5).
	var sawReceived bool
	for _, e := range r.Store.Recent(20) {
		if e.Kind == "delivery" {
			if e.Status == "" {
				t.Fatalf("routed delivery event has empty status: %+v", e)
			}
			if e.Status == StatusReceived {
				sawReceived = true
			}
		}
	}
	if !sawReceived {
		t.Fatal("expected a delivery event with status=received")
	}
}

func TestLoopGuardIgnoresAgentComment(t *testing.T) {
	r, fd := newTestRouter(t)
	body := `{"action":"created","comment":{"body":"@claude rerun ` + AgentMarker +
		`","user":{"login":"sandforge"}},"issue":{"number":1},"repository":{"name":"tasks","owner":{"login":"sandforge"}},"sender":{"login":"sandforge"}}`
	if _, err := r.HandleDelivery("issue_comment", []byte(body), ""); err != nil {
		t.Fatal(err)
	}
	if len(fd.jobs) != 0 {
		t.Fatalf("loop-guard failed: agent-marked comment dispatched %d jobs", len(fd.jobs))
	}
}

func TestUnknownMentionNotDispatched(t *testing.T) {
	r, fd := newTestRouter(t)
	body := `{"action":"created","comment":{"body":"@nobody hi","user":{"login":"a"}},"issue":{"number":1},"repository":{"name":"tasks","owner":{"login":"sandforge"}},"sender":{"login":"a"}}`
	if _, err := r.HandleDelivery("issue_comment", []byte(body), ""); err != nil {
		t.Fatal(err)
	}
	if len(fd.jobs) != 0 {
		t.Fatalf("expected no dispatch for unknown handle, got %d", len(fd.jobs))
	}
}

func TestConfigPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secret == "" || len(cfg.Agents) == 0 {
		t.Fatal("expected seeded secret + agents on first load")
	}
	if err := cfg.Upsert(Agent{Handle: "@Gemini", Command: []string{"echo", "hi"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Upsert(Agent{Handle: "noop"}); err == nil {
		t.Fatal("expected error: agent with no command")
	}
	// reload from disk and confirm persistence + handle normalization
	cfg2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.FindAgent("gemini") == nil {
		t.Fatal("upserted agent not persisted/normalized")
	}
	if err := cfg2.Remove("gemini"); err != nil {
		t.Fatal(err)
	}
	if cfg2.FindAgent("gemini") != nil {
		t.Fatal("agent not removed")
	}
	if _, err := LoadConfig(filepath.Dir(dir)); err != nil {
		t.Fatalf("LoadConfig on fresh dir should succeed: %v", err)
	}
}

func TestLoopbackGuard(t *testing.T) {
	// the dangerous routes (which exec host commands) must reject non-loopback callers.
	cases := map[string]bool{
		"127.0.0.1:5000":  true,
		"[::1]:5000":      true,
		"192.168.1.5:443": false,
		"10.0.0.2:80":     false,
		"host.docker:80":  false, // unparseable host → not loopback
	}
	for addr, want := range cases {
		if got := isLoopbackRemote(addr); got != want {
			t.Errorf("isLoopbackRemote(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	for i := 0; i < 3; i++ {
		s.Append(Event{Kind: "route", Handle: "claude", Status: StatusDone})
	}
	got := s.Recent(10)
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	if got[0].ID == "" || got[0].TS.IsZero() {
		t.Fatal("event missing stamped ID/TS")
	}
}
