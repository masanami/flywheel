package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gitadapter "github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #101（親要件チケット #98 §実行スロット・§プロバイダ worktree）の受入基準を、
// 実物の git リポジトリ・実ストア・実際の adapter（internal/adapters/git）を core へ渡す
// 組み立て（CLI が担う結線）で検証する（AC-228・278・283〜296）。偽の git／gh は PATH の
// 先頭に置く実行ファイルで、本物の claude・gh・harness は起動しない。

func slotGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	exe, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// slotClone は dir に main のコミットが 1 つある clean なクローン（origin = url）を作る。
func slotClone(t *testing.T, dir, url string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	slotGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	slotGit(t, dir, "add", "f.txt")
	slotGit(t, dir, "commit", "-q", "-m", "init")
	if url != "" {
		slotGit(t, dir, "remote", "add", "origin", url)
	}
}

// slotBinder は slot_id つきの run を（SQL で）作る。run を作る本体は委譲の起動の範囲で、
// ここではスロットの割り当てが同じトランザクションの中で run に結び付くことだけを使う。
func slotBinder(tx *sql.Tx, slotID int64) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	res, err := tx.Exec(`INSERT INTO run (kind, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket, slot_id, repo)
		VALUES ('predict', 1, 'h', ?, ?, 1, 'predict', ?, 'r')`, now, now, slotID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func openSlotStore(t *testing.T, ws string) *core.Store {
	t.Helper()
	s, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func cloneRepo(paths ...string) core.ConnectorRepo {
	return core.ConnectorRepo{Name: "r", Remote: "Owner/Name", DefaultBranch: "main",
		Slots: core.ConnectorRepoSlots{Provider: "clone", Paths: paths}}
}

// 偽の gh を PATH の先頭に置き、呼ばれたら印を残す（検査がネットワークを使わないことの確認）。
func installTripwireGH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "gh-called")
	script := "#!/bin/sh\necho called >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// AC-283〜285・M3P47: origin の URL の各形（https・scp・ssh × .git の有無）を正規化し、大文字小文字を
// 無視して宣言の remote と一致とみなす。違う名前・未コミットの変更は割り当てずに needs_attention。
func TestSlotAssignment_OriginFormsAndPreAssignmentChecks_RealGit(t *testing.T) {
	marker := installTripwireGH(t)
	forms := []string{
		"https://github.com/owner/NAME", "https://github.com/owner/NAME.git",
		"git@github.com:OWNER/name", "git@github.com:owner/name.git",
		"ssh://git@github.com/Owner/Name", "ssh://git@github.com/Owner/Name.git",
	}
	for _, url := range forms {
		t.Run(url, func(t *testing.T) {
			ws := initializedWorkspace(t)
			slotClone(t, filepath.Join(ws, "clones", "a"), url)
			s := openSlotStore(t, ws)
			a, err := s.AcquireSlot(context.Background(), gitadapter.New(), cloneRepo("clones/a"), slotBinder)
			if err != nil {
				t.Fatalf("AcquireSlot: %v", err)
			}
			if a.SlotID != "SL-1" {
				t.Errorf("assignment = %+v", a)
			}
		})
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("gh was called during the pre-assignment checks (they must not use the network)")
	}

	t.Run("origin mismatch", func(t *testing.T) {
		ws := initializedWorkspace(t)
		slotClone(t, filepath.Join(ws, "clones", "a"), "git@github.com:owner/other.git")
		s := openSlotStore(t, ws)
		_, err := s.AcquireSlot(context.Background(), gitadapter.New(), cloneRepo("clones/a"), slotBinder)
		if !errors.Is(err, core.ErrSlotUnavailable) {
			t.Fatalf("error = %v, want ErrSlotUnavailable", err)
		}
		doc := runJSON(t, ws, "status")
		slots := doc["needs_human"].(map[string]any)["slots"].([]any)
		if len(slots) != 1 {
			t.Fatalf("needs_human.slots = %v, want 1 entry", slots)
		}
	})

	t.Run("uncommitted changes", func(t *testing.T) {
		ws := initializedWorkspace(t)
		dir := filepath.Join(ws, "clones", "a")
		slotClone(t, dir, "git@github.com:owner/name.git")
		if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := openSlotStore(t, ws)
		if _, err := s.AcquireSlot(context.Background(), gitadapter.New(), cloneRepo("clones/a"), slotBinder); !errors.Is(err, core.ErrSlotUnavailable) {
			t.Fatalf("error = %v, want ErrSlotUnavailable", err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "wip.txt")); err != nil || string(data) != "wip" {
			t.Errorf("flywheel altered the slot's working tree: (%q, %v)", data, err)
		}
		slots, _ := s.ListSlots(context.Background())
		if len(slots) != 1 || slots[0].State != "needs_attention" || !strings.Contains(slots[0].AttentionReason, "uncommitted") {
			t.Errorf("slots = %+v", slots)
		}
	})
}

// AC-278: clone のパスが無いスロットは needs_attention（理由はパスが無いこと）で status に出て、
// ディレクトリは作られない。slot clear で戻せる。
func TestSlotClone_MissingPathAppearsInStatusAndClearReturnsItToIdle(t *testing.T) {
	ws := initializedWorkspace(t)
	s := openSlotStore(t, ws)
	_, err := s.AcquireSlot(context.Background(), gitadapter.New(), cloneRepo("clones/gone"), slotBinder)
	if !errors.Is(err, core.ErrSlotUnavailable) {
		t.Fatalf("error = %v, want ErrSlotUnavailable", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "clones")); err == nil {
		t.Error("flywheel created the clone's directory")
	}

	doc := runJSON(t, ws, "status")
	slots := doc["needs_human"].(map[string]any)["slots"].([]any)
	if len(slots) != 1 {
		t.Fatalf("needs_human.slots = %v", slots)
	}
	e := slots[0].(map[string]any)
	if e["slot_id"] != "SL-1" || e["repo"] != "r" || e["path"] != filepath.Join(ws, "clones", "gone") || e["run_id"] != nil || len(e) != 4 {
		t.Errorf("slot entry = %v", e)
	}
	all, _ := s.ListSlots(context.Background())
	if !strings.Contains(all[0].AttentionReason, "does not exist") {
		t.Errorf("reason = %q", all[0].AttentionReason)
	}

	// slot clear: 端末でなくても成功し（runJSON の標準入力は端末でない）、作業ログを残さない。
	out := runJSON(t, ws, "slot", "clear", "SL-1")
	slot := out["slot"].(map[string]any)
	if slot["slot_id"] != "SL-1" || slot["state"] != "idle" {
		t.Errorf("slot clear output = %v", out)
	}
	doc = runJSON(t, ws, "status")
	if got := doc["needs_human"].(map[string]any)["slots"].([]any); len(got) != 0 {
		t.Errorf("needs_human.slots after clear = %v, want []", got)
	}
	acts := runJSON(t, ws, "log")["activities"].([]any)
	if len(acts) != 0 {
		t.Errorf("activity log has %d entries, want 0", len(acts))
	}
}

func TestSlotClear_ErrorsMapToCLIErrorCodes(t *testing.T) {
	ws := initializedWorkspace(t)
	requireJSONErrorEnvelope(t, []string{"slot", "clear", "SL-9", "--workspace", ws}, 1, CodeNotFound)
	idle := coretest.InsertSlot(t, ws, "r", "clone", filepath.Join(ws, "c"), "idle", "")
	requireJSONErrorEnvelope(t, []string{"slot", "clear", fmt.Sprintf("SL-%d", idle), "--workspace", ws}, 1, CodeInvalidTransition)
	requireJSONErrorEnvelope(t, []string{"slot", "clear", "--workspace", ws}, 2, CodeUsageError)
}

func TestStatus_NeedsHumanSlotsTextShowsSlot(t *testing.T) {
	ws := initializedWorkspace(t)
	coretest.InsertSlot(t, ws, "r", "clone", filepath.Join(ws, "c"), "needs_attention", "the slot path does not exist")
	var stdout, stderr strings.Builder
	code := run([]string{"status", "--workspace", ws}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SL-1") || !strings.Contains(stdout.String(), "the slot path does not exist") {
		t.Errorf("status text = %q", stdout.String())
	}
}

// AC-288〜293: worktree の払い出し（実物の git）。count 本・初回の割り当てで作成・使い回し・
// ref が無い・パスにファイルがある場合は needs_attention。
func TestSlotWorktree_ProvisioningAndReuse_RealGit(t *testing.T) {
	ws := initializedWorkspace(t)
	base := filepath.Join(ws, "base")
	slotClone(t, base, "https://github.com/owner/name.git")
	s := openSlotStore(t, ws)
	repo := core.ConnectorRepo{Name: "r", Remote: "owner/name", DefaultBranch: "main",
		Slots: core.ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: 3}}
	ctx := context.Background()
	g := gitadapter.New()

	a, err := s.AcquireSlot(ctx, g, repo, slotBinder)
	if err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if all, _ := s.ListSlots(ctx); len(all) != 3 {
		t.Fatalf("slots = %d, want 3", len(all))
	}
	wantPath := filepath.Join(ws, ".flywheel", "worktrees", "r", "SL-1")
	if a.Path != wantPath {
		t.Fatalf("path = %q, want %q", a.Path, wantPath)
	}
	list := slotGit(t, base, "worktree", "list", "--porcelain")
	head := slotGit(t, base, "rev-parse", "refs/heads/main")
	var block string
	for _, b := range strings.Split(list, "\n\n") {
		if strings.Contains(b, "SL-1") {
			block = b
		}
	}
	if !strings.Contains(block, "HEAD "+head) || !strings.Contains(block, "detached") {
		t.Errorf("worktree list lacks a detached SL-1 at %s:\n%s", head, list)
	}

	// 2 つ目の課題は別のスロット（SL-2）を払い出す。SL-1 を解放して 3 つ目の課題に割り当てても、
	// 作り直さず同じパスを使い回す。
	a2, err := s.AcquireSlot(ctx, g, repo, slotBinder)
	if err != nil || a2.SlotID != "SL-2" {
		t.Fatalf("second AcquireSlot = (%+v, %v), want SL-2", a2, err)
	}
	endRunAndRelease(t, s, a)
	a3, err := s.AcquireSlot(ctx, g, repo, slotBinder)
	if err != nil || a3.SlotID != "SL-1" || a3.Path != wantPath {
		t.Fatalf("reused AcquireSlot = (%+v, %v), want SL-1 at %s", a3, err, wantPath)
	}
	if n := strings.Count(slotGit(t, base, "worktree", "list", "--porcelain"), "worktree "); n != 3 {
		t.Errorf("worktrees = %d (including the base), want 3", n)
	}
}

func endRunAndRelease(t *testing.T, s *core.Store, a *core.SlotAssignment) {
	t.Helper()
	coretest.EndRun(t, s.Workspace(), a.RunID)
	if err := s.ReleaseSlot(context.Background(), a.SlotID); err != nil {
		t.Fatal(err)
	}
}

func TestSlotWorktree_ProvisioningFailuresMarkNeedsAttention_RealGit(t *testing.T) {
	t.Run("default branch ref is missing", func(t *testing.T) {
		ws := initializedWorkspace(t)
		slotClone(t, filepath.Join(ws, "base"), "https://github.com/owner/name.git")
		s := openSlotStore(t, ws)
		repo := core.ConnectorRepo{Name: "r", Remote: "owner/name", DefaultBranch: "trunk",
			Slots: core.ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: 1}}
		if _, err := s.AcquireSlot(context.Background(), gitadapter.New(), repo, slotBinder); !errors.Is(err, core.ErrSlotUnavailable) {
			t.Fatalf("error = %v, want ErrSlotUnavailable", err)
		}
		all, _ := s.ListSlots(context.Background())
		if all[0].State != "needs_attention" || all[0].AttentionReason == "" {
			t.Errorf("slot = %+v", all[0])
		}
	})
	t.Run("the slot path is a file", func(t *testing.T) {
		ws := initializedWorkspace(t)
		slotClone(t, filepath.Join(ws, "base"), "https://github.com/owner/name.git")
		blocker := filepath.Join(ws, ".flywheel", "worktrees", "r", "SL-1")
		if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := openSlotStore(t, ws)
		repo := core.ConnectorRepo{Name: "r", Remote: "owner/name", DefaultBranch: "main",
			Slots: core.ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: 1}}
		if _, err := s.AcquireSlot(context.Background(), gitadapter.New(), repo, slotBinder); !errors.Is(err, core.ErrSlotUnavailable) {
			t.Fatalf("error = %v, want ErrSlotUnavailable", err)
		}
		all, _ := s.ListSlots(context.Background())
		if all[0].State != "needs_attention" || all[0].AttentionReason == "" || all[0].RunID != nil {
			t.Errorf("slot = %+v", all[0])
		}
	})
	t.Run("the .git pointer points at another worktree", func(t *testing.T) {
		ws := initializedWorkspace(t)
		base := filepath.Join(ws, "base")
		slotClone(t, base, "https://github.com/owner/name.git")
		s := openSlotStore(t, ws)
		repo := core.ConnectorRepo{Name: "r", Remote: "owner/name", DefaultBranch: "main",
			Slots: core.ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: 2}}
		ctx := context.Background()
		a, err := s.AcquireSlot(ctx, gitadapter.New(), repo, slotBinder)
		if err != nil {
			t.Fatal(err)
		}
		a2, err := s.AcquireSlot(ctx, gitadapter.New(), repo, slotBinder)
		if err != nil {
			t.Fatal(err)
		}
		endRunAndRelease(t, s, a)
		// SL-1 のポインタを SL-2 の管理ディレクトリへ向ける。
		entries, _ := os.ReadDir(filepath.Join(base, ".git", "worktrees"))
		var other string
		for _, e := range entries {
			if e.Name() == "SL-2" {
				other = filepath.Join(base, ".git", "worktrees", e.Name())
			}
		}
		if other == "" {
			t.Fatalf("no admin dir for SL-2 among %v", entries)
		}
		if err := os.WriteFile(filepath.Join(a.Path, ".git"), []byte("gitdir: "+other+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = a2
		if _, err := s.AcquireSlot(ctx, gitadapter.New(), repo, slotBinder); !errors.Is(err, core.ErrSlotUnavailable) {
			t.Fatalf("error = %v, want ErrSlotUnavailable", err)
		}
		all, _ := s.ListSlots(ctx)
		if all[0].State != "needs_attention" || !strings.Contains(all[0].AttentionReason, "pointer") {
			t.Errorf("SL-1 = %+v", all[0])
		}
	})
}

var envLineRe = regexp.MustCompile(`^GIT_DIR=\S+ GIT_WORK_TREE=\S+ ARGS=`)

// AC-294〜296: 割り当てまでの流れで flywheel が起動する git は、GIT_DIR・GIT_WORK_TREE を持ち、
// fetch・clone を実行しない（git の包みで環境変数とサブコマンドを記録して検証する）。
func TestSlotAssignment_GitCallsCarryEnvAndNeverFetchOrClone_RealGit(t *testing.T) {
	ws := initializedWorkspace(t)
	base := filepath.Join(ws, "base")
	slotClone(t, base, "https://github.com/owner/name.git")
	realGit, _ := exec.LookPath("git")
	logPath := filepath.Join(t.TempDir(), "git.log")
	wrapDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho \"GIT_DIR=$GIT_DIR GIT_WORK_TREE=$GIT_WORK_TREE ARGS=$*\" >> %q\nexec %q \"$@\"\n", logPath, realGit)
	if err := os.WriteFile(filepath.Join(wrapDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := openSlotStore(t, ws)
	repo := core.ConnectorRepo{Name: "r", Remote: "owner/name", DefaultBranch: "main",
		Slots: core.ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: 1}}
	if _, err := s.AcquireSlot(context.Background(), gitadapter.New(), repo, slotBinder); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatalf("recorded %d git calls: %q", len(lines), lines)
	}
	for _, l := range lines {
		_, args, _ := strings.Cut(l, " ARGS=")
		if !envLineRe.MatchString(l) {
			t.Errorf("git %s ran without GIT_DIR/GIT_WORK_TREE: %s", args, l)
		}
		switch strings.Fields(args)[0] {
		case "fetch", "clone", "pull", "reset", "clean", "checkout":
			t.Errorf("forbidden subcommand: %s", l)
		}
	}
}
