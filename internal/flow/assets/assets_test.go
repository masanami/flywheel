package assets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func sample() fstest.MapFS {
	return fstest.MapFS{
		"README.md":                  {Data: []byte("placeholder")},
		"workflows/ticket.yaml":      {Data: []byte("schema: harness.workflow/v1\n")},
		"workflows/prompts/a.md":     {Data: []byte("prompt")},
		"workflows/scripts/tool.sh":  {Data: []byte("#!/bin/bash\n")},
		"workflows/schemas/out.json": {Data: []byte("{}\n")},
	}
}

func TestEnsureExtractsOnceAndReuses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime", "1.0.0")
	l, err := Ensure(sample(), root)
	if err != nil {
		t.Fatal(err)
	}
	if l.WorkflowDir != filepath.Join(root, "workflows") || l.Root != root {
		t.Fatalf("layout = %+v", l)
	}
	for p, want := range map[string]os.FileMode{
		"workflows/ticket.yaml":      0o444,
		"workflows/prompts/a.md":     0o444,
		"workflows/schemas/out.json": 0o444,
		"workflows/scripts/tool.sh":  0o555,
	} {
		fi, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	// files/ の直下の README（Dirs の外）は展開しない。
	if _, err := os.Stat(filepath.Join(root, "README.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("README.md outside Dirs was extracted: %v", err)
	}
	digest, _ := Digest(sample())
	if b, _ := os.ReadFile(filepath.Join(root, MarkerFile)); strings.TrimSpace(string(b)) != digest {
		t.Errorf("marker %q, want %q", b, digest)
	}
	before, _ := os.Stat(filepath.Join(root, "workflows", "ticket.yaml"))
	// 展開済みなら何も書かない: 親を書き込み不可にしても成功する（再展開なら一時ディレクトリを作れず失敗する）。
	parent := filepath.Dir(root)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0o755) }()
	if _, err := Ensure(sample(), root); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(root, "workflows", "ticket.yaml"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("the extracted file was rewritten")
	}
	_ = os.Chmod(parent, 0o755)
	entries, _ := os.ReadDir(filepath.Dir(root))
	if len(entries) != 1 {
		t.Errorf("leftovers next to the extracted directory: %v", entries)
	}
}

func TestEnsureRefusesDifferentContents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "1.0.0")
	if _, err := Ensure(sample(), root); err != nil {
		t.Fatal(err)
	}
	other := sample()
	other["workflows/ticket.yaml"] = &fstest.MapFile{Data: []byte("schema: harness.workflow/v1\nid: changed\n")}
	_, err := Ensure(other, root)
	if err == nil || !strings.Contains(err.Error(), "not overwritten") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "workflows", "ticket.yaml")); strings.Contains(string(b), "changed") {
		t.Fatal("the extracted definition was overwritten")
	}
	bare := filepath.Join(t.TempDir(), "1.0.0")
	if err := os.MkdirAll(filepath.Join(bare, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(sample(), bare); err == nil || !strings.Contains(err.Error(), MarkerFile) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureWithoutEmbeddedFiles(t *testing.T) {
	fsys := fstest.MapFS{"README.md": {Data: []byte("placeholder")}}
	if Available(fsys) {
		t.Fatal("a bundle without workflows must not be available")
	}
	if _, err := Ensure(fsys, filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrNotEmbedded) {
		t.Fatalf("err = %v", err)
	}
}

func TestDigestChangesWithPathAndContent(t *testing.T) {
	a, _ := Digest(sample())
	moved := sample()
	moved["workflows/scripts/renamed.sh"] = moved["workflows/scripts/tool.sh"]
	delete(moved, "workflows/scripts/tool.sh")
	b, _ := Digest(moved)
	edited := sample()
	edited["workflows/scripts/tool.sh"] = &fstest.MapFile{Data: []byte("#!/bin/bash\necho\n")}
	c, _ := Digest(edited)
	readme := sample()
	readme["README.md"] = &fstest.MapFile{Data: []byte("other")}
	d, _ := Digest(readme)
	if a == b || a == c || a != d {
		t.Fatalf("digests: %s %s %s %s", a, b, c, d)
	}
}

func TestRuntimeRootIgnoresEnvironment(t *testing.T) {
	for _, k := range []string{"HARNESS_STATE_DIR", "HARNESS_DATA_DIR", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, "/elsewhere")
	}
	want := filepath.Join("/ws", ".flywheel", "flow", "runtime", "abc123")
	if got := RuntimeRoot("/ws", "abc123"); got != want {
		t.Fatalf("RuntimeRoot = %q, want %q", got, want)
	}
}

// AC-4: 埋め込んだ資材のファイルごとの SHA-256 が、写した先に置いた一覧と一致する。
func TestEmbeddedMaterialsMatchSums(t *testing.T) {
	if !Available(FS) {
		t.Fatal("no embedded materials")
	}
	if err := CheckSums(FS, Sums()); err != nil {
		t.Fatal(err)
	}
}

// AC-5: 一覧に無い資材のファイルがあると落ちる（一覧にあって無い・中身が違う場合も落ちる）。
func TestCheckSumsFailsOnFileNotListed(t *testing.T) {
	embeddedCopy := fstest.MapFS{}
	names, err := Files(FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		b, _ := fs.ReadFile(FS, n)
		embeddedCopy[n] = &fstest.MapFile{Data: b}
	}
	extra := cloneFS(embeddedCopy)
	extra["workflows/scripts/extra.sh"] = &fstest.MapFile{Data: []byte("#!/bin/bash\n")}
	if err := CheckSums(extra, Sums()); err == nil || !strings.Contains(err.Error(), "workflows/scripts/extra.sh is not in SHA256SUMS") {
		t.Fatalf("err = %v", err)
	}
	missing := cloneFS(embeddedCopy)
	delete(missing, "workflows/ticket.yaml")
	if err := CheckSums(missing, Sums()); err == nil || !strings.Contains(err.Error(), "workflows/ticket.yaml is in SHA256SUMS but not embedded") {
		t.Fatalf("err = %v", err)
	}
	edited := cloneFS(embeddedCopy)
	edited["workflows/ticket.yaml"] = &fstest.MapFile{Data: []byte("changed")}
	if err := CheckSums(edited, Sums()); err == nil || !strings.Contains(err.Error(), "workflows/ticket.yaml differs") {
		t.Fatalf("err = %v", err)
	}
}

func cloneFS(m fstest.MapFS) fstest.MapFS {
	c := fstest.MapFS{}
	for k, v := range m {
		c[k] = v
	}
	return c
}
