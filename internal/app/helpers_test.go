package app

import (
	"net"
	"strings"
	"testing"
)

func TestFreeTCPPort(t *testing.T) {
	p, err := freeTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	// It should be a usable, currently-free port we can bind.
	l, err := net.Listen("tcp", "127.0.0.1:"+p)
	if err != nil {
		t.Fatalf("freeTCPPort returned %q which is not bindable: %v", p, err)
	}
	l.Close()
}

func TestRunnerLogReady(t *testing.T) {
	online := []string{"declared successfully", "poller", "is online"}
	// online signal, no error -> ready
	if !runnerLogReady("starting\nrunner declared successfully\n[poller 0] launched\n", online) {
		t.Error("should be ready (online, no error)")
	}
	// error AFTER online -> NOT ready (a later crash)
	if runnerLogReady("declared successfully\ncannot ping the docker daemon\n", online) {
		t.Error("should NOT be ready (error after online)")
	}
	// online AFTER a stale early error -> ready
	if !runnerLogReady("cannot ping the docker daemon\n...recovered...\nis online\n", online) {
		t.Error("should be ready (online after stale error)")
	}
	// no online signal at all -> NOT ready
	if runnerLogReady("starting runner daemon\n", online) {
		t.Error("should NOT be ready (no online signal)")
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("the runner declared successfully now", []string{"nope", "declared successfully"}) {
		t.Error("should match")
	}
	if containsAny("nothing here", []string{"x", "y"}) {
		t.Error("should not match")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("got %q", got)
	}
	got := truncate("hello world", 5)
	if !strings.HasPrefix(got, "hello") || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
}

func TestIndent(t *testing.T) {
	got := indent("a\nb", "  ")
	if got != "  a\n  b" {
		t.Errorf("got %q", got)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("a\nb\nc\n"); got != "c" {
		t.Errorf("got %q", got)
	}
	if got := lastLine("solo"); got != "solo" {
		t.Errorf("got %q", got)
	}
}

func TestPRNumberFromURL(t *testing.T) {
	cases := map[string]int{
		"https://github.com/o/r/pull/42":      42,
		"https://github.com/o/r/pull/7/files": 7,
		"http://127.0.0.1:3000/o/r/pulls/3":   0, // forge "pulls" path, not "/pull/"
		"https://github.com/o/r/pull/":        0,
		"no-url":                              0,
	}
	for in, want := range cases {
		if got := prNumberFromURL(in); got != want {
			t.Errorf("prNumberFromURL(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestGithubSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/jpoley/sandforge.git":            "jpoley/sandforge",
		"https://x:tok@github.com/jpoley/sandforge.git":      "jpoley/sandforge",
		"git@github.com:jpoley/sandforge.git":                "jpoley/sandforge",
		"http://127.0.0.1:3000/sandforge/tasks-upstream.git": "",
	}
	for in, want := range cases {
		if got := githubSlug(in); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafePrefix(t *testing.T) {
	if got := safePrefix("0123456789abcdef"); got != "01234567" {
		t.Errorf("got %q", got)
	}
	if got := safePrefix("short"); got != "short" {
		t.Errorf("got %q", got)
	}
}
