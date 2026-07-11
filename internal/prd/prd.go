// Package prd parses the machine-readable PRD (prd.md frontmatter) and runs each success
// criterion against the running deploy-target stack, emitting a per-criterion report (docs/goal.md
// AC-7, design §7). The PRD is the single source of truth for "done".
package prd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Criterion is one success criterion from the PRD header.
type Criterion struct {
	ID        string `yaml:"id"`
	Statement string `yaml:"statement"`
	Level     string `yaml:"level"` // unit|integration|e2e|nonfunctional
	Gate      string `yaml:"gate"`  // work-staging|staging-main
	Verify    string `yaml:"verify"`
}

// PRD is the parsed structured header.
type PRD struct {
	ID              string      `yaml:"id"`
	Title           string      `yaml:"title"`
	Version         string      `yaml:"version"`
	SuccessCriteria []Criterion `yaml:"success_criteria"`
}

type frontmatter struct {
	PRD PRD `yaml:"prd"`
}

// Parse reads prd.md and extracts the YAML frontmatter (between the first two `---` lines).
func Parse(path string) (*PRD, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(b)
	// frontmatter is the block between the first '---' and the next '---'
	parts := strings.SplitN(s, "\n---", 2)
	if !strings.HasPrefix(strings.TrimSpace(s), "---") || len(parts) < 2 {
		return nil, fmt.Errorf("%s: no YAML frontmatter found", path)
	}
	head := strings.TrimPrefix(strings.TrimSpace(parts[0]), "---")
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return nil, fmt.Errorf("parse prd frontmatter: %w", err)
	}
	if len(fm.PRD.SuccessCriteria) == 0 {
		return nil, fmt.Errorf("%s: no success_criteria parsed", path)
	}
	return &fm.PRD, nil
}

// Result is the outcome of validating one criterion.
type Result struct {
	Criterion Criterion
	Pass      bool
	Skipped   bool
	Output    string
	Elapsed   time.Duration
}

// Report is the full set of results plus gate evaluation.
type Report struct {
	Results       []Result
	BlockingFails []Result // failed staging-main criteria — these block `upstream`
}

// Passed reports whether `upstream` may proceed (no failed staging-main gate, docs/goal.md AC-7).
func (r *Report) Passed() bool { return len(r.BlockingFails) == 0 }

// Runner executes a criterion's verify check. The deploy target supplies verify.sh <id>.
type Runner struct {
	VerifyScript string   // path to deploy/tasks-app/verify.sh
	WorkDir      string   // cwd for the script (deploy/tasks-app)
	Env          []string // BASE_URL, COMPOSE, TASKS_* etc.
	Only         []string // if non-empty, only run these criterion ids; others Skipped
	Skip         []string // criterion ids to skip (e.g. SC-6/SC-7 when Playwright is disabled)
	Timeout      time.Duration
}

func (rn *Runner) only(id string) bool {
	for _, s := range rn.Skip {
		if s == id {
			return false
		}
	}
	if len(rn.Only) == 0 {
		return true
	}
	for _, o := range rn.Only {
		if o == id {
			return true
		}
	}
	return false
}

// Validate runs each criterion's verify.sh and assembles the report.
func (rn *Runner) Validate(p *PRD, emit func(Result)) *Report {
	if rn.Timeout == 0 {
		rn.Timeout = 5 * time.Minute
	}
	rep := &Report{}
	for _, c := range p.SuccessCriteria {
		res := Result{Criterion: c}
		if !rn.only(c.ID) {
			res.Skipped = true
			rep.Results = append(rep.Results, res)
			if emit != nil {
				emit(res)
			}
			continue
		}
		start := time.Now()
		// Enforce the per-criterion timeout (the field was previously unused): a hung verify.sh
		// must FAIL loud, not block the whole CLI forever (and leave the deploy-target stack up).
		ctx, cancel := context.WithTimeout(context.Background(), rn.Timeout)
		cmd := exec.CommandContext(ctx, "bash", rn.VerifyScript, c.ID)
		cmd.Dir = rn.WorkDir
		cmd.Env = rn.Env
		// Run verify.sh in its own process group and, on timeout, kill the WHOLE group — otherwise
		// a child (curl/sleep) outlives the killed bash, keeps the output pipe open, and the CLI
		// hangs anyway despite the deadline. Unix-only (build-tagged); a no-op elsewhere.
		setProcGroupKill(cmd)
		out, err := cmd.CombinedOutput()
		cancel()
		res.Elapsed = time.Since(start)
		res.Output = strings.TrimSpace(string(out))
		if ctx.Err() == context.DeadlineExceeded {
			res.Pass = false
			res.Output += fmt.Sprintf("\n[verify timed out after %s]", rn.Timeout)
		} else {
			res.Pass = err == nil
		}
		rep.Results = append(rep.Results, res)
		if !res.Pass && c.Gate == "staging-main" {
			rep.BlockingFails = append(rep.BlockingFails, res)
		}
		if emit != nil {
			emit(res)
		}
	}
	return rep
}

// Markdown renders the success-criteria report for the PR body (AC-6).
func (r *Report) Markdown(p *PRD) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## PRD success-criteria report — %s (%s)\n\n", p.Title, p.Version)
	fmt.Fprintf(&b, "| SC | Gate | Level | Result | Time |\n|----|------|-------|--------|------|\n")
	for _, res := range r.Results {
		status := "✅ pass"
		if res.Skipped {
			status = "⊘ skipped"
		} else if !res.Pass {
			status = "❌ FAIL"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			res.Criterion.ID, res.Criterion.Gate, res.Criterion.Level, status, res.Elapsed.Round(time.Millisecond))
	}
	if r.Passed() {
		fmt.Fprintf(&b, "\n**All `staging-main` gates passed — eligible for upstream.**\n")
	} else {
		fmt.Fprintf(&b, "\n**BLOCKED: %d `staging-main` gate(s) failed.**\n", len(r.BlockingFails))
	}
	return b.String()
}
