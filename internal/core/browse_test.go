package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// snapshotTables は run・activity・cycle_lock（lock）の全行を文字列にして返す
// （回収が行を変えないことの比較用）。
func snapshotTables(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		for _, tbl := range []string{"run", "activity", "lock", "slot"} {
			rows, err := tx.Query("SELECT * FROM " + tbl + " ORDER BY 1")
			if err != nil {
				return err
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					_ = rows.Close()
					return err
				}
				out[tbl] = append(out[tbl], fmt.Sprint(vals...))
			}
			_ = rows.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

func staleOpenRunFixture(t *testing.T) *Store {
	t.Helper()
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	cid, _ := parseChallengeID(id)
	old := time.Now().Add(-time.Hour).UTC()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111", PID: deadPID(t), Host: host,
		HeartbeatAt: old, StartedAt: old, MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestListRunsWithoutReap_DoesNotChangeRows(t *testing.T) {
	s := staleOpenRunFixture(t)
	before := snapshotTables(t, s)
	runs, err := s.ListRunsWithoutReap(context.Background(), RunListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Result != "" || runs[0].EndedAt != nil {
		t.Fatalf("runs = %+v, want one still-open run", runs)
	}
	if after := snapshotTables(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("rows changed:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestListRuns_StillReapsStaleRun(t *testing.T) {
	s := staleOpenRunFixture(t)
	runs, err := s.ListRuns(context.Background(), RunListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Result != RunResultInterrupted {
		t.Fatalf("runs = %+v, want the stale run reaped as interrupted", runs)
	}
}

func TestResolveWorkspaceDir(t *testing.T) {
	ws := t.TempDir()
	if _, err := Init(ws); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(ws, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(WorkspaceEnvVar, "")
	t.Chdir(sub)
	got, err := ResolveWorkspaceDir("")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(ws)
	if g, _ := filepath.EvalSymlinks(got); g != want {
		t.Errorf("upward search = %q, want %q", got, want)
	}

	// 明示された基点は遡らず、ストアが無くてもパスを返す。
	empty := t.TempDir()
	got, err = ResolveWorkspaceDir(empty)
	if err != nil || got != empty {
		t.Errorf("explicit = %q, %v; want %q", got, err, empty)
	}
	t.Setenv(WorkspaceEnvVar, empty)
	got, err = ResolveWorkspaceDir("")
	if err != nil || got != empty {
		t.Errorf("env = %q, %v; want %q", got, err, empty)
	}
	if _, err := os.Stat(filepath.Join(empty, ".flywheel")); err == nil {
		t.Error("ResolveWorkspaceDir created .flywheel")
	}

	// 上位にも無ければカレントディレクトリ。
	t.Setenv(WorkspaceEnvVar, "")
	t.Chdir(empty)
	got, err = ResolveWorkspaceDir("")
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := filepath.EvalSymlinks(got); g != func() string { e, _ := filepath.EvalSymlinks(empty); return e }() {
		t.Errorf("fallback = %q, want cwd %q", got, empty)
	}
}

func TestSchemaVersion(t *testing.T) {
	s := newStoreForTest(t)
	v, err := s.SchemaVersion(context.Background())
	latest, lerr := LatestSchemaVersion()
	if err != nil || lerr != nil || v != latest || v < 6 {
		t.Errorf("SchemaVersion = %d, %v; latest = %d, %v", v, err, latest, lerr)
	}
	// 開いた後に別の接続で上げられた版を読み直せる。
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", latest+1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.SchemaVersion(context.Background()); err != nil || v != latest+1 {
		t.Errorf("after bump SchemaVersion = %d, %v; want %d", v, err, latest+1)
	}
}

func TestListUnansweredHolds(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	mk := func(title string) int64 {
		ch, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		n, _ := parseChallengeID(ch.ID)
		return n
	}
	c1, c2, c3 := mk("one"), mk("two"), mk("three")
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []struct {
			cid      int64
			question string
			raised   string
			answered bool
		}{
			{c2, "q2", "2026-09-25T00:00:00.000Z", false},
			{c1, "q1-same-time", "2026-09-25T00:00:00.000Z", false},
			{c3, "q3-old", "2026-09-24T00:00:00.000Z", false},
			{c1, "answered", "2026-09-20T00:00:00.000Z", true},
		} {
			if q.answered {
				if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by) VALUES (?, ?, 'in_progress', ?, 'a', ?, 'alice')`, q.cid, q.question, q.raised, q.raised); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, ?, 'in_progress', ?)`, q.cid, q.question, q.raised); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// run_id つきの保留（run を 1 件作って参照する）。
	row, err := insertRunForTest(t, s, c3, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111", PID: 1, Host: "h",
		HeartbeatAt: time.Now(), StartedAt: time.Now(), MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketJudgment,
	})
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := parseRunID(row.ID)
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, run_id) VALUES (?, 'q-run', 'in_progress', '2026-09-26T00:00:00.000Z', ?)`, c3, rid)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListUnansweredHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var qs []string
	for _, h := range got {
		qs = append(qs, h.Question)
	}
	if want := []string{"q3-old", "q1-same-time", "q2", "q-run"}; !reflect.DeepEqual(qs, want) {
		t.Fatalf("order = %v, want %v", qs, want)
	}
	if got[0].ChallengeID != "C-3" || got[0].Title != "three" || got[0].FromStatus != "in_progress" || got[0].RunID != nil {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[3].RunID == nil || *got[3].RunID != row.ID {
		t.Errorf("run_id = %v, want %s", got[3].RunID, row.ID)
	}
}

func TestListUnansweredHolds_EmptyIsNonNil(t *testing.T) {
	s := newStoreForTest(t)
	got, err := s.ListUnansweredHolds(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("got %v, %v; want empty non-nil", got, err)
	}
}

func TestReadRunReport(t *testing.T) {
	s := staleOpenRunFixture(t) // R-1 が存在する
	ctx := context.Background()

	for _, bad := range []string{"R-01", "R-1/../x", "../../etc", "R-0", "", "r-1"} {
		if _, err := s.ReadRunReport(ctx, bad); !errors.Is(err, ErrValidation) {
			t.Errorf("ReadRunReport(%q) err = %v, want ErrValidation", bad, err)
		}
	}
	if _, err := s.ReadRunReport(ctx, "R-99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing run err = %v, want ErrNotFound", err)
	}

	// ファイルなし → 空の配列。
	r, err := s.ReadRunReport(ctx, "R-1")
	if err != nil || r.RunID != "R-1" || r.Assumptions == nil || r.Unverified == nil || len(r.Assumptions)+len(r.Unverified) != 0 {
		t.Errorf("no file: %+v, %v", r, err)
	}

	dir := runDirPath(s.Workspace(), "R-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "stdout.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"structured_output":{"assumptions":["a1","a2"],"unverified":["u1"]}}`)
	r, err = s.ReadRunReport(ctx, "R-1")
	if err != nil || !reflect.DeepEqual(r.Assumptions, []string{"a1", "a2"}) || !reflect.DeepEqual(r.Unverified, []string{"u1"}) {
		t.Errorf("full: %+v, %v", r, err)
	}
	// 片方のキーが壊れていても、もう片方は読む。壊れた JSON は空の配列。
	write(`{"structured_output":{"assumptions":[1],"unverified":["u"]}}`)
	r, err = s.ReadRunReport(ctx, "R-1")
	if err != nil || len(r.Assumptions) != 0 || !reflect.DeepEqual(r.Unverified, []string{"u"}) {
		t.Errorf("partial: %+v, %v", r, err)
	}
	for _, body := range []string{`{not json`, `{"structured_output":"x"}`} {
		write(body)
		r, err = s.ReadRunReport(ctx, "R-1")
		if err != nil || r.Assumptions == nil || len(r.Assumptions)+len(r.Unverified) != 0 {
			t.Errorf("%s: %+v, %v", body, r, err)
		}
	}
	// stdout.json が読めない（ディレクトリ）→ ErrStoreError。
	if err := os.Remove(filepath.Join(dir, "stdout.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "stdout.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRunReport(ctx, "R-1"); !errors.Is(err, ErrStoreError) {
		t.Errorf("unreadable err = %v, want ErrStoreError", err)
	}
	if err := os.Remove(filepath.Join(dir, "stdout.json")); err != nil {
		t.Fatal(err)
	}
	write(`{"structured_output":{"summary":"x"}}`)
	// キーなし → 空の配列。
	write(`{"structured_output":{"summary":"x"}}`)
	r, err = s.ReadRunReport(ctx, "R-1")
	if err != nil || r.Assumptions == nil || r.Unverified == nil || len(r.Assumptions) != 0 || len(r.Unverified) != 0 {
		t.Errorf("no keys: %+v, %v", r, err)
	}
}

// `flywheel status` が呼ぶ GetOverviewFor・ListWaitingExternal は中断した run を
// 回収しない（M4P12 の前提の確認）。
type noChecks struct{}

func (noChecks) GetPullRequestChecks(context.Context, string, int) (UpstreamPullRequestChecks, error) {
	return UpstreamPullRequestChecks{}, nil
}

func TestGetChallengeWithoutReap_DoesNotChangeRows(t *testing.T) {
	s := staleOpenRunFixture(t)
	before := snapshotTables(t, s)
	d, err := s.GetChallengeWithoutReap(context.Background(), "C-1")
	if err != nil || len(d.Runs) != 1 || d.Runs[0].Result != "" {
		t.Fatalf("detail runs = %+v, %v", d, err)
	}
	if after := snapshotTables(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("rows changed")
	}
	d, err = s.GetChallenge(context.Background(), "C-1")
	if err != nil || d.Runs[0].Result != RunResultInterrupted {
		t.Errorf("GetChallenge should still reap: %+v, %v", d, err)
	}
}

func TestGetOverviewFor_DoesNotReapStaleRun(t *testing.T) {
	s := staleOpenRunFixture(t)
	before := snapshotTables(t, s)
	if _, err := s.GetOverviewFor(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListWaitingExternal(context.Background(), noChecks{}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTables(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("GetOverviewFor changed rows:\nbefore=%v\nafter=%v", before, after)
	}
}
