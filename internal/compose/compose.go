// Package compose is a thin wrapper around `docker compose` and `docker` for the control-plane
// and deploy-target stacks (design §13: wrap off-the-shelf parts, don't reinvent).
package compose

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run executes a command with the given env, streaming nothing; returns combined output.
func Run(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// RunStream runs a command inheriting stdout/stderr (for long, user-visible operations).
func RunStream(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Compose runs `docker compose -f <file> -p? <args...>`. Project is set via env in the file.
func Compose(env []string, file string, args ...string) (string, error) {
	full := append([]string{"compose", "-f", file}, args...)
	return Run(env, "docker", full...)
}

// ComposeStream is Compose with inherited stdio.
func ComposeStream(env []string, file string, args ...string) error {
	full := append([]string{"compose", "-f", file}, args...)
	return RunStream(env, "docker", full...)
}

// fileArgs builds the `-f a -f b …` prefix shared by the multi-file compose helpers. Passing more
// than one file lets the CLI overlay a runner-mode file (socket/tcp) onto the base control-plane
// file; compose merges them into one project (the first file's `name:` wins).
func fileArgs(files []string) []string {
	args := make([]string, 0, len(files)*2)
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}

// ComposeFiles runs `docker compose -f <f1> -f <f2> … <args…>` against a merged set of files.
func ComposeFiles(env []string, files []string, args ...string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("compose: no files provided")
	}
	full := append(append([]string{"compose"}, fileArgs(files)...), args...)
	return Run(env, "docker", full...)
}

// ComposeFilesStream is ComposeFiles with inherited stdio.
func ComposeFilesStream(env []string, files []string, args ...string) error {
	if len(files) == 0 {
		return fmt.Errorf("compose: no files provided")
	}
	full := append(append([]string{"compose"}, fileArgs(files)...), args...)
	return RunStream(env, "docker", full...)
}

// ExecUserFiles runs a command inside a service (as a user) against a merged set of files.
func ExecUserFiles(env []string, files []string, service, user string, argv ...string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("compose: no files provided")
	}
	full := append(append([]string{"compose"}, fileArgs(files)...), "exec", "-T", "-u", user, service)
	full = append(full, argv...)
	return Run(env, "docker", full...)
}

// Exec runs a command inside a running compose service (e.g. forgejo) and returns output.
func Exec(env []string, file, service string, argv ...string) (string, error) {
	args := append([]string{"compose", "-f", file, "exec", "-T"}, service)
	args = append(args, argv...)
	return Run(env, "docker", args...)
}

// ExecUser runs a command inside a service as a specific user.
func ExecUser(env []string, file, service, user string, argv ...string) (string, error) {
	args := append([]string{"compose", "-f", file, "exec", "-T", "-u", user, service}, argv...)
	return Run(env, "docker", args...)
}

// ImagePresent reports whether an image exists locally.
func ImagePresent(ref string) bool {
	out, _ := Run(os.Environ(), "docker", "images", "-q", ref)
	return strings.TrimSpace(out) != ""
}

// Pull pulls an image if absent.
func Pull(ref string) error {
	if ImagePresent(ref) {
		return nil
	}
	return RunStream(os.Environ(), "docker", "pull", ref)
}

// MustDocker errors out if docker/compose aren't usable.
func MustDocker() error {
	if _, err := Run(os.Environ(), "docker", "info"); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	if _, err := Run(os.Environ(), "docker", "compose", "version"); err != nil {
		return fmt.Errorf("docker compose plugin missing: %w", err)
	}
	return nil
}
