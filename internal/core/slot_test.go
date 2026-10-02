package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは #101（親要件チケット #98 §実行スロット・§プロバイダ worktree）の
// スロットの規則を、実ストア（t.TempDir() の SQLite）と SlotGit の偽の実装で検証する
// （AC-228・278・283〜293 の core 側。git の実物を使う検証は internal/adapters/git と
// internal/cli の結線のテストが行う）。

// fakeSlotGit は SlotGit の偽の実装。パスごとの状態と払い出しの結果を持ち、呼び出しを記録する。
type fakeSlotGit struct {
	mu      sync.Mutex
	states  map[string]SlotTreeState
	errs    map[string]error // Inspect が返すエラー（パスごと）
	addErr  map[string]error // EnsureWorktree が返すエラー（パスごと）
	added   []string         // EnsureWorktree が実際に作ったパス
	ensured []WorktreeRequest
	// EnsureWorktree が成功したとき、そのパスの状態をこの値にする。
	provisioned SlotTreeState
}

func newFakeSlotGit() *fakeSlotGit {
	return &fakeSlotGit{
		states: map[string]SlotTreeState{}, errs: map[string]error{}, addErr: map[string]error{},
		provisioned: SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/r.git"},
	}
}

func okState() SlotTreeState {
	return SlotTreeState{Exists: true, PointerOK: true, OriginURL: "git@github.com:O/R.git"}
}

func (f *fakeSlotGit) Inspect(_ context.Context, tree SlotTree) (SlotTreeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[tree.Path]; err != nil {
		return SlotTreeState{}, err
	}
	return f.states[tree.Path], nil
}

func (f *fakeSlotGit) EnsureWorktree(_ context.Context, req WorktreeRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, req)
	if err := f.addErr[req.Path]; err != nil {
		return false, err
	}
	if st, ok := f.states[req.Path]; ok && st.Exists {
		return false, nil
	}
	f.states[req.Path] = f.provisioned
	f.added = append(f.added, req.Path)
	return true, nil
}

func cloneRepoForTest(paths ...string) ConnectorRepo {
	return ConnectorRepo{Name: "r", Remote: "o/r", DefaultBranch: "main", Slots: ConnectorRepoSlots{Provider: "clone", Paths: paths}}
}

func worktreeRepoForTest(count int) ConnectorRepo {
	return ConnectorRepo{Name: "r", Remote: "o/r", DefaultBranch: "main", Slots: ConnectorRepoSlots{Provider: "worktree", Base: "base", Count: count}}
}

// bindRunForTest は predict の run を slot_id 付きで作る SlotBinder。
func bindRunForTest(ctx context.Context) SlotBinder {
	return func(tx *sql.Tx, slotID int64) (int64, error) {
		now := time.Now()
		r, err := insertRun(ctx, tx, 0, insertRunInput{
			Kind: runKindPredict, PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: 1, BudgetBucket: budgetBucketPredict, SlotID: &slotID, Repo: "r",
		})
		if err != nil {
			return 0, err
		}
		id, _ := parseRunID(r.ID)
		return id, nil
	}
}

// releaseForTest は a の run を終了させてから（run の終了が先。idx_run_active_slot は
// 終了していない run だけを数える）スロットを解放する。
func releaseForTest(t *testing.T, s *Store, a *SlotAssignment) {
	t.Helper()
	ctx := context.Background()
	runID, _ := parseRunID(a.RunID)
	if err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE run SET result = 'succeeded', ended_at = ? WHERE id = ?`, formatTimestamp(time.Now()), runID)
		return err
	}); err != nil {
		t.Fatalf("end run: %v", err)
	}
	if err := s.ReleaseSlot(ctx, a.SlotID); err != nil {
		t.Fatalf("ReleaseSlot: %v", err)
	}
}

func slotByID(t *testing.T, s *Store, id string) Slot {
	t.Helper()
	all, err := s.ListSlots(context.Background())
	if err != nil {
		t.Fatalf("ListSlots: %v", err)
	}
	for _, sl := range all {
		if sl.ID == id {
			return sl
		}
	}
	t.Fatalf("slot %s not found in %v", id, all)
	return Slot{}
}

func TestNormalizeRemoteURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://github.com/Owner/Name", "Owner/Name", true},
		{"https://github.com/Owner/Name.git", "Owner/Name", true},
		{"git@github.com:Owner/Name", "Owner/Name", true},
		{"git@github.com:Owner/Name.git", "Owner/Name", true},
		{"ssh://git@github.com/Owner/Name", "Owner/Name", true},
		{"ssh://git@github.com/Owner/Name.git", "Owner/Name", true},
		{"ssh://git@github.com:22/Owner/Name.git", "Owner/Name", true},
		{"https://github.com/Owner/Name/", "Owner/Name", true},
		{"", "", false},
		{"/local/path/repo", "", false},
		{"https://github.com/onlyowner", "", false},
		{"https://github.com/a/b/c", "", false},
	} {
		got, ok := normalizeRemoteURL(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeRemoteURL(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if !remoteMatches("git@github.com:O/R.git", "o/r") {
		t.Error("remoteMatches should ignore case")
	}
	if remoteMatches("git@github.com:o/other.git", "o/r") {
		t.Error("remoteMatches matched a different name")
	}
}

// AC-278・M3P45: clone のパスが無いと needs_attention（理由はパスが無いこと）になり、
// ディレクトリを作らない。使えるスロットが 0 本なら ErrSlotUnavailable。
func TestAcquireSlot_ClonePathMissingMarksNeedsAttentionAndUnavailable(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit() // 状態を持たないパス = Exists:false
	repo := cloneRepoForTest("clones/a")

	_, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("AcquireSlot error = %v, want ErrSlotUnavailable", err)
	}
	sl := slotByID(t, s, "SL-1")
	if sl.State != "needs_attention" || sl.AttentionReason == "" {
		t.Fatalf("slot = %+v, want needs_attention with a reason", sl)
	}
	if want := filepath.Join(s.Workspace(), "clones/a"); sl.Path != want {
		t.Errorf("slot path = %q, want %q", sl.Path, want)
	}
	if exists, _ := pathExists(filepath.Join(s.Workspace(), "clones")); exists {
		t.Error("core created the clone directory")
	}

	ov, err := s.GetOverview(ctx)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(ov.NeedsHumanSlots) != 1 || ov.NeedsHumanSlots[0].SlotID != "SL-1" || ov.NeedsHumanSlots[0].Repo != "r" || ov.NeedsHumanSlots[0].Path != sl.Path {
		t.Errorf("NeedsHumanSlots = %+v, want SL-1", ov.NeedsHumanSlots)
	}
}

func TestAcquireSlot_ChecksBeforeAssignment(t *testing.T) {
	cases := []struct {
		name  string
		state SlotTreeState
		want  string // attention_reason に含まれる語
	}{
		{"uncommitted changes", SlotTreeState{Exists: true, PointerOK: true, Dirty: true, OriginURL: "git@github.com:o/r.git"}, "uncommitted"},
		{"origin mismatch", SlotTreeState{Exists: true, PointerOK: true, OriginURL: "git@github.com:o/other.git"}, "declared remote"},
		{"no origin", SlotTreeState{Exists: true, PointerOK: true}, "declared remote"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			ctx := context.Background()
			repo := cloneRepoForTest("c")
			g := newFakeSlotGit()
			g.states[filepath.Join(s.Workspace(), "c")] = tc.state
			if _, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
				t.Fatalf("error = %v, want ErrSlotUnavailable", err)
			}
			sl := slotByID(t, s, "SL-1")
			if sl.State != "needs_attention" || !contains(sl.AttentionReason, tc.want) {
				t.Errorf("slot = %+v, want needs_attention mentioning %q", sl, tc.want)
			}
			if sl.RunID != nil {
				t.Errorf("run_id = %v, want nil (not assigned)", *sl.RunID)
			}
		})
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	return err == nil, nil
}

// 全 URL 形（https・scp・ssh、`.git` の有無）× 大文字小文字違いで割り当てられる（AC-284・285）。
func TestAcquireSlot_AcceptsEveryOriginURLForm(t *testing.T) {
	forms := []string{
		"https://github.com/O/R", "https://github.com/O/R.git",
		"git@github.com:O/R", "git@github.com:O/R.git",
		"ssh://git@github.com/O/R", "ssh://git@github.com/O/R.git",
	}
	for _, url := range forms {
		t.Run(url, func(t *testing.T) {
			s := newStoreForTest(t)
			ctx := context.Background()
			g := newFakeSlotGit()
			g.states[filepath.Join(s.Workspace(), "c")] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: url}
			a, err := s.AcquireSlot(ctx, g, cloneRepoForTest("c"), bindRunForTest(ctx))
			if err != nil {
				t.Fatalf("AcquireSlot: %v", err)
			}
			if a.SlotID != "SL-1" || slotByID(t, s, "SL-1").State != "busy" {
				t.Errorf("assignment = %+v, slot = %+v", a, slotByID(t, s, "SL-1"))
			}
		})
	}
}

// 失敗したスロットは needs_attention にして次の idle のスロットを試す。needs_attention は選ばない。
func TestAcquireSlot_SkipsFailedSlotAndNeverReassignsNeedsAttentionUntilCleared(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	repo := cloneRepoForTest("a", "b")
	g := newFakeSlotGit()
	pa, pb := filepath.Join(s.Workspace(), "a"), filepath.Join(s.Workspace(), "b")
	g.states[pa] = SlotTreeState{Exists: true, PointerOK: true, Dirty: true, OriginURL: "git@github.com:o/r.git"}
	g.states[pb] = okState()

	a, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if a.SlotID != "SL-2" || a.Path != pb {
		t.Fatalf("assignment = %+v, want SL-2", a)
	}
	if st := slotByID(t, s, "SL-1").State; st != "needs_attention" {
		t.Fatalf("SL-1 state = %s, want needs_attention", st)
	}

	// 1 スロット 1 セッション: SL-2 は busy、SL-1 は needs_attention → 使えるスロット無し。
	if _, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("second AcquireSlot error = %v, want ErrSlotUnavailable", err)
	}

	// 作業ツリーを直しても、slot clear までは割り当てない。
	g.states[pa] = okState()
	releaseForTest(t, s, a)
	a2, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if err != nil || a2.SlotID != "SL-2" {
		t.Fatalf("AcquireSlot after release = (%+v, %v), want SL-2", a2, err)
	}
	releaseForTest(t, s, a2)
	if _, err := s.ClearSlot(ctx, "SL-1"); err != nil {
		t.Fatalf("ClearSlot: %v", err)
	}
	if st := slotByID(t, s, "SL-1"); st.State != "idle" || st.AttentionReason != "" {
		t.Fatalf("SL-1 after clear = %+v", st)
	}
	a3, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if err != nil || a3.SlotID != "SL-1" {
		t.Fatalf("AcquireSlot after clear = (%+v, %v), want SL-1", a3, err)
	}
}

func TestClearSlot_Errors(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	if _, err := s.ClearSlot(ctx, "SL-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing slot: %v, want ErrNotFound", err)
	}
	if _, err := s.ClearSlot(ctx, "C-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed ID: %v, want ErrNotFound", err)
	}
	if err := s.EnsureSlots(ctx, cloneRepoForTest("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearSlot(ctx, "SL-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("idle slot: %v, want ErrInvalidTransition", err)
	}
}

// slot の書き込み（作成・割り当て・解放・needs_attention・clear）は作業ログに載せない。
func TestSlotWritesDoNotAppearInActivityLog(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	g.states[filepath.Join(s.Workspace(), "a")] = okState()
	// SL-1（missing）は needs_attention になり、SL-2（a）が割り当てられる。
	a, err := s.AcquireSlot(ctx, g, cloneRepoForTest("missing", "a"), bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	releaseForTest(t, s, a)
	if _, err := s.ClearSlot(ctx, "SL-1"); err != nil {
		t.Fatal(err)
	}
	acts, err := s.ListActivities(ctx, nil)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if len(acts) != 0 {
		t.Errorf("activities = %d, want 0", len(acts))
	}
}

// AC-288〜293 の core 側: worktree は count 本・初回の割り当てで払い出し・使い回し・失敗は needs_attention。
func TestAcquireSlot_WorktreeProvisionsOnceAndReuses(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(3)

	a, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	all, _ := s.ListSlots(ctx)
	if len(all) != 3 {
		t.Fatalf("slots = %d, want 3 (count)", len(all))
	}
	wantPath := filepath.Join(s.Workspace(), ".flywheel", "worktrees", "r", "SL-1")
	if a.Path != wantPath {
		t.Errorf("path = %q, want %q", a.Path, wantPath)
	}
	if len(g.ensured) != 1 || g.ensured[0].Ref != "refs/heads/main" || g.ensured[0].BaseClone != filepath.Join(s.Workspace(), "base") {
		t.Errorf("EnsureWorktree calls = %+v", g.ensured)
	}
	// 同じスロットを 2 つ目の課題へ: 払い出しは再び作られず、パスは変わらない。
	releaseForTest(t, s, a)
	a2, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if a2.SlotID != a.SlotID || a2.Path != a.Path {
		t.Errorf("second assignment = %+v, want the same slot and path as %+v", a2, a)
	}
	if len(g.added) != 1 {
		t.Errorf("worktrees created = %v, want exactly 1", g.added)
	}
	// count を減らしても行は消えず、増やすと足りない分だけ作る。
	if err := s.EnsureSlots(ctx, worktreeRepoForTest(1)); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListSlots(ctx); len(all) != 3 {
		t.Errorf("slots after reducing count = %d, want 3", len(all))
	}
	if err := s.EnsureSlots(ctx, worktreeRepoForTest(4)); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListSlots(ctx); len(all) != 4 {
		t.Errorf("slots after raising count = %d, want 4", len(all))
	}
}

func TestAcquireSlot_WorktreeProvisioningFailureAndPointerMismatch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), ".flywheel", "worktrees", "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), ".flywheel", "worktrees", "r", "SL-2")
	g.addErr[p1] = fmt.Errorf("git worktree: invalid reference: refs/heads/main")
	g.states[p2] = SlotTreeState{Exists: true, PointerOK: false, PointerProblem: "points elsewhere"}

	if _, err := s.AcquireSlot(ctx, g, repo, bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("error = %v, want ErrSlotUnavailable", err)
	}
	sl1, sl2 := slotByID(t, s, "SL-1"), slotByID(t, s, "SL-2")
	if sl1.State != "needs_attention" || !contains(sl1.AttentionReason, "invalid reference") {
		t.Errorf("SL-1 = %+v", sl1)
	}
	if sl2.State != "needs_attention" || !contains(sl2.AttentionReason, "points elsewhere") {
		t.Errorf("SL-2 = %+v", sl2)
	}
}

// 2 つのプロセス（ここでは 2 つのハンドル）が同じスロットを取らない。
func TestAcquireSlot_ConcurrentAcquirersNeverShareASlot(t *testing.T) {
	s := newStoreForTest(t)
	s2, err := OpenWorkspace(s.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	ctx := context.Background()
	g := newFakeSlotGit()
	g.states[filepath.Join(s.Workspace(), "only")] = okState()
	repo := cloneRepoForTest("only")

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, st := range []*Store{s, s2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = st.AcquireSlot(ctx, g, repo, bindRunForTest(ctx))
		}()
	}
	wg.Wait()
	ok, unavailable := 0, 0
	for _, e := range results {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrSlotUnavailable):
			unavailable++
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if ok != 1 || unavailable != 1 {
		t.Errorf("ok=%d unavailable=%d, want exactly one of each", ok, unavailable)
	}
}

// ReleaseSlot は busy だけを idle にし、無いスロットは ErrNotFound。
func TestAcquireSlot_ReleaseOnlyAffectsBusySlot(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	if err := s.EnsureSlots(ctx, cloneRepoForTest("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSlot(ctx, "SL-1"); err != nil { // idle: no-op
		t.Errorf("ReleaseSlot(idle) = %v", err)
	}
	if err := s.ReleaseSlot(ctx, "SL-7"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReleaseSlot(missing) = %v, want ErrNotFound", err)
	}
}

func TestAcquireSlot_NoDeclaredSlotsIsUnavailable(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	repo := ConnectorRepo{Name: "r", Remote: "o/r", DefaultBranch: "main"}
	if _, err := s.AcquireSlot(ctx, newFakeSlotGit(), repo, bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("error = %v, want ErrSlotUnavailable", err)
	}
}

// git を起動できない失敗は、スロットを needs_attention にせず AcquireSlot のエラーで返す。
func TestAcquireSlot_GitUnavailableIsNotASlotProblem(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	g.errs[filepath.Join(s.Workspace(), "c")] = fmt.Errorf("wrapped: %w", ErrGitUnavailable)
	_, err := s.AcquireSlot(ctx, g, cloneRepoForTest("c"), bindRunForTest(ctx))
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("error = %v, want ErrGitUnavailable", err)
	}
	if sl := slotByID(t, s, "SL-1"); sl.State != "idle" {
		t.Errorf("slot = %+v, want idle", sl)
	}
}

func TestEnsureSlots_WorktreeNameMustBeASinglePathElementAndIsGitignored(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	bad := worktreeRepoForTest(1)
	bad.Name = "../escape"
	if err := s.EnsureSlots(ctx, bad); !errors.Is(err, ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
	// 既存の .gitignore（worktrees/ が無い）へ行を足す。
	gi := filepath.Join(s.Workspace(), ".flywheel", ".gitignore")
	if err := os.WriteFile(gi, []byte("flywheel.db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSlots(ctx, worktreeRepoForTest(1)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(gi)
	if string(data) != "flywheel.db\nworktrees/\n" {
		t.Errorf(".gitignore = %q", data)
	}
	if err := s.EnsureSlots(ctx, worktreeRepoForTest(1)); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(gi); string(again) != string(data) {
		t.Errorf(".gitignore changed on the second call: %q", again)
	}
}
