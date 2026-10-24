package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var memNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// memWS は t.TempDir() のワークスペースと、その中の置き場 memory/harness（作成済み）を返す。
func memWS(t *testing.T) (ws, root string, decl *AgentDeclaration) {
	t.Helper()
	ws = t.TempDir()
	root = filepath.Join(ws, "memory", "harness")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	decl = defaultAgentDeclaration()
	decl.MemoryDir = "memory/harness"
	return
}

func tacit(slug string) memoryItem {
	return memoryItem{Mode: "create", Type: "tacit", Slug: slug, Description: "説明 " + slug,
		Confidence: "high", Sources: []string{"src one", "src: two"}, Body: "本文です"}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// dirSnapshot は dir 配下の全ファイルの（相対パス→内容）を返す。
func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if fi.IsDir() {
			m[rel+"/"] = ""
		} else if fi.Mode().IsRegular() {
			m[rel] = readFile(t, p)
		}
		return nil
	})
	return m
}

func equalSnap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func assertRejectedNothingWritten(t *testing.T, ws string, decl *AgentDeclaration, items []memoryItem) {
	t.Helper()
	before := dirSnapshot(t, ws)
	got, err := writeMemory(ws, decl, items, memNow)
	if !errors.Is(err, ErrMemoryWriteRejected) {
		t.Fatalf("err = %v, want ErrMemoryWriteRejected", err)
	}
	if got != nil {
		t.Errorf("written = %v, want nil", got)
	}
	if after := dirSnapshot(t, ws); !equalSnap(before, after) {
		t.Errorf("workspace changed\nbefore=%v\nafter=%v", before, after)
	}
}

func TestWriteMemory_CreateWritesFrontmatterIndexAndHash(t *testing.T) {
	ws, root, decl := memWS(t)
	got, err := writeMemory(ws, decl, []memoryItem{tacit("alpha")}, memNow)
	if err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(root, "tacit-alpha.md"))
	want := "---\nname: alpha\ndescription: 説明 alpha\ndomain: harness\nmetadata:\n  type: tacit\n  confidence: high\n" +
		"  sources: [\"src one\", \"src: two\"]\n---\n\n本文です\n"
	if content != want {
		t.Errorf("content =\n%q\nwant\n%q", content, want)
	}
	for _, k := range []string{"reflected", "applied_as", "recurrence"} {
		if strings.Contains(content, k) {
			t.Errorf("content has legacy key %s", k)
		}
	}
	if len(got) != 1 || got[0] != (memoryWritten{Path: "tacit-alpha.md", Mode: "create", SHA256: sha(want)}) {
		t.Errorf("written = %+v", got)
	}
	idx := readFile(t, filepath.Join(root, "INDEX.md"))
	if idx != "# memory/harness INDEX\n\n- [alpha] tacit/high — 説明 alpha\n" {
		t.Errorf("INDEX = %q", idx)
	}
}

func TestWriteMemory_ExperienceFrontmatter(t *testing.T) {
	ws, root, decl := memWS(t)
	it := memoryItem{Mode: "create", Type: "experience", Slug: "exp1", Description: "d", Confidence: "medium",
		Outcome: "good", Target: "brief", Signal: "2026-10-24: ok", Body: "b\n\n"}
	if _, err := writeMemory(ws, decl, []memoryItem{it}, memNow); err != nil {
		t.Fatal(err)
	}
	c := readFile(t, filepath.Join(root, "experience-exp1.md"))
	for _, w := range []string{"  type: experience\n  outcome: good\n  target: brief\n  signal: \"2026-10-24: ok\"\n  confidence: medium\n  sources: []\n", "domain: harness\n"} {
		if !strings.Contains(c, w) {
			t.Errorf("missing %q in\n%s", w, c)
		}
	}
	if !strings.HasSuffix(c, "---\n\nb\n") {
		t.Errorf("body not normalized: %q", c)
	}
}

func TestWriteMemory_ExistingIndexAndLegacyLinesUntouched(t *testing.T) {
	ws, root, decl := memWS(t)
	legacy := "# memory/harness INDEX\n\n- [old] experience/high (recurrence 4, resolved 2026-08-30) — x"
	idxPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(idxPath, []byte(legacy), 0o644); err != nil { // 末尾に改行なし
		t.Fatal(err)
	}
	if _, err := writeMemory(ws, decl, []memoryItem{tacit("alpha"), tacit("beta")}, memNow); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, idxPath)
	want := legacy + "\n- [alpha] tacit/high — 説明 alpha\n- [beta] tacit/high — 説明 beta\n"
	if got != want {
		t.Errorf("INDEX = %q\nwant %q", got, want)
	}
}

func TestWriteMemory_AppendKeepsFrontmatterAndLegacyKeys(t *testing.T) {
	ws, root, decl := memWS(t)
	orig := "---\nname: old\ndescription: d\nmetadata:\n  type: experience\n  recurrence: 3\n  reflected: 2026-09-23\n  applied_as: x\n---\n\n既存の本文"
	p := filepath.Join(root, "experience-old.md")
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil { // 末尾に改行なし
		t.Fatal(err)
	}
	idx := "- [old] experience/high — d\n"
	if err := os.WriteFile(filepath.Join(root, "INDEX.md"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	it := memoryItem{Mode: "append", Type: "experience", Slug: "old", Body: "追記です"}
	got, err := writeMemory(ws, decl, []memoryItem{it}, memNow)
	if err != nil {
		t.Fatal(err)
	}
	section := "\n\n## 2026-10-24\n\n追記です\n"
	if c := readFile(t, p); c != orig+section {
		t.Errorf("content = %q", c)
	}
	if readFile(t, filepath.Join(root, "INDEX.md")) != idx {
		t.Error("INDEX changed by append")
	}
	if len(got) != 1 || got[0] != (memoryWritten{Path: "experience-old.md", Mode: "append", SHA256: sha(section)}) {
		t.Errorf("written = %+v", got)
	}
}

func TestWriteMemory_CreateThenAppendSameFileInOneOutput(t *testing.T) {
	ws, root, decl := memWS(t)
	app := memoryItem{Mode: "append", Type: "tacit", Slug: "alpha", Body: "next"}
	got, err := writeMemory(ws, decl, []memoryItem{tacit("alpha"), app}, memNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("written = %+v", got)
	}
	if !strings.HasSuffix(readFile(t, filepath.Join(root, "tacit-alpha.md")), "本文です\n\n## 2026-10-24\n\nnext\n") {
		t.Error("append after create not applied")
	}
}

func TestWriteMemory_RejectsAndWritesNothing(t *testing.T) {
	existing := func(t *testing.T, root string) {
		if err := os.WriteFile(filepath.Join(root, "tacit-dup.md"), []byte("orig"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exp := func(mut func(*memoryItem)) memoryItem {
		it := memoryItem{Mode: "create", Type: "experience", Slug: "e", Description: "d", Confidence: "high", Outcome: "good", Target: "t", Signal: "s", Body: "b"}
		mut(&it)
		return it
	}
	with := func(mut func(*memoryItem)) memoryItem { it := tacit("x"); mut(&it); return it }
	cases := map[string]struct {
		pre   func(t *testing.T, root string)
		items []memoryItem
	}{
		"type-outside":       {items: []memoryItem{with(func(i *memoryItem) { i.Type = "note" })}},
		"type-empty":         {items: []memoryItem{with(func(i *memoryItem) { i.Type = "" })}},
		"mode-overwrite":     {items: []memoryItem{with(func(i *memoryItem) { i.Mode = "overwrite" })}},
		"mode-delete":        {items: []memoryItem{with(func(i *memoryItem) { i.Mode = "delete" })}},
		"mode-rename":        {items: []memoryItem{with(func(i *memoryItem) { i.Mode = "rename" })}},
		"mode-empty":         {items: []memoryItem{with(func(i *memoryItem) { i.Mode = "" })}},
		"experience-outcome": {items: []memoryItem{exp(func(i *memoryItem) { i.Outcome = "" })}},
		"experience-target":  {items: []memoryItem{exp(func(i *memoryItem) { i.Target = "" })}},
		"experience-signal":  {items: []memoryItem{exp(func(i *memoryItem) { i.Signal = " " })}},
		"create-duplicate":   {pre: existing, items: []memoryItem{with(func(i *memoryItem) { i.Slug = "dup" })}},
		"create-twice":       {items: []memoryItem{tacit("a"), tacit("a")}},
		"append-missing":     {items: []memoryItem{{Mode: "append", Type: "tacit", Slug: "nope", Body: "b"}}},
		"slug-absolute":      {items: []memoryItem{with(func(i *memoryItem) { i.Slug = "/etc/passwd" })}},
		"slug-dotdot":        {items: []memoryItem{with(func(i *memoryItem) { i.Slug = ".." })}},
		"slug-dotdot-in":     {items: []memoryItem{with(func(i *memoryItem) { i.Slug = "a..b" })}},
		"slug-slash":         {items: []memoryItem{with(func(i *memoryItem) { i.Slug = "a/b" })}},
		"slug-backslash":     {items: []memoryItem{with(func(i *memoryItem) { i.Slug = `a\b` })}},
		"slug-empty":         {items: []memoryItem{with(func(i *memoryItem) { i.Slug = "" })}},
		"slug-space":         {items: []memoryItem{with(func(i *memoryItem) { i.Slug = "a b" })}},
		"description-empty":  {items: []memoryItem{with(func(i *memoryItem) { i.Description = "" })}},
		"description-nl":     {items: []memoryItem{with(func(i *memoryItem) { i.Description = "a\nb" })}},
		"confidence-empty":   {items: []memoryItem{with(func(i *memoryItem) { i.Confidence = "" })}},
		"body-empty":         {items: []memoryItem{with(func(i *memoryItem) { i.Body = " \n" })}},
		"files-over":         {items: []memoryItem{tacit("a"), tacit("b"), tacit("c"), tacit("d"), tacit("e"), tacit("f")}},
		"bytes-create-over":  {items: []memoryItem{with(func(i *memoryItem) { i.Body = strings.Repeat("a", 8200) })}},
		"later-item-invalid": {items: []memoryItem{tacit("ok"), with(func(i *memoryItem) { i.Type = "bad" })}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ws, root, decl := memWS(t)
			if c.pre != nil {
				c.pre(t, root)
			}
			assertRejectedNothingWritten(t, ws, decl, c.items)
		})
	}
}

func TestWriteMemory_Limits(t *testing.T) {
	ws, root, decl := memWS(t)
	// ちょうど 5 件は通る。
	var five []memoryItem
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		five = append(five, tacit(s))
	}
	if _, err := writeMemory(ws, decl, five, memNow); err != nil {
		t.Fatalf("5 items: %v", err)
	}
	// 作成のバイト数の境界: ファイル全体の長さがちょうど上限なら通り、1 バイト超えれば拒否。
	fileLen := len(memoryFileContent(tacit("fit"), "harness"))
	decl.MemoryWriteLimit.Bytes = fileLen
	if _, err := writeMemory(ws, decl, []memoryItem{tacit("fit")}, memNow); err != nil {
		t.Fatalf("exact bytes: %v", err)
	}
	over := tacit("fit2")
	over.Body += "x"
	over.Slug = "fit"
	over.Slug = "fit" + "2"
	decl.MemoryWriteLimit.Bytes = len(memoryFileContent(over, "harness")) - 1
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{over})
	// 追記は足す分で数える（既存のファイルが大きくても通る）。
	big := strings.Repeat("z", 50)
	if err := os.WriteFile(filepath.Join(root, "tacit-big.md"), []byte(strings.Repeat("y", 100000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := memoryItem{Mode: "append", Type: "tacit", Slug: "big", Body: big}
	decl.MemoryWriteLimit.Bytes = len(memoryAppendSection(app, "2026-10-24", false))
	if _, err := writeMemory(ws, decl, []memoryItem{app}, memNow); err != nil {
		t.Fatalf("append exact bytes: %v", err)
	}
	app.Body += "z"
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{app})
	// files の上限は宣言の値に従う。
	decl.MemoryWriteLimit = MemoryWriteLimit{Files: 1, Bytes: 8192}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("p"), tacit("q")})
}

func TestWriteMemory_NoIndexCreatesIndex_NoRootCreatesRoot(t *testing.T) {
	ws := t.TempDir()
	decl := defaultAgentDeclaration()
	decl.MemoryDir = "memory/newdomain"
	if memoryIndexPath(ws, decl) != "" {
		t.Error("memoryIndexPath before write should be empty")
	}
	if _, err := writeMemory(ws, decl, []memoryItem{tacit("a")}, memNow); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(ws, "memory", "newdomain", "tacit-a.md")), "domain: newdomain\n") {
		t.Error("domain not newdomain")
	}
	if got := memoryIndexPath(ws, decl); got != filepath.Join(ws, "memory", "newdomain", "INDEX.md") {
		t.Errorf("memoryIndexPath = %q", got)
	}
}

func TestWriteMemory_DefaultDirWhenDeclNil(t *testing.T) {
	ws := t.TempDir()
	if _, err := writeMemory(ws, nil, []memoryItem{tacit("a")}, memNow); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "memory", "tacit-a.md")); err != nil {
		t.Error(err)
	}
}

func TestWriteMemory_EmptyItemsWritesNothing(t *testing.T) {
	ws, _, decl := memWS(t)
	before := dirSnapshot(t, ws)
	got, err := writeMemory(ws, decl, nil, memNow)
	if err != nil || got != nil {
		t.Fatalf("got %v err %v", got, err)
	}
	if !equalSnap(before, dirSnapshot(t, ws)) {
		t.Error("changed")
	}
}

func TestWriteMemory_SymlinkedAppendTargetOutsideRejected(t *testing.T) {
	ws, root, decl := memWS(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tacit-link.md")); err != nil {
		t.Fatal(err)
	}
	it := memoryItem{Mode: "append", Type: "tacit", Slug: "link", Body: "evil"}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{it})
	if readFile(t, outside) != "secret\n" {
		t.Error("link target changed")
	}
}

func TestWriteMemory_SymlinkedAppendTargetInsideAllowed(t *testing.T) {
	ws, root, decl := memWS(t)
	realPath := filepath.Join(root, "tacit-realPath.md")
	if err := os.WriteFile(realPath, []byte("r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, filepath.Join(root, "tacit-link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMemory(ws, decl, []memoryItem{{Mode: "append", Type: "tacit", Slug: "link", Body: "ok"}}, memNow); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(readFile(t, realPath), "## 2026-10-24\n\nok\n") {
		t.Error("not appended")
	}
}

func TestWriteMemory_CreateOverDanglingSymlinkRejected(t *testing.T) {
	ws, root, decl := memWS(t)
	outside := filepath.Join(t.TempDir(), "new.md")
	if err := os.Symlink(outside, filepath.Join(root, "tacit-x.md")); err != nil {
		t.Fatal(err)
	}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("x")})
	if _, err := os.Lstat(outside); err == nil {
		t.Error("file created through dangling link")
	}
}

func TestWriteMemory_IndexSymlinkOutsideRejected(t *testing.T) {
	ws, root, decl := memWS(t)
	outside := filepath.Join(t.TempDir(), "INDEX.md")
	if err := os.WriteFile(outside, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "INDEX.md")); err != nil {
		t.Fatal(err)
	}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("a")})
	if readFile(t, outside) != "x\n" {
		t.Error("outside index changed")
	}
	if memoryIndexPath(ws, decl) != "" {
		t.Error("memoryIndexPath should not return an outside index")
	}
}

func TestWriteMemory_RootSymlinkOutsideRejected(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "memory", "harness")); err != nil {
		t.Fatal(err)
	}
	decl := defaultAgentDeclaration()
	decl.MemoryDir = "memory/harness"
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("a")})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("outside dir has %d entries", len(entries))
	}
	if memoryIndexPath(ws, decl) != "" {
		t.Error("memoryIndexPath should be empty for an escaping root")
	}
}

func TestWriteMemory_RootParentSymlinkOutsideRejectedEvenWhenRootMissing(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "memory")); err != nil {
		t.Fatal(err)
	}
	decl := defaultAgentDeclaration()
	decl.MemoryDir = "memory/harness"
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("a")})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("outside dir has %d entries", len(entries))
	}
}

func TestWriteMemory_FailureMidwayRollsBackEverything(t *testing.T) {
	for failAt := 0; failAt < 4; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			ws, root, decl := memWS(t)
			orig := "---\nname: o\n---\n\nbody\n"
			if err := os.WriteFile(filepath.Join(root, "tacit-o.md"), []byte(orig), 0o644); err != nil {
				t.Fatal(err)
			}
			idx := "- [o] tacit/high — d"
			if err := os.WriteFile(filepath.Join(root, "INDEX.md"), []byte(idx), 0o644); err != nil {
				t.Fatal(err)
			}
			before := dirSnapshot(t, ws)
			items := []memoryItem{tacit("a"), {Mode: "append", Type: "tacit", Slug: "o", Body: "more"}, tacit("b")}
			boom := errors.New("injected")
			memoryWriteFault = func(n int) error {
				if n == failAt {
					return boom
				}
				return nil
			}
			t.Cleanup(func() { memoryWriteFault = nil })
			got, err := writeMemory(ws, decl, items, memNow)
			if !errors.Is(err, boom) || errors.Is(err, ErrMemoryWriteRejected) {
				t.Fatalf("err = %v", err)
			}
			if got != nil {
				t.Errorf("written = %v", got)
			}
			if after := dirSnapshot(t, ws); !equalSnap(before, after) {
				t.Errorf("not restored\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestWriteMemory_FailureRemovesCreatedRootAndIndex(t *testing.T) {
	ws := t.TempDir()
	decl := defaultAgentDeclaration()
	decl.MemoryDir = "memory/harness"
	before := dirSnapshot(t, ws)
	memoryWriteFault = func(n int) error {
		if n == 2 {
			return errors.New("injected")
		}
		return nil
	}
	t.Cleanup(func() { memoryWriteFault = nil })
	if _, err := writeMemory(ws, decl, []memoryItem{tacit("a"), tacit("b")}, memNow); err == nil {
		t.Fatal("want error")
	}
	if after := dirSnapshot(t, ws); !equalSnap(before, after) {
		t.Errorf("not restored: %v", after)
	}
}

func TestWriteMemory_DoesNotTouchGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws, _, decl := memWS(t)
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "init")
	head := run("rev-parse", "HEAD")
	staged := run("diff", "--cached", "--name-only")
	if _, err := writeMemory(ws, decl, []memoryItem{tacit("a")}, memNow); err != nil {
		t.Fatal(err)
	}
	if run("rev-parse", "HEAD") != head || run("diff", "--cached", "--name-only") != staged {
		t.Error("git commit or index changed")
	}
	if !strings.Contains(run("status", "--porcelain"), "memory/") {
		t.Error("new file should be untracked")
	}
}

func TestMemoryIndexPath(t *testing.T) {
	ws, root, decl := memWS(t)
	if got := memoryIndexPath(ws, decl); got != "" {
		t.Errorf("no index: %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "INDEX.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws, "memory/harness", "INDEX.md")
	if got := memoryIndexPath(ws, decl); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// 宣言が無いときの既定は memory。
	if got := memoryIndexPath(ws, nil); got != "" {
		t.Errorf("default dir without index: %q", got)
	}
}

func TestYAMLScalar(t *testing.T) {
	cases := map[string]string{
		"plain text":       "plain text",
		"日本語の説明":           "日本語の説明",
		"a: b":             `"a: b"`,
		"- dash":           `"- dash"`,
		"true":             `"true"`,
		"123":              `"123"`,
		"":                 `""`,
		"x #c":             `"x #c"`,
		"line1\nline2":     `"line1\nline2"`,
		"has \"quote\" in": `has "quote" in`,
		"<tag>&":           `<tag>&`,
		"[list]":           `"[list]"`,
	}
	for in, want := range cases {
		if got := yamlScalar(in); got != want {
			t.Errorf("yamlScalar(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestWriteMemory_SymlinkInsideWorkspaceButOutsideMemoryDirRejected(t *testing.T) {
	ws, root, decl := memWS(t)
	other := filepath.Join(ws, "other.md")
	if err := os.WriteFile(other, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "tacit-link.md")); err != nil {
		t.Fatal(err)
	}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{{Mode: "append", Type: "tacit", Slug: "link", Body: "x"}})
	_ = os.Remove(filepath.Join(root, "tacit-link.md"))
	if err := os.Symlink(other, filepath.Join(root, "INDEX.md")); err != nil {
		t.Fatal(err)
	}
	assertRejectedNothingWritten(t, ws, decl, []memoryItem{tacit("a")})
	if readFile(t, other) != "keep\n" {
		t.Error("other.md changed")
	}
}
