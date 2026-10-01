package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// このファイルは #99（親要件チケット #98 §クリティカル設計決定 1）が足す
// マイグレーション 0005 の受入基準 AC-280・AC-370・AC-371 を検証する: 版 4 の
// ストアを開いたときの版・既存データの保持、slot・run_artifact・run の新しい列、
// スロットごとの run の排他。

func v4OnlyMigrationsForTest(t *testing.T) []store.Migration {
	t.Helper()
	all, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations(): %v", err)
	}
	var v4 []store.Migration
	for _, m := range all {
		if m.Version <= 4 {
			v4 = append(v4, m)
		}
	}
	if len(v4) != 4 {
		t.Fatalf("v4OnlyMigrationsForTest: found %d migrations with Version <= 4, want 4", len(v4))
	}
	return v4
}

// openSchemaVersion4Fixture は版 4 のストアに、課題・計画・承認・保留（run を
// 参照）・作業ログ（run を参照）・対応の記録・周・run（2 件。1 件は 1 件目を
// 再開したもの）を作り、OpenWorkspace で 0005 まで適用してから返す。
func openSchemaVersion4Fixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, flywheelDirName))

	legacy, err := store.Open(storeDBPath(dir), v4OnlyMigrationsForTest(t))
	if err != nil {
		t.Fatalf("store.Open(v4) error = %v", err)
	}
	if err := legacy.Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO challenge (id, title, description, done_criteria, urgency, priority, status, version, reporter, created_at, updated_at)
			 VALUES (1, 'legacy title', 'legacy desc', 'legacy done', 'high', 'P1', 'in_progress', 2, 'alice', '` + fixtureTS + `', '` + fixtureTS + `')`,
			`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (1, 1, 'plan body', '` + fixtureTS + `', 'spec body')`,
			`INSERT INTO operation (id, challenge_id, kind, summary, ref, state, version, created_at)
			 VALUES (1, 1, 'release', 'do the release', 'ref-1', 'pending', 1, '` + fixtureTS + `')`,
			`INSERT INTO approval (id, challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			 VALUES (1, 1, 1, 'release', 'approved', 1, 'alice', 'cli', 'tty_confirm', NULL, '` + fixtureTS + `')`,
			`INSERT INTO cycle (id, trigger, started_at, budget_usd) VALUES (1, 'cli', '` + fixtureTS + `', 5000000)`,
			`INSERT INTO run (id, cycle_id, kind, judgment, challenge_id, challenge_version, plan_version, session_id, pid, host,
				heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, cost_source, output)
			 VALUES (1, 1, 'judgment', 'J1', 1, 2, 1, 'sess-1', 10, 'h', '` + fixtureTS + `', '` + fixtureTS + `', '` + fixtureTS + `',
				'succeeded', 500000, 'judgment', 1234, 'reported', 'out-1')`,
			`INSERT INTO run (id, cycle_id, kind, challenge_id, challenge_version, session_id, resumed_from_run_id, pid, host,
				heartbeat_at, started_at, max_budget_usd, budget_bucket)
			 VALUES (2, 1, 'delegate', 1, 2, 'sess-2', 1, 11, 'h', '` + fixtureTS + `', '` + fixtureTS + `', 900000, 'impl')`,
			`INSERT INTO hold (id, challenge_id, question, from_status, raised_at, run_id)
			 VALUES (1, 1, 'need info?', 'in_progress', '` + fixtureTS + `', 2)`,
			`INSERT INTO activity (id, at, actor, channel, verification, entity, entity_id, action, before, after, run_id)
			 VALUES (1, '` + fixtureTS + `', 'alice', 'cli', 'none', 'challenge', 1, 'create', NULL, '{"status":"unclassified"}', 2)`,
			`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, comments_count, upstream_updated_at, read_comments_count, read_upstream_updated_at, created_at, updated_at)
			 VALUES (1, 's', 'o/r#1', 'https://example.invalid/o/r/1', '2:abc', 'open', 'in_policy', 5, '` + fixtureTS + `', 5, '` + fixtureTS + `', '` + fixtureTS + `', '` + fixtureTS + `')`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("legacy fixture write error = %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenWorkspace_UpgradesSchemaVersion4StoreToVersion5(t *testing.T) {
	s := openSchemaVersion4Fixture(t)
	ctx := context.Background()

	// AC-370: 版が 5 になる。
	if got := schemaVersionForTest(t, s); got != 5 {
		t.Fatalf("schema version = %d, want 5 (AC-370)", got)
	}

	// AC-371: 既存の課題・計画・承認・保留・作業ログ・対応の記録・run・周が
	// 件数と内容で保持される。
	detail, err := s.GetChallenge(ctx, "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Title != "legacy title" || detail.Version != 2 || detail.Status != "in_progress" {
		t.Fatalf("GetChallenge() = %+v (AC-371)", detail)
	}
	if detail.SourceBinding == nil || detail.SourceBinding.ExternalKey != "o/r#1" {
		t.Fatalf("SourceBinding = %+v (AC-371)", detail.SourceBinding)
	}
	if len(detail.Approvals) != 1 || len(detail.Operations) != 1 {
		t.Fatalf("approvals=%d operations=%d, want 1 and 1 (AC-371)", len(detail.Approvals), len(detail.Operations))
	}
	plans := loadTaskPlanRowsForTest(t, s, 1)
	if len(plans) != 1 || plans[0].Body != "plan body" || plans[0].Spec == nil || *plans[0].Spec != "spec body" {
		t.Fatalf("task_plan rows = %+v (AC-371)", plans)
	}
	holds := loadHoldRowsForTest(t, s, 1)
	if len(holds) != 1 || holds[0].Question != "need info?" || holds[0].RunID == nil || *holds[0].RunID != 2 {
		t.Fatalf("hold rows = %+v, want 1 row referencing run 2 (AC-371)", holds)
	}
	if got := loadActivityRunIDForTest(t, s, 1); got == nil || *got != 2 {
		t.Fatalf("activity.run_id = %v, want 2 (AC-371)", got)
	}
	activities, err := s.ListActivities(ctx, stringPtr("C-1"))
	if err != nil || len(activities) != 1 || activities[0].Action != "create" {
		t.Fatalf("activities = %+v err=%v (AC-371)", activities, err)
	}

	r1, err := loadRunForTest(t, s, 1)
	if err != nil || r1 == nil {
		t.Fatalf("loadRun(1) = %v, %v", r1, err)
	}
	if r1.Kind != runKindJudgment || r1.Judgment != judgmentJ1 || r1.ChallengeID != "C-1" || r1.ChallengeVersion != 2 ||
		r1.SessionID != "sess-1" || r1.Result != runResultSucceeded || r1.CostUSD == nil || *r1.CostUSD != 1234 ||
		r1.Output != "out-1" || r1.PlanVersion == nil || *r1.PlanVersion != 1 {
		t.Fatalf("run 1 = %+v (AC-371)", r1)
	}
	if r1.SlotID != nil || r1.Decider != "" || r1.DeciderRow != nil || r1.Repo != "" {
		t.Fatalf("run 1 new columns = %+v, want all NULL", r1)
	}
	r2, err := loadRunForTest(t, s, 2)
	if err != nil || r2 == nil {
		t.Fatalf("loadRun(2) = %v, %v", r2, err)
	}
	if r2.Kind != runKindDelegate || r2.ResumedFromRunID == nil || *r2.ResumedFromRunID != "R-1" || r2.Result != "" || r2.BudgetBucket != budgetBucketImpl {
		t.Fatalf("run 2 = %+v (AC-371)", r2)
	}
	// 24 列すべてを移行前の値と突き合わせる（フィクスチャの値は既定値以外）。
	for id, want := range map[int64]string{
		1: "1|1|judgment|J1|1|2|1|sess-1|0||10|h|" + fixtureTS + "|" + fixtureTS + "|" + fixtureTS + "|succeeded|0|500000|judgment|1234|reported||out-1|",
		2: "2|1|delegate||1|2||sess-2|0|1|11|h|" + fixtureTS + "|" + fixtureTS + "|||0|900000|impl|||||",
	} {
		if got := legacyRunRowStringForTest(t, s, id); got != want {
			t.Fatalf("run %d row = %q, want %q (AC-371)", id, got, want)
		}
	}
	if got := countRowsForTest(t, s, "run"); got != 2 {
		t.Fatalf("run count = %d, want 2", got)
	}
	if got := countRowsForTest(t, s, "cycle"); got != 1 {
		t.Fatalf("cycle count = %d, want 1", got)
	}

	// 作り直した run でも、課題ごとの終了していない run の排他（AC-168）が効く。
	if _, err := insertRunForTest(t, s, 1, validInsertRunInput()); !errors.Is(err, errActiveRunExists) {
		t.Fatalf("insertRun(active challenge) error = %v, want errActiveRunExists", err)
	}
	// 外部キーが有効なまま（孤児の run_id を拒否する）。
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, run_id) VALUES (1, 'q', 'in_progress', ?, 999)`, fixtureTS)
		return err
	}); err == nil {
		t.Fatalf("hold with dangling run_id was accepted, want foreign key failure")
	}
}

func TestOpenWorkspace_Version5ColumnSetIsFixed(t *testing.T) {
	s := openSchemaVersion4Fixture(t)

	wantRun := []string{
		"id", "cycle_id", "kind", "judgment", "challenge_id", "challenge_version", "plan_version",
		"session_id", "session_id_mismatch", "resumed_from_run_id", "pid", "host", "heartbeat_at",
		"started_at", "ended_at", "result", "rate_limited", "max_budget_usd", "budget_bucket",
		"cost_usd", "cost_source", "reported_total_cost_usd", "output", "error",
		"slot_id", "decider", "decider_row", "repo",
	}
	if got := pragmaTableInfoColumnsForTest(t, s, "run"); !stringSlicesEqual(got, wantRun) {
		t.Fatalf("run columns = %v, want %v", got, wantRun)
	}
	wantSlot := []string{"id", "repo", "provider", "path", "state", "run_id", "attention_reason"}
	if got := pragmaTableInfoColumnsForTest(t, s, "slot"); !stringSlicesEqual(got, wantSlot) {
		t.Fatalf("slot columns = %v, want %v", got, wantSlot)
	}
	wantArtifact := []string{"id", "run_id", "kind", "ref", "state", "base", "verified_at"}
	if got := pragmaTableInfoColumnsForTest(t, s, "run_artifact"); !stringSlicesEqual(got, wantArtifact) {
		t.Fatalf("run_artifact columns = %v, want %v", got, wantArtifact)
	}
	tp := pragmaTableInfoColumnsForTest(t, s, "task_plan")
	if !containsString(tp, "impl_budget_usd") || !containsString(tp, "review_budget_usd") {
		t.Fatalf("task_plan columns = %v, want impl_budget_usd and review_budget_usd", tp)
	}
}

func TestSchemaV5_FreshStoreHasSlotTables(t *testing.T) {
	s := newStoreForTest(t)
	if got := schemaVersionForTest(t, s); got != 5 {
		t.Fatalf("schema version = %d, want 5", got)
	}
	if !tableExistsForTest(t, s, "slot") || !tableExistsForTest(t, s, "run_artifact") {
		t.Fatalf("slot and run_artifact tables must exist")
	}
}

// --- slot ---

func insertSlotForTest(t *testing.T, s *Store, in insertSlotInput) (*slotRow, error) {
	t.Helper()
	var out *slotRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := insertSlot(context.Background(), tx, in)
		out = r
		return err
	})
	return out, classifyReadWriteErr(err)
}

func validSlotInput() insertSlotInput {
	return insertSlotInput{Repo: "o/r", Provider: slotProviderWorktree, Path: "/tmp/slot-1"}
}

func mustSlotForTest(t *testing.T, s *Store) (*slotRow, int64) {
	t.Helper()
	slot, err := insertSlotForTest(t, s, validSlotInput())
	if err != nil {
		t.Fatalf("insertSlot() error = %v", err)
	}
	id, ok := parseSlotID(slot.ID)
	if !ok {
		t.Fatalf("slot ID %q is not SL-<n>", slot.ID)
	}
	return slot, id
}

func TestInsertSlot_CreatesIdleSlotAndRejectsInvalidInput(t *testing.T) {
	s := newStoreForTest(t)
	slot, err := insertSlotForTest(t, s, validSlotInput())
	if err != nil {
		t.Fatalf("insertSlot() error = %v", err)
	}
	if slot.ID != "SL-1" || slot.Repo != "o/r" || slot.Provider != slotProviderWorktree || slot.Path != "/tmp/slot-1" ||
		slot.State != slotStateIdle || slot.RunID != nil || slot.AttentionReason != "" {
		t.Fatalf("slot = %+v", slot)
	}
	for name, in := range map[string]insertSlotInput{
		"empty repo":       {Provider: slotProviderClone, Path: "/p"},
		"bad provider":     {Repo: "o/r", Provider: "container", Path: "/p"},
		"empty path":       {Repo: "o/r", Provider: slotProviderClone},
		"empty provider":   {Repo: "o/r", Path: "/p"},
		"uppercase clone":  {Repo: "o/r", Provider: "Clone", Path: "/p"},
		"whitespace":       {Repo: "o/r", Provider: " clone", Path: "/p"},
		"worktree-with-nl": {Repo: "o/r", Provider: "worktree\n", Path: "/p"},
	} {
		if _, err := insertSlotForTest(t, s, in); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: insertSlot() error = %v, want ErrValidation", name, err)
		}
	}
}

func TestUpdateSlotState_RecordsAttentionReasonAndEnforcesConsistency(t *testing.T) {
	s := newStoreForTest(t)
	_, slotID := mustSlotForTest(t, s)
	cid := mustCreateChallengeInternalID(t, s, "c")
	run, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun: %v", err)
	}
	runID := mustParseRunIDForTest(t, run.ID)

	update := func(state slotState, rid *int64, reason string) (*slotRow, error) {
		var out *slotRow
		err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			r, err := updateSlotState(context.Background(), tx, slotID, state, rid, reason)
			out = r
			return err
		})
		return out, err
	}

	busy, err := update(slotStateBusy, &runID, "")
	if err != nil || busy.State != slotStateBusy || busy.RunID == nil || *busy.RunID != run.ID {
		t.Fatalf("busy = %+v err=%v", busy, err)
	}
	na, err := update(slotStateNeedsAttention, nil, "uncommitted changes in /tmp/slot-1")
	if err != nil || na.State != slotStateNeedsAttention || na.AttentionReason != "uncommitted changes in /tmp/slot-1" || na.RunID != nil {
		t.Fatalf("needs_attention = %+v err=%v", na, err)
	}
	idle, err := update(slotStateIdle, nil, "")
	if err != nil || idle.State != slotStateIdle || idle.AttentionReason != "" {
		t.Fatalf("idle = %+v err=%v (reason must be cleared)", idle, err)
	}

	for name, c := range map[string]struct {
		state  slotState
		rid    *int64
		reason string
	}{
		"busy without run":           {slotStateBusy, nil, ""},
		"idle with run":              {slotStateIdle, &runID, ""},
		"needs_attention w/o reason": {slotStateNeedsAttention, nil, ""},
		"idle with reason":           {slotStateIdle, nil, "x"},
		"unknown state":              {"broken", nil, ""},
	} {
		if _, err := update(c.state, c.rid, c.reason); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: error = %v, want ErrValidation", name, err)
		}
	}
}

// AC-280: 1 つのスロットを使う終了していない run を 2 つ書き込もうとするとストアが拒否する。
func TestInsertRun_RejectsSecondActiveRunForSameSlot(t *testing.T) {
	s := newStoreForTest(t)
	_, slotID := mustSlotForTest(t, s)
	c1 := mustCreateChallengeInternalID(t, s, "c1")
	c2 := mustCreateChallengeInternalID(t, s, "c2")

	in := validInsertRunInput()
	in.SlotID = &slotID
	first, err := insertRunForTest(t, s, c1, in)
	if err != nil {
		t.Fatalf("insertRun(1回目) error = %v", err)
	}
	if first.SlotID == nil || *first.SlotID != "SL-1" {
		t.Fatalf("run.SlotID = %v, want SL-1", first.SlotID)
	}
	if _, err := insertRunForTest(t, s, c2, in); !errors.Is(err, errActiveSlotRunExists) {
		t.Fatalf("insertRun(2回目・同じスロット) error = %v, want errActiveSlotRunExists (AC-280)", err)
	}
	// スロットを指定しない run や別のスロットの run は妨げない。
	if _, err := insertRunForTest(t, s, c2, validInsertRunInput()); err != nil {
		t.Fatalf("insertRun(スロットなし) error = %v", err)
	}
	// 終了すれば同じスロットを使える。
	if _, err := updateRunEndForTest(t, s, mustParseRunIDForTest(t, first.ID), updateRunEndInput{
		EndedAt: time.Now(), Result: runResultSucceeded,
	}); err != nil {
		t.Fatalf("updateRunEnd: %v", err)
	}
	c3 := mustCreateChallengeInternalID(t, s, "c3")
	if _, err := insertRunForTest(t, s, c3, in); err != nil {
		t.Fatalf("insertRun(終了後の再利用) error = %v", err)
	}
}

// ストア自身（スキーマ）がスロットの排他を拒否する（core の検査を経由しない生の INSERT）。
func TestStore_RawInsertRejectsSecondActiveRunForSameSlot(t *testing.T) {
	s := newStoreForTest(t)
	_, slotID := mustSlotForTest(t, s)
	ins := `INSERT INTO run (kind, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket, slot_id, repo)
	        VALUES ('predict', 1, 'h', ?, ?, 1, 'predict', ?, 'o/r')`
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(ins, fixtureTS, fixtureTS, slotID)
		return err
	}); err != nil {
		t.Fatalf("first raw insert: %v", err)
	}
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(ins, fixtureTS, fixtureTS, slotID)
		return err
	})
	if !store.IsUniqueViolation(err) {
		t.Fatalf("second raw insert error = %v, want unique violation (AC-280)", err)
	}
}

func TestInsertRun_DeciderAndRepoValidation(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "c")
	row := int64Ptr(3)

	in := validInsertRunInput()
	in.Decider, in.DeciderRow = deciderChild, row
	in.Repo = "o/r"
	r, err := insertRunForTest(t, s, cid, in)
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	if r.Decider != deciderChild || r.DeciderRow == nil || *r.DeciderRow != 3 || r.Repo != "o/r" {
		t.Fatalf("run = %+v", r)
	}
	for name, mut := range map[string]func(*insertRunInput){
		"decider without row": func(in *insertRunInput) { in.Decider, in.DeciderRow = deciderHuman, nil },
		"row without decider": func(in *insertRunInput) { in.Decider, in.DeciderRow = "", int64Ptr(1) },
		"row 0":               func(in *insertRunInput) { in.Decider, in.DeciderRow = deciderHuman, int64Ptr(0) },
		"row 6":               func(in *insertRunInput) { in.Decider, in.DeciderRow = deciderParent, int64Ptr(6) },
		"unknown decider":     func(in *insertRunInput) { in.Decider, in.DeciderRow = "robot", int64Ptr(1) },
		"predict bucket":      func(in *insertRunInput) { in.BudgetBucket = budgetBucketPredict },
	} {
		bad := validInsertRunInput()
		mut(&bad)
		if _, err := insertRunForTest(t, s, cid, bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: error = %v, want ErrValidation", name, err)
		}
	}
}

func validPredictRunInput() insertRunInput {
	return insertRunInput{
		Kind: runKindPredict, PID: 7, Host: "h",
		HeartbeatAt:  time.Date(2026, 10, 1, 0, 5, 0, 0, time.UTC),
		StartedAt:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		MaxBudgetUSD: 100_000, BudgetBucket: budgetBucketPredict, Repo: "o/r",
	}
}

func TestInsertRun_PredictRunHasNoChallengePlanOrSession(t *testing.T) {
	s := newStoreForTest(t)
	r, err := insertRunForTest(t, s, 0, validPredictRunInput())
	if err != nil {
		t.Fatalf("insertRun(predict) error = %v", err)
	}
	if r.Kind != runKindPredict || r.ChallengeID != "" || r.ChallengeVersion != 0 || r.PlanVersion != nil ||
		r.SessionID != "" || r.Repo != "o/r" || r.PID != 7 || r.Host != "h" || r.HeartbeatAt.IsZero() ||
		r.BudgetBucket != budgetBucketPredict {
		t.Fatalf("predict run = %+v", r)
	}
	// predict の run は課題を持たないので、課題ごとの排他の対象外（複数同時に持てる）。
	if _, err := insertRunForTest(t, s, 0, validPredictRunInput()); err != nil {
		t.Fatalf("second predict run error = %v", err)
	}

	cid := mustCreateChallengeInternalID(t, s, "c")
	for name, mut := range map[string]func(*insertRunInput){
		"with resumed_from": func(in *insertRunInput) { in.ResumedFromRunID = int64Ptr(1) },
		"missing repo":      func(in *insertRunInput) { in.Repo = "" },
		"with session":      func(in *insertRunInput) { in.SessionID = "s" },
		"with plan version": func(in *insertRunInput) { in.PlanVersion = int64Ptr(1) },
		"with challenge v":  func(in *insertRunInput) { in.ChallengeVersion = 1 },
		"with judgment":     func(in *insertRunInput) { in.Judgment = judgmentJ1 },
		"wrong bucket":      func(in *insertRunInput) { in.BudgetBucket = budgetBucketJudgment },
	} {
		bad := validPredictRunInput()
		mut(&bad)
		if _, err := insertRunForTest(t, s, 0, bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: error = %v, want ErrValidation", name, err)
		}
	}
	if _, err := insertRunForTest(t, s, cid, validPredictRunInput()); !errors.Is(err, ErrValidation) {
		t.Fatalf("predict with challenge id: error = %v, want ErrValidation", err)
	}
	if _, err := insertRunForTest(t, s, 0, validInsertRunInput()); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-predict without challenge id: error = %v, want ErrValidation", err)
	}
}

func TestUpdateRunEnd_PredictResultAndCostSourceSets(t *testing.T) {
	s := newStoreForTest(t)
	end := func(res runResult, src costSource) error {
		r, err := insertRunForTest(t, s, 0, validPredictRunInput())
		if err != nil {
			t.Fatalf("insertRun(predict): %v", err)
		}
		_, err = updateRunEndForTest(t, s, mustParseRunIDForTest(t, r.ID), updateRunEndInput{
			EndedAt: time.Now(), Result: res, CostSource: src, CostUSD: int64Ptr(10),
		})
		return err
	}
	for _, res := range []runResult{runResultSucceeded, runResultLaunchFailed, runResultTimedOut,
		runResultMalformed, runResultErrored, runResultInterrupted} {
		if err := end(res, costSourceReported); err != nil {
			t.Fatalf("predict result %s: error = %v", res, err)
		}
	}
	if err := end(runResultSucceeded, costSourceUnknown); err != nil {
		t.Fatalf("predict unknown cost source: %v", err)
	}
	for _, res := range []runResult{runResultBudgetExhausted, runResultInvalidOutput} {
		if err := end(res, costSourceReported); !errors.Is(err, ErrValidation) {
			t.Fatalf("predict result %s: error = %v, want ErrValidation", res, err)
		}
	}
	if err := end(runResultSucceeded, costSourceDelta); !errors.Is(err, ErrValidation) {
		t.Fatalf("predict delta cost source: error = %v, want ErrValidation", err)
	}
}

// --- run_artifact ---

func TestRunArtifact_InsertListAndValidation(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "c")
	run, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun: %v", err)
	}
	runID := mustParseRunIDForTest(t, run.ID)
	verified := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)

	write := func(in insertRunArtifactInput) (*runArtifactRow, error) {
		var out *runArtifactRow
		err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			r, err := insertRunArtifact(context.Background(), tx, runID, in)
			out = r
			return err
		})
		return out, err
	}
	pr, err := write(insertRunArtifactInput{Kind: artifactKindPR, Ref: "o/r#5", State: artifactStateOpen, Base: "main", VerifiedAt: &verified})
	if err != nil {
		t.Fatalf("insert pr: %v", err)
	}
	if pr.RunID != run.ID || pr.Kind != artifactKindPR || pr.State != artifactStateOpen || pr.Base != "main" ||
		pr.VerifiedAt == nil || !pr.VerifiedAt.Equal(verified) {
		t.Fatalf("pr = %+v", pr)
	}
	if _, err := write(insertRunArtifactInput{Kind: artifactKindBranch, Ref: "feat/x"}); err != nil {
		t.Fatalf("insert branch: %v", err)
	}
	c, err := write(insertRunArtifactInput{Kind: artifactKindCommit, Ref: "abc123"})
	if err != nil || c.State != "" || c.Base != "" || c.VerifiedAt != nil {
		t.Fatalf("commit = %+v err=%v", c, err)
	}
	for name, in := range map[string]insertRunArtifactInput{
		"bad kind":        {Kind: "tag", Ref: "x"},
		"empty ref":       {Kind: artifactKindBranch},
		"bad pr state":    {Kind: artifactKindPR, Ref: "x", State: "draft"},
		"state on branch": {Kind: artifactKindBranch, Ref: "x", State: artifactStateOpen},
		"state on commit": {Kind: artifactKindCommit, Ref: "x", State: artifactStateMerged},
	} {
		if _, err := write(in); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: error = %v, want ErrValidation", name, err)
		}
	}

	var list []runArtifactRow
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		list, err = listRunArtifacts(context.Background(), tx, runID)
		return err
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 || list[0].Kind != artifactKindPR || list[1].Kind != artifactKindBranch || list[2].Kind != artifactKindCommit {
		t.Fatalf("list = %+v", list)
	}

	// 存在しない run への成果物は外部キー違反で拒否される。
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertRunArtifact(context.Background(), tx, 9999, insertRunArtifactInput{Kind: artifactKindBranch, Ref: "x"})
		return err
	}); err == nil {
		t.Fatalf("artifact for missing run accepted")
	}
}

// M3P11: slot・run_artifact の書き込みは作業ログに載せない。
func TestSlotAndRunArtifactWritesDoNotAppearInActivity(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "c")
	before := countRowsForTest(t, s, "activity")

	_, slotID := mustSlotForTest(t, s)
	in := validInsertRunInput()
	in.SlotID = &slotID
	run, err := insertRunForTest(t, s, cid, in)
	if err != nil {
		t.Fatalf("insertRun: %v", err)
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := insertRunArtifact(context.Background(), tx, mustParseRunIDForTest(t, run.ID),
			insertRunArtifactInput{Kind: artifactKindBranch, Ref: "b"}); err != nil {
			return err
		}
		_, err := updateSlotState(context.Background(), tx, slotID, slotStateNeedsAttention, nil, "path missing")
		return err
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := countRowsForTest(t, s, "activity"); got != before {
		t.Fatalf("activity rows = %d, want %d (M3P11)", got, before)
	}
}

// --- 計画の枠の上書き ---

func TestPlanBudgetOverride_SetLoadAndClear(t *testing.T) {
	s := openSchemaVersion4Fixture(t)
	ctx := context.Background()

	var o *planBudgetOverride
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		o, err = loadPlanBudgetOverride(ctx, tx, 1, 1)
		return err
	}); err != nil || o == nil || o.ImplBudgetUSD != nil || o.ReviewBudgetUSD != nil {
		t.Fatalf("initial override = %+v err=%v, want empty (既存の計画は上書きなし)", o, err)
	}

	set := func(v planBudgetOverride, ver int64) (bool, error) {
		var ok bool
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			var err error
			ok, err = setPlanBudgetOverride(ctx, tx, 1, ver, v)
			return err
		})
		return ok, err
	}
	if ok, err := set(planBudgetOverride{ImplBudgetUSD: int64Ptr(12_000_000), ReviewBudgetUSD: int64Ptr(3_000_000)}, 1); err != nil || !ok {
		t.Fatalf("set = %v, %v", ok, err)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		o, err = loadPlanBudgetOverride(ctx, tx, 1, 1)
		return err
	}); err != nil || o.ImplBudgetUSD == nil || *o.ImplBudgetUSD != 12_000_000 || *o.ReviewBudgetUSD != 3_000_000 {
		t.Fatalf("override = %+v err=%v", o, err)
	}
	if ok, err := set(planBudgetOverride{ImplBudgetUSD: int64Ptr(1)}, 7); err != nil || ok {
		t.Fatalf("set for missing plan version = %v, %v, want false,nil", ok, err)
	}
	if _, err := set(planBudgetOverride{ImplBudgetUSD: int64Ptr(0)}, 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("set(0) error = %v, want ErrValidation", err)
	}
	if ok, err := set(planBudgetOverride{}, 1); err != nil || !ok {
		t.Fatalf("clear = %v, %v", ok, err)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		o, err = loadPlanBudgetOverride(ctx, tx, 1, 1)
		return err
	}); err != nil || o.ImplBudgetUSD != nil || o.ReviewBudgetUSD != nil {
		t.Fatalf("cleared override = %+v err=%v", o, err)
	}
}

func countRowsForTest(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
	}); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// legacyRunRowStringForTest は 0004 までに存在した run の 24 列を "|" 区切りで返す
// （NULL は空文字）。
func legacyRunRowStringForTest(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var out string
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT
			id || '|' || ifnull(cycle_id,'') || '|' || kind || '|' || ifnull(judgment,'') || '|' || ifnull(challenge_id,'') || '|' ||
			ifnull(challenge_version,'') || '|' || ifnull(plan_version,'') || '|' || ifnull(session_id,'') || '|' || session_id_mismatch || '|' ||
			ifnull(resumed_from_run_id,'') || '|' || pid || '|' || host || '|' || heartbeat_at || '|' || started_at || '|' ||
			ifnull(ended_at,'') || '|' || ifnull(result,'') || '|' || rate_limited || '|' || max_budget_usd || '|' || budget_bucket || '|' ||
			ifnull(cost_usd,'') || '|' || ifnull(cost_source,'') || '|' || ifnull(reported_total_cost_usd,'') || '|' ||
			ifnull(output,'') || '|' || ifnull(error,'')
			FROM run WHERE id = ?`, id).Scan(&out)
	})
	if err != nil {
		t.Fatalf("legacyRunRowStringForTest(%d): %v", id, err)
	}
	return out
}

func TestInsertSlot_RejectsSameRepoAndPath(t *testing.T) {
	s := newStoreForTest(t)
	if _, err := insertSlotForTest(t, s, validSlotInput()); err != nil {
		t.Fatalf("insertSlot() error = %v", err)
	}
	if _, err := insertSlotForTest(t, s, validSlotInput()); !store.IsUniqueViolation(err) {
		t.Fatalf("duplicate (repo, path) error = %v, want unique violation", err)
	}
	other := validSlotInput()
	other.Path = "/tmp/slot-2"
	if _, err := insertSlotForTest(t, s, other); err != nil {
		t.Fatalf("different path error = %v", err)
	}
}

func TestInsertRun_SlotSpecifiedButChallengeCollisionIsChallengeError(t *testing.T) {
	s := newStoreForTest(t)
	_, slot1 := mustSlotForTest(t, s)
	other := validSlotInput()
	other.Path = "/tmp/slot-2"
	sl2, err := insertSlotForTest(t, s, other)
	if err != nil {
		t.Fatalf("insertSlot: %v", err)
	}
	slot2, _ := parseSlotID(sl2.ID)
	cid := mustCreateChallengeInternalID(t, s, "c")

	a := validInsertRunInput()
	a.SlotID = &slot1
	if _, err := insertRunForTest(t, s, cid, a); err != nil {
		t.Fatalf("first: %v", err)
	}
	b := validInsertRunInput()
	b.SlotID = &slot2
	if _, err := insertRunForTest(t, s, cid, b); !errors.Is(err, errActiveRunExists) {
		t.Fatalf("same challenge, other slot error = %v, want errActiveRunExists", err)
	}
	c2 := mustCreateChallengeInternalID(t, s, "c2")
	if _, err := insertRunForTest(t, s, c2, b); err != nil {
		t.Fatalf("other challenge, other slot error = %v", err)
	}
}

func TestOpenWorkspace_Version5KeepsCycleAndLockColumns(t *testing.T) {
	s := newStoreForTest(t)
	if got := pragmaTableInfoColumnsForTest(t, s, "cycle"); !stringSlicesEqual(got,
		[]string{"id", "trigger", "started_at", "ended_at", "result", "budget_usd", "spent_usd"}) {
		t.Fatalf("cycle columns = %v", got)
	}
	if got := pragmaTableInfoColumnsForTest(t, s, "lock"); !stringSlicesEqual(got,
		[]string{"name", "holder", "pid", "host", "acquired_at", "heartbeat_at"}) {
		t.Fatalf("lock columns = %v", got)
	}
}
