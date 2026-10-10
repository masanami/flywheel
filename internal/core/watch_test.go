package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const watchHelperEnv = "FLYWHEEL_CORE_TEST_WATCH_WRITER"

// TestWatchWriterHelper は子プロセスとして再実行されたときだけ動き、別プロセスの
// 書き込みを 1 回して終わる（"challenge" = 課題の作成、"heartbeat" = run の heartbeat
// の更新だけ）。
func TestWatchWriterHelper(t *testing.T) {
	op := os.Getenv(watchHelperEnv)
	if op == "" {
		t.Skip("helper process only")
	}
	s, err := OpenWorkspace(os.Getenv("FLYWHEEL_CORE_TEST_WATCH_WS"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	switch op {
	case "challenge":
		if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "from child"}); err != nil {
			t.Fatal(err)
		}
	case "heartbeat":
		if _, err := updateRunHeartbeatForTest(t, s, 1, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown op %q", op)
	}
}

func writeFromChildProcess(t *testing.T, ws, op string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatchWriterHelper$")
	cmd.Env = append(os.Environ(), watchHelperEnv+"="+op, "FLYWHEEL_CORE_TEST_WATCH_WS="+ws)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child %s: %v\n%s", op, err, out)
	}
}

func newWatchedWorkspace(t *testing.T) (*Store, *WorkspaceWatcher) {
	t.Helper()
	s := newStoreForTest(t)
	w := NewWorkspaceWatcher(s.Workspace())
	t.Cleanup(func() { _ = w.Close() })
	return s, w
}

// AC-85: どのストアも変わらない間は変化を返さない。
func TestWorkspaceWatcher_NoChangeWhileStoreIsUntouched(t *testing.T) {
	ctx := context.Background()
	_, w := newWatchedWorkspace(t)
	if got := w.Poll(ctx); !got.Available || got.Changed {
		t.Fatalf("first Poll = %+v, want available baseline without change", got)
	}
	for i := 0; i < 5; i++ {
		if got := w.Poll(ctx); !got.Available || got.Changed {
			t.Fatalf("Poll #%d = %+v, want no change", i, got)
		}
	}
}

// 自分の接続の書き込みでも別の接続（別プロセス）の書き込みでも、課題の作成を検出し、
// 検出の後は変化を返さなくなる。
func TestWorkspaceWatcher_DetectsChallengeCreatedByAnotherProcess(t *testing.T) {
	ctx := context.Background()
	s, w := newWatchedWorkspace(t)
	w.Poll(ctx)
	writeFromChildProcess(t, s.Workspace(), "challenge")
	if got := w.Poll(ctx); !got.Available || !got.Changed {
		t.Fatalf("Poll after child challenge = %+v, want changed", got)
	}
	if got := w.Poll(ctx); got.Changed {
		t.Fatalf("second Poll = %+v, want no change", got)
	}
}

// AC-84: 課題・保留・作業ログに変化が無く、run の heartbeat だけが更新されたときも検出する。
func TestWorkspaceWatcher_DetectsHeartbeatOnlyUpdateByAnotherProcess(t *testing.T) {
	ctx := context.Background()
	s, w := newWatchedWorkspace(t)
	runID := insertFakeRunForTest(t, s, mustCreateChallenge(t, s, "c").ID, JudgmentJ1)
	if runID != 1 {
		t.Fatalf("fixture run id = %d, want 1", runID)
	}
	w.Poll(ctx)
	before := countActivities(t, s)
	writeFromChildProcess(t, s.Workspace(), "heartbeat")
	if got := countActivities(t, s); got != before {
		t.Fatalf("heartbeat update changed the activity log (%d -> %d); fixture invalid", before, got)
	}
	if got := w.Poll(ctx); !got.Available || !got.Changed {
		t.Fatalf("Poll after heartbeat-only update = %+v, want changed", got)
	}
	if got := w.Poll(ctx); got.Changed {
		t.Fatalf("second Poll = %+v, want no change", got)
	}
}

// ストアが無いワークスペースはエラーで落とさず状態として返し、現れたら変化として返す。
func TestWorkspaceWatcher_MissingStoreIsReportedAsState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := NewWorkspaceWatcher(dir)
	defer func() { _ = w.Close() }()
	for i := 0; i < 2; i++ {
		if got := w.Poll(ctx); got.Available || got.Changed {
			t.Fatalf("Poll on missing store = %+v, want unavailable without change", got)
		}
	}
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if got := w.Poll(ctx); !got.Available || !got.Changed {
		t.Fatalf("Poll after store appeared = %+v, want available+changed", got)
	}
	if got := w.Poll(ctx); got.Changed {
		t.Fatalf("Poll again = %+v, want no change", got)
	}
}

func TestWorkspaceWatcher_StoreDisappearingAndUnopenableAreStates(t *testing.T) {
	ctx := context.Background()
	s, w := newWatchedWorkspace(t)
	w.Poll(ctx)
	if err := os.Remove(s.StorePath()); err != nil {
		t.Fatal(err)
	}
	if got := w.Poll(ctx); got.Available || !got.Changed {
		t.Fatalf("Poll after store removed = %+v, want unavailable+changed", got)
	}
	// SQLite のファイルではない中身は開けない状態として返す。
	if err := os.WriteFile(s.StorePath(), []byte("not a database, just text padding padding padding padding"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := w.Poll(ctx); got.Available {
		t.Fatalf("Poll on garbage store = %+v, want unavailable", got)
	}
}

// ストアのファイルが差し替えられたら（古い接続が古いファイルを見続けないよう）開き直して変化として返す。
func TestWorkspaceWatcher_ReopensWhenStoreFileIsReplaced(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	w := NewWorkspaceWatcher(dir)
	defer func() { _ = w.Close() }()
	w.Poll(ctx)
	dbPath := filepath.Join(dir, ".flywheel", "flywheel.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if got := w.Poll(ctx); !got.Available || !got.Changed {
		t.Fatalf("Poll after replace = %+v, want available+changed", got)
	}
}

func countActivities(t *testing.T, s *Store) int {
	t.Helper()
	acts, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	return len(acts)
}
