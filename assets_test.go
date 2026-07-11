package sandforge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if filepath.Ext(name) == ".sh" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGzRoundtrip(t *testing.T) {
	data := makeTarGz(t, map[string]string{
		"deploy/a.txt":      "hello",
		"deploy/run.sh":     "#!/bin/sh\n",
		"deploy/sub/b.yaml": "k: v",
	})
	dir := t.TempDir()
	if err := extractTarGz(data, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "deploy/a.txt")); string(b) != "hello" {
		t.Errorf("a.txt = %q", b)
	}
	info, err := os.Stat(filepath.Join(dir, "deploy/run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh not executable: %v", info.Mode())
	}
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	data := makeTarGz(t, map[string]string{"../escape.txt": "evil"})
	if err := extractTarGz(data, t.TempDir()); err == nil {
		t.Fatal("expected path-traversal rejection")
	}
}

func TestAssetsHashStable(t *testing.T) {
	if len(assetsTarGz) == 0 {
		t.Skip("no embedded assets in this build")
	}
	if assetsHash() != assetsHash() {
		t.Error("assetsHash not stable")
	}
	if len(assetsHash()) != 16 {
		t.Errorf("hash len = %d", len(assetsHash()))
	}
}

func TestMaterializeIdempotent(t *testing.T) {
	if len(assetsTarGz) == 0 {
		t.Skip("no embedded assets in this build")
	}
	base := t.TempDir()
	dir1, err := Materialize(base)
	if err != nil {
		t.Fatal(err)
	}
	// The guarded load-bearing files must exist.
	for _, must := range []string{
		"deploy/tasks-app/backend/main.go",
		"deploy/tasks-app/.github/workflows/ci.yml",
		"deploy/control-plane/control-plane.compose.yml",
		"docs/prd.md",
	} {
		if _, err := os.Stat(filepath.Join(dir1, must)); err != nil {
			t.Errorf("missing materialized asset %s: %v", must, err)
		}
	}
	dir2, err := Materialize(base)
	if err != nil {
		t.Fatal(err)
	}
	if dir1 != dir2 {
		t.Errorf("non-idempotent: %s != %s", dir1, dir2)
	}
}
