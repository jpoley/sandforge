//go:build mage

// Mage build file for Sandforge — replaces the Makefile (the user wants no make).
//
//	go install github.com/magefile/mage@latest   # one-time, if you don't have mage
//	mage build                                    # compile the sandforge binary
//	mage e2e                                      # stand it up + run the whole closed loop
//	mage -l                                       # list all targets
//
// End users do NOT need mage or this repo: "mage install" (or a release binary) drops a
// self-contained sandforge on PATH with all deploy assets embedded (see assets.go, package sandforge).
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Default target when you run bare `mage`.
var Default = Build

const (
	bin       = "bin/sandforge"
	pkg       = "./cmd/sandforge"
	ciImage   = "sandforge/ci:ubuntu-22.04"
	ciCtx     = "deploy/ci-image"
	assetsTar = "assets/deploy.tar.gz"
)

// Build compiles the sandforge CLI to bin/sandforge, regenerating the embedded asset tarball first
// so the binary is always self-contained and current.
func Build() error {
	mg.Deps(GenAssets)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil { // bin/ may not exist on a fresh checkout
		return err
	}
	fmt.Println("• build", bin)
	return sh.RunV(goBin(), "build", "-o", bin, pkg)
}

// assetRoots are the trees baked into the binary (relative to repo root).
var assetRoots = []string{"deploy", "docs/prd.md"}

// assetExcludeFrag matches path fragments that must never be embedded (npm/build output, vcs).
var assetExcludeFrag = []string{
	"/node_modules/", "/dist/", "/test-results/", "/playwright-report/", "/.git/",
	// The agents-ui SOURCE isn't needed at runtime by the standalone binary — the built UI is
	// embedded separately (internal/agents/webdist). Keep its src/configs out of the deploy tarball.
	"/agents-ui/",
}

// assetExcludeSuffix matches build artifacts by suffix (anchored so we don't drop e.g. changelog).
var assetExcludeSuffix = []string{"/tasksapp", ".log"}

func excluded(rel string) bool {
	p := "/" + filepath.ToSlash(rel)
	for _, frag := range assetExcludeFrag {
		if strings.Contains(p, frag) {
			return true
		}
	}
	for _, suf := range assetExcludeSuffix {
		if strings.HasSuffix(p, suf) {
			return true
		}
	}
	return false
}

// GenAssets (re)builds assets/deploy.tar.gz from the deploy tree + docs/prd.md. A tarball is used
// instead of //go:embed all: because go:embed silently drops nested-module subtrees
// (deploy/tasks-app/backend has its own go.mod) and errors on npm's symlinks. Deterministic output
// (sorted, zeroed mtimes) keeps the committed artifact stable across rebuilds.
func GenAssets() error {
	var files []string
	for _, root := range assetRoots {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if excluded(p + "/") {
					return filepath.SkipDir
				}
				return nil
			}
			if excluded(p) {
				return nil
			}
			files = append(files, filepath.ToSlash(p))
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Strings(files)
	// Guard: the nested-module backend + the CI workflow must be present, or we'd ship a binary
	// that fails graduate later (the exact bug a naive embed caused).
	for _, must := range []string{"deploy/tasks-app/backend/main.go", "deploy/tasks-app/.github/workflows/ci.yml"} {
		if !contains(files, must) {
			return fmt.Errorf("genAssets: required asset %q not found — refusing to ship an incomplete binary", must)
		}
	}
	if err := os.MkdirAll(filepath.Dir(assetsTar), 0o755); err != nil {
		return err
	}
	// Write to a UNIQUE temp file in the same dir and atomically rename on success: a partial
	// walk/read/write never leaves a truncated tarball, and a unique name means two concurrent
	// `mage build`/`genAssets` runs can't clobber each other's temp file.
	out, err := os.CreateTemp(filepath.Dir(assetsTar), ".deploy.tar.gz.*.tmp")
	if err != nil {
		return err
	}
	tmpTar := out.Name()
	defer os.Remove(tmpTar) // no-op once renamed away
	// nil-guarded defer (same pattern as tw/gz): closes the file on an early error return, but the
	// explicit Close on the success path nils it so we never double-close and mask a real error.
	defer func() {
		if out != nil {
			out.Close()
		}
	}()
	gz := gzip.NewWriter(out)
	gz.ModTime = time.Time{} // don't embed the current time in the gzip header (keeps output stable)
	tw := tar.NewWriter(gz)
	// Defer closes (nil-guarded) so an early error return still flushes/closes the writers and
	// releases fds; the explicit closes below capture errors on the success path, then nil the
	// writers to avoid a double-close.
	defer func() {
		if tw != nil {
			tw.Close()
		}
		if gz != nil {
			gz.Close()
		}
	}()
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			return err
		}
		// Fixed ModTime (epoch) so identical inputs produce a byte-identical tarball — no git churn.
		hdr := &tar.Header{Name: f, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	tw = nil
	if err := gz.Close(); err != nil {
		return err
	}
	gz = nil
	if err := out.Close(); err != nil { // flush to disk before the rename
		return err
	}
	out = nil // prevent the deferred double-close
	if err := os.Rename(tmpTar, assetsTar); err != nil {
		return err
	}
	fmt.Printf("• genAssets: %d files -> %s\n", len(files), assetsTar)
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// WebUI builds the agents control UI (deploy/agents-ui) to static assets embedded by
// internal/agents (webdist). Run it after changing the UI; the output is committed so a plain
// `go build` needs no Node. Requires npm.
func WebUI() error {
	dir := filepath.Join("deploy", "agents-ui")
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		fmt.Println("• agents-ui: npm install")
		if err := sh.RunV("npm", "--prefix", dir, "install", "--no-audit", "--no-fund"); err != nil {
			return err
		}
	}
	fmt.Println("• agents-ui: vite build -> internal/agents/webdist")
	return sh.RunV("npm", "--prefix", dir, "run", "build")
}

// Vet runs go vet across the module.
func Vet() error { return sh.RunV(goBin(), "vet", "./...") }

// Test runs the Go unit tests.
func Test() error { return sh.RunV(goBin(), "test", "./...") }

// CIImage builds the warm CI job image (Go+Node, arch-matched, /var/run fix for Docker 29+).
func CiImage() error {
	fmt.Println("• docker build", ciImage)
	return sh.RunV("docker", "build", "-t", ciImage, ciCtx)
}

// E2E is the headline: build the CLI and run the full closed loop, validating every AC. The CLI
// owns the CI-image build (init builds + freshness-stamps it), so we don't pre-build it here —
// doing so would build the image WITHOUT the freshness label and force init to rebuild it (two
// Docker builds per run).
func E2e() error {
	mg.Deps(Build)
	return sh.RunV(bin, "e2e")
}

// E2EFast runs the closed loop without the Playwright UI criteria (SC-6/SC-7) — quicker.
func E2eFast() error {
	mg.Deps(Build)
	return sh.RunV(bin, "e2e", "--no-playwright")
}

// Init brings up the control plane only.
func Init() error { mg.Deps(Build); return sh.RunV(bin, "init") }

// Status prints the instance summary.
func Status() error { mg.Deps(Build); return sh.RunV(bin, "status") }

// Down tears down the control plane.
func Down() error { mg.Deps(Build); return sh.RunV(bin, "down") }

// Reset wipes instance state + credentials.
func Reset() error { mg.Deps(Build); return sh.RunV(bin, "reset") }

// Install builds and installs a self-contained sandforge binary onto your PATH (GOBIN or
// GOPATH/bin). With assets embedded, the installed binary needs neither this repo nor mage.
func Install() error {
	mg.Deps(GenAssets) // regenerate the embedded tarball first so the installed binary isn't stale
	fmt.Println("• go install", pkg, "(standalone, assets embedded)")
	return sh.RunV(goBin(), "install", pkg)
}

// Clean removes build artifacts.
func Clean() error {
	fmt.Println("• clean")
	return os.RemoveAll("bin")
}

// goBin resolves the go binary (respects GOROOT installs on PATH).
func goBin() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}
