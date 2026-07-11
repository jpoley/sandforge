package prd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const samplePRD = `---
prd:
  id: test
  title: "Test PRD"
  version: 0.1.0
  success_criteria:
    - id: SC-1
      statement: "one"
      level: integration
      gate: work-staging
      verify: "noop"
    - id: SC-2
      statement: "two"
      level: e2e
      gate: staging-main
      verify: "noop"
    - id: SC-3
      statement: "three"
      level: e2e
      gate: staging-main
      verify: "noop"
---
# body ignored
`

func writePRD(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "prd.md")
	if err := os.WriteFile(p, []byte(samplePRD), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParse(t *testing.T) {
	p, err := Parse(writePRD(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.SuccessCriteria) != 3 {
		t.Fatalf("got %d criteria", len(p.SuccessCriteria))
	}
	if p.SuccessCriteria[1].Gate != "staging-main" {
		t.Errorf("SC-2 gate = %q", p.SuccessCriteria[1].Gate)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	if err := os.WriteFile(p, []byte("# no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(p); err == nil {
		t.Error("expected error for missing frontmatter")
	}
}

// fakeVerify writes a script that passes for ids in passList, fails otherwise.
func fakeVerify(t *testing.T, passList string) string {
	t.Helper()
	dir := t.TempDir()
	s := filepath.Join(dir, "verify.sh")
	body := "#!/usr/bin/env bash\ncase \"$1\" in\n" + passList + ") exit 0;; *) exit 1;; esac\n"
	if err := os.WriteFile(s, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateGatesAndSkip(t *testing.T) {
	p, err := Parse(writePRD(t))
	if err != nil {
		t.Fatal(err)
	}
	// SC-1 passes, SC-2 fails (staging-main -> blocking), SC-3 skipped.
	rn := &Runner{
		VerifyScript: fakeVerify(t, "SC-1"),
		Skip:         []string{"SC-3"},
	}
	rep := rn.Validate(p, nil)
	if len(rep.Results) != 3 {
		t.Fatalf("got %d results", len(rep.Results))
	}
	// SC-3 must be skipped.
	var sc3 Result
	for _, r := range rep.Results {
		if r.Criterion.ID == "SC-3" {
			sc3 = r
		}
	}
	if !sc3.Skipped {
		t.Error("SC-3 should be skipped")
	}
	// SC-2 failed and is a staging-main gate -> blocks upstream.
	if rep.Passed() {
		t.Error("report should NOT pass (SC-2 staging-main failed)")
	}
	if len(rep.BlockingFails) != 1 || rep.BlockingFails[0].Criterion.ID != "SC-2" {
		t.Errorf("expected SC-2 blocking, got %+v", rep.BlockingFails)
	}
}

func TestValidateTimeoutFailsLoud(t *testing.T) {
	p, err := Parse(writePRD(t))
	if err != nil {
		t.Fatal(err)
	}
	// A verify script that sleeps longer than the timeout must FAIL (not hang), and because SC-2 is
	// a staging-main gate, it must block (appear in BlockingFails).
	dir := t.TempDir()
	slow := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(slow, []byte("#!/usr/bin/env bash\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rn := &Runner{VerifyScript: slow, Only: []string{"SC-2"}, Timeout: 100 * time.Millisecond}
	rep := rn.Validate(p, nil)
	var sc2 Result
	for _, r := range rep.Results {
		if r.Criterion.ID == "SC-2" {
			sc2 = r
		}
	}
	if sc2.Pass {
		t.Error("SC-2 should FAIL on timeout")
	}
	if !strings.Contains(sc2.Output, "timed out") {
		t.Errorf("expected timeout note in output, got %q", sc2.Output)
	}
	if rep.Passed() {
		t.Error("a timed-out staging-main criterion must block upstream")
	}
}

func TestValidateAllPass(t *testing.T) {
	p, err := Parse(writePRD(t))
	if err != nil {
		t.Fatal(err)
	}
	rn := &Runner{VerifyScript: fakeVerify(t, "SC-1|SC-2|SC-3")}
	rep := rn.Validate(p, nil)
	if !rep.Passed() {
		t.Errorf("expected pass, blocking=%+v", rep.BlockingFails)
	}
}
