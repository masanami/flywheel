package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// このファイルは #78（親要件チケット #77 §クリティカル設計決定 1）が足す
// run・cycle・lock の行レベルの読み書き（run_store.go）を検証する。
// AC-168（1 つの課題に終了していない run は高々 1 つ）はここで確かめる。

func int64Ptr(n int64) *int64 { return &n }

func validInsertRunInput() insertRunInput {
	return insertRunInput{
		Kind:             runKindJudgment,
		Judgment:         judgmentJ1,
		ChallengeVersion: 1,
		SessionID:        "11111111-1111-1111-1111-111111111111",
		PID:              4242,
		Host:             "host-a",
		HeartbeatAt:      time.Date(2026, 9, 28, 0, 5, 0, 0, time.UTC),
		StartedAt:        time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		MaxBudgetUSD:     500_000,
		BudgetBucket:     budgetBucketJudgment,
	}
}

func insertRunForTest(t *testing.T, s *Store, challengeID int64, in insertRunInput) (*runRow, error) {
	t.Helper()
	var result *runRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := insertRun(context.Background(), tx, challengeID, in)
		result = r
		return err
	})
	return result, classifyReadWriteErr(err)
}

func loadRunForTest(t *testing.T, s *Store, id int64) (*runRow, error) {
	t.Helper()
	var result *runRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := loadRunByID(context.Background(), tx, id)
		result = r
		return err
	})
	return result, classifyReadWriteErr(err)
}

func loadActiveRunForTest(t *testing.T, s *Store, challengeID int64) (*runRow, error) {
	t.Helper()
	var result *runRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := loadActiveRunByChallengeID(context.Background(), tx, challengeID)
		result = r
		return err
	})
	return result, classifyReadWriteErr(err)
}

func updateRunEndForTest(t *testing.T, s *Store, id int64, in updateRunEndInput) (*runRow, error) {
	t.Helper()
	var result *runRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := updateRunEnd(context.Background(), tx, id, in)
		result = r
		return err
	})
	return result, classifyReadWriteErr(err)
}

func updateRunHeartbeatForTest(t *testing.T, s *Store, id int64, at time.Time) (*runRow, error) {
	t.Helper()
	var result *runRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := updateRunHeartbeat(context.Background(), tx, id, at)
		result = r
		return err
	})
	return result, classifyReadWriteErr(err)
}

func mustCreateChallengeInternalID(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		t.Fatalf("parseChallengeID(%q) failed", ch.ID)
	}
	return cid
}

// --- run: 作成・取得 ---

func TestInsertRun_CreatesJudgmentRunReadableByID(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "issue-1")

	in := validInsertRunInput()
	created, err := insertRunForTest(t, s, cid, in)
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	if created.ID == "" {
		t.Fatalf("insertRun() ID is empty")
	}
	if created.Kind != runKindJudgment || created.Judgment != judgmentJ1 {
		t.Fatalf("insertRun() Kind/Judgment = %v/%v, want judgment/J1", created.Kind, created.Judgment)
	}
	if created.ChallengeID != formatChallengeID(cid) {
		t.Fatalf("insertRun() ChallengeID = %q, want %q", created.ChallengeID, formatChallengeID(cid))
	}
	if created.Result != "" || created.EndedAt != nil {
		t.Fatalf("insertRun() Result/EndedAt = %q/%v, want unset (not ended)", created.Result, created.EndedAt)
	}
	if created.SessionIDMismatch || created.RateLimited {
		t.Fatalf("insertRun() SessionIDMismatch/RateLimited = %v/%v, want false/false", created.SessionIDMismatch, created.RateLimited)
	}
	if created.CycleID != nil || created.PlanVersion != nil || created.ResumedFromRunID != nil {
		t.Fatalf("insertRun() nullable refs should be nil by default, got CycleID=%v PlanVersion=%v ResumedFromRunID=%v",
			created.CycleID, created.PlanVersion, created.ResumedFromRunID)
	}
	// 入力の値がそのまま保存されていることを、insertRun の返り値ではなく
	// 入力（in）と突き合わせて確認する（insertRun の返り値どうしを比べるだけ
	// では、INSERT の列と値の対応が入れ替わっていても検出できない）。
	if created.ChallengeVersion != in.ChallengeVersion || created.SessionID != in.SessionID ||
		created.PID != in.PID || created.Host != in.Host || created.MaxBudgetUSD != in.MaxBudgetUSD ||
		created.BudgetBucket != in.BudgetBucket {
		t.Fatalf("insertRun() = %+v, want values from input %+v", created, in)
	}
	if !created.HeartbeatAt.Equal(in.HeartbeatAt) || !created.StartedAt.Equal(in.StartedAt) {
		t.Fatalf("insertRun() HeartbeatAt/StartedAt = %v/%v, want %v/%v (異なる値にして取り違えを検出する)",
			created.HeartbeatAt, created.StartedAt, in.HeartbeatAt, in.StartedAt)
	}

	got, err := loadRunForTest(t, s, mustParseRunIDForTest(t, created.ID))
	if err != nil {
		t.Fatalf("loadRunByID() error = %v", err)
	}
	if got == nil || *got != *created {
		t.Fatalf("loadRunByID() = %+v, want %+v", got, created)
	}
}

func TestInsertRun_CreatesDelegateRunWithNullJudgmentAndOptionalRefs(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "issue-2")
	planVersion := int64(3)

	in := validInsertRunInput()
	in.Kind = runKindDelegate
	in.Judgment = ""
	in.PlanVersion = &planVersion

	created, err := insertRunForTest(t, s, cid, in)
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	if created.Kind != runKindDelegate || created.Judgment != "" {
		t.Fatalf("insertRun() Kind/Judgment = %v/%q, want delegate/\"\"", created.Kind, created.Judgment)
	}
	if created.PlanVersion == nil || *created.PlanVersion != planVersion {
		t.Fatalf("insertRun() PlanVersion = %v, want %d", created.PlanVersion, planVersion)
	}
}

func TestInsertRun_RejectsInvalidInput(t *testing.T) {
	cases := map[string]func(in *insertRunInput){
		"kind が閉集合外":               func(in *insertRunInput) { in.Kind = "other" },
		"judgment な run に判断点が無い":   func(in *insertRunInput) { in.Judgment = "" },
		"judgment な run に閉集合外の判断点": func(in *insertRunInput) { in.Judgment = "J9" },
		"delegate な run に判断点がある":   func(in *insertRunInput) { in.Kind = runKindDelegate; in.Judgment = judgmentJ1 },
		"budget_bucket が閉集合外":      func(in *insertRunInput) { in.BudgetBucket = "other" },
		"challenge_version が 0":    func(in *insertRunInput) { in.ChallengeVersion = 0 },
		"session_id が空":            func(in *insertRunInput) { in.SessionID = "" },
		"pid が 0":                  func(in *insertRunInput) { in.PID = 0 },
		"host が空":                  func(in *insertRunInput) { in.Host = "" },
		"heartbeat_at がゼロ値":        func(in *insertRunInput) { in.HeartbeatAt = time.Time{} },
		"started_at がゼロ値":          func(in *insertRunInput) { in.StartedAt = time.Time{} },
		"max_budget_usd が 0":       func(in *insertRunInput) { in.MaxBudgetUSD = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			cid := mustCreateChallengeInternalID(t, s, "invalid")
			in := validInsertRunInput()
			mutate(&in)
			if _, err := insertRunForTest(t, s, cid, in); !errors.Is(err, ErrValidation) {
				t.Fatalf("insertRun() error = %v, want ErrValidation", err)
			}
		})
	}
}

func mustParseRunIDForTest(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseRunID(id)
	if !ok {
		t.Fatalf("parseRunID(%q) failed", id)
	}
	return n
}

// --- run: AC-168 課題ごとの排他 ---

func TestInsertRun_RejectsSecondActiveRunForSameChallenge(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "one active run")

	first, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun(1回目) error = %v", err)
	}

	_, err = insertRunForTest(t, s, cid, validInsertRunInput())
	if !errors.Is(err, errActiveRunExists) {
		t.Fatalf("insertRun(2回目・同じ課題) error = %v, want errActiveRunExists (AC-168)", err)
	}

	// 拒否された 2 回目は残らない: 1 回目がそのまま読める。
	got, err := loadRunForTest(t, s, mustParseRunIDForTest(t, first.ID))
	if err != nil || got == nil {
		t.Fatalf("loadRunByID(1回目) = %v, %v, want the first run to remain", got, err)
	}
}

func TestInsertRun_AllowsActiveRunForDifferentChallenge(t *testing.T) {
	s := newStoreForTest(t)
	first := mustCreateChallengeInternalID(t, s, "first")
	second := mustCreateChallengeInternalID(t, s, "second")

	if _, err := insertRunForTest(t, s, first, validInsertRunInput()); err != nil {
		t.Fatalf("insertRun(first) error = %v", err)
	}
	if _, err := insertRunForTest(t, s, second, validInsertRunInput()); err != nil {
		t.Fatalf("insertRun(second) error = %v, want nil (別の課題は排他の対象外) (AC-168)", err)
	}
}

func TestInsertRun_AllowsNewActiveRunAfterFirstEnded(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "ends then new")

	first, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun(1回目) error = %v", err)
	}
	firstID := mustParseRunIDForTest(t, first.ID)

	if _, err := updateRunEndForTest(t, s, firstID, updateRunEndInput{
		EndedAt: time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC),
		Result:  runResultSucceeded,
	}); err != nil {
		t.Fatalf("updateRunEnd() error = %v", err)
	}

	if _, err := insertRunForTest(t, s, cid, validInsertRunInput()); err != nil {
		t.Fatalf("insertRun(1回目終了後の2回目) error = %v, want nil (AC-168)", err)
	}
}

func TestLoadActiveRunByChallengeID_ReturnsActiveRunAndNilAfterItEnds(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "active run lookup")

	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}

	active, err := loadActiveRunForTest(t, s, cid)
	if err != nil {
		t.Fatalf("loadActiveRunByChallengeID() error = %v", err)
	}
	if active == nil || active.ID != created.ID {
		t.Fatalf("loadActiveRunByChallengeID() = %+v, want the active run %s", active, created.ID)
	}

	if _, err := updateRunEndForTest(t, s, mustParseRunIDForTest(t, created.ID), updateRunEndInput{
		EndedAt: time.Now(), Result: runResultSucceeded,
	}); err != nil {
		t.Fatalf("updateRunEnd() error = %v", err)
	}

	active, err = loadActiveRunForTest(t, s, cid)
	if err != nil {
		t.Fatalf("loadActiveRunByChallengeID() after end error = %v", err)
	}
	if active != nil {
		t.Fatalf("loadActiveRunByChallengeID() after end = %+v, want nil (run が終了した)", active)
	}
}

func TestLoadActiveRunByChallengeID_ReturnsNilWhenChallengeHasNoRun(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "no run yet")

	active, err := loadActiveRunForTest(t, s, cid)
	if err != nil {
		t.Fatalf("loadActiveRunByChallengeID() error = %v", err)
	}
	if active != nil {
		t.Fatalf("loadActiveRunByChallengeID() = %+v, want nil", active)
	}
}

// --- run: 終了列の更新 ---

func TestUpdateRunEnd_SetsEndedAtResultAndCostFields(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "ends with cost")
	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	id := mustParseRunIDForTest(t, created.ID)

	endedAt := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	// CostUSD と ReportedTotalCostUSD は異なる値にする（--resume の delta 計算で
	// 両者は一般に異なる。同じ値だと列の取り違えが起きても検出できない）。
	got, err := updateRunEndForTest(t, s, id, updateRunEndInput{
		EndedAt:              endedAt,
		Result:               runResultSucceeded,
		RateLimited:          true,
		CostUSD:              int64Ptr(150_000),
		CostSource:           costSourceReported,
		ReportedTotalCostUSD: int64Ptr(400_000),
		Output:               `{"ok":true}`,
		Error:                "warn: retried once",
	})
	if err != nil {
		t.Fatalf("updateRunEnd() error = %v", err)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(endedAt) {
		t.Fatalf("updateRunEnd() EndedAt = %v, want %v", got.EndedAt, endedAt)
	}
	if got.Result != runResultSucceeded {
		t.Fatalf("updateRunEnd() Result = %q, want succeeded", got.Result)
	}
	if !got.RateLimited {
		t.Fatalf("updateRunEnd() RateLimited = false, want true")
	}
	if got.CostUSD == nil || *got.CostUSD != 150_000 {
		t.Fatalf("updateRunEnd() CostUSD = %v, want 150000", got.CostUSD)
	}
	if got.CostSource != costSourceReported {
		t.Fatalf("updateRunEnd() CostSource = %q, want reported", got.CostSource)
	}
	if got.ReportedTotalCostUSD == nil || *got.ReportedTotalCostUSD != 400_000 {
		t.Fatalf("updateRunEnd() ReportedTotalCostUSD = %v, want 400000", got.ReportedTotalCostUSD)
	}
	if got.Output != `{"ok":true}` {
		t.Fatalf("updateRunEnd() Output = %q, want {\"ok\":true}", got.Output)
	}
	if got.Error != "warn: retried once" {
		t.Fatalf("updateRunEnd() Error = %q, want %q", got.Error, "warn: retried once")
	}
}

// TestUpdateRunEnd_LeavesErrorUnsetWhenEmpty は Error="" が NULL のまま
// 保持される（往復する）ことを確かめる（TestUpdateRunEnd_SetsEndedAtResultAndCostFields
// は非空の Error の書き込みを確かめるため、この境界はここで別に確かめる）。
// runRow.Error は NULL・"" のどちらも "" として読むため（scanRunRow が
// sql.NullString を経由する）、got.Error だけでは NULL であることまでは
// 確かめられない。生の列を直接 `error IS NULL` で読み、UPDATE が error 列自体を
// 書き換えなくなる／"" を書き込むように壊れる変異の両方を検出できるようにする。
func TestUpdateRunEnd_LeavesErrorUnsetWhenEmpty(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "no error")
	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	id := mustParseRunIDForTest(t, created.ID)

	got, err := updateRunEndForTest(t, s, id, updateRunEndInput{
		EndedAt: time.Now(),
		Result:  runResultSucceeded,
		Error:   "",
	})
	if err != nil {
		t.Fatalf("updateRunEnd() error = %v", err)
	}
	if got.Error != "" {
		t.Fatalf("updateRunEnd() Error = %q, want \"\" (NULL)", got.Error)
	}
	if isNull := isRunErrorColumnNullForTest(t, s, id); !isNull {
		t.Fatalf("run.error column = non-NULL, want NULL (Error=\"\" は NULL として保存される)")
	}
}

// isRunErrorColumnNullForTest は id の run の生の error 列が NULL かどうかを
// 直接読む（runRow.Error の "" は NULL と "" の両方を表しうるため、NULL 性
// そのものはこのヘルパーで別に確かめる）。
func isRunErrorColumnNullForTest(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	var isNull bool
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT error IS NULL FROM run WHERE id = ?`, id).Scan(&isNull)
	})
	if err != nil {
		t.Fatalf("isRunErrorColumnNullForTest() error = %v", err)
	}
	return isNull
}

func TestUpdateRunEnd_RejectsMissingOrInvalidResult(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "invalid end")
	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	id := mustParseRunIDForTest(t, created.ID)

	cases := map[string]updateRunEndInput{
		"result が空":         {EndedAt: time.Now(), Result: ""},
		"result が閉集合外":      {EndedAt: time.Now(), Result: "not_a_result"},
		"ended_at がゼロ値":     {Result: runResultErrored},
		"cost_source が閉集合外": {EndedAt: time.Now(), Result: runResultSucceeded, CostSource: "not_a_source"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := updateRunEndForTest(t, s, id, in); !errors.Is(err, ErrValidation) {
				t.Fatalf("updateRunEnd() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestUpdateRunEnd_ReturnsNilForMissingRun(t *testing.T) {
	s := newStoreForTest(t)
	got, err := updateRunEndForTest(t, s, 999999, updateRunEndInput{
		EndedAt: time.Now(),
		Result:  runResultErrored,
	})
	if err != nil {
		t.Fatalf("updateRunEnd() error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("updateRunEnd() = %+v, want nil (run が存在しない)", got)
	}
}

// --- run: heartbeat ---

func TestUpdateRunHeartbeat_UpdatesHeartbeatAt(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "heartbeat")
	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	id := mustParseRunIDForTest(t, created.ID)

	next := created.HeartbeatAt.Add(60 * time.Second)
	got, err := updateRunHeartbeatForTest(t, s, id, next)
	if err != nil {
		t.Fatalf("updateRunHeartbeat() error = %v", err)
	}
	if !got.HeartbeatAt.Equal(next) {
		t.Fatalf("updateRunHeartbeat() HeartbeatAt = %v, want %v", got.HeartbeatAt, next)
	}
}

func TestUpdateRunHeartbeat_RejectsZeroTime(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "heartbeat invalid")
	created, err := insertRunForTest(t, s, cid, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun() error = %v", err)
	}
	id := mustParseRunIDForTest(t, created.ID)
	if _, err := updateRunHeartbeatForTest(t, s, id, time.Time{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("updateRunHeartbeat() error = %v, want ErrValidation", err)
	}
}

// --- cycle ---

func insertCycleForTest(t *testing.T, s *Store, in insertCycleInput) (*cycleRow, error) {
	t.Helper()
	var result *cycleRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		c, err := insertCycle(context.Background(), tx, in)
		result = c
		return err
	})
	return result, classifyReadWriteErr(err)
}

func updateCycleEndForTest(t *testing.T, s *Store, id int64, in updateCycleEndInput) (*cycleRow, error) {
	t.Helper()
	var result *cycleRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		c, err := updateCycleEnd(context.Background(), tx, id, in)
		result = c
		return err
	})
	return result, classifyReadWriteErr(err)
}

func mustParseCycleIDForTest(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseCycleID(id)
	if !ok {
		t.Fatalf("parseCycleID(%q) failed", id)
	}
	return n
}

func TestInsertCycle_CreatesCycleReadableByID(t *testing.T) {
	s := newStoreForTest(t)
	startedAt := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	created, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "cron", StartedAt: startedAt, BudgetUSD: 1_000_000})
	if err != nil {
		t.Fatalf("insertCycle() error = %v", err)
	}
	if created.Trigger != "cron" || created.BudgetUSD != 1_000_000 || created.SpentUSD != 0 {
		t.Fatalf("insertCycle() = %+v, want Trigger=cron BudgetUSD=1000000 SpentUSD=0", created)
	}
	if created.Result != "" || created.EndedAt != nil {
		t.Fatalf("insertCycle() Result/EndedAt = %q/%v, want unset", created.Result, created.EndedAt)
	}

	got, err := updateCycleEndForTest(t, s, mustParseCycleIDForTest(t, created.ID), updateCycleEndInput{
		EndedAt: startedAt.Add(time.Hour),
		Result:  cycleResultCompleted,
	})
	if err != nil {
		t.Fatalf("updateCycleEnd() error = %v", err)
	}
	if got.Result != cycleResultCompleted || got.EndedAt == nil {
		t.Fatalf("updateCycleEnd() = %+v, want Result=completed with EndedAt set", got)
	}
}

func TestInsertCycle_RejectsInvalidInput(t *testing.T) {
	cases := map[string]insertCycleInput{
		"trigger が空":      {Trigger: "", StartedAt: time.Now(), BudgetUSD: 1},
		"started_at がゼロ値": {Trigger: "cron", BudgetUSD: 1},
		"budget_usd が 0":  {Trigger: "cron", StartedAt: time.Now(), BudgetUSD: 0},
	}
	s := newStoreForTest(t)
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := insertCycleForTest(t, s, in); !errors.Is(err, ErrValidation) {
				t.Fatalf("insertCycle() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestUpdateCycleEnd_RejectsInvalidResult(t *testing.T) {
	s := newStoreForTest(t)
	created, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "cron", StartedAt: time.Now(), BudgetUSD: 1})
	if err != nil {
		t.Fatalf("insertCycle() error = %v", err)
	}
	id := mustParseCycleIDForTest(t, created.ID)
	if _, err := updateCycleEndForTest(t, s, id, updateCycleEndInput{EndedAt: time.Now(), Result: "not_a_result"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("updateCycleEnd() error = %v, want ErrValidation", err)
	}
}

// --- lock ---

func insertLockForTest(t *testing.T, s *Store, in insertLockInput) (*lockRow, error) {
	t.Helper()
	var result *lockRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		l, err := insertLock(context.Background(), tx, in)
		result = l
		return err
	})
	return result, classifyReadWriteErr(err)
}

func updateLockHeartbeatForTest(t *testing.T, s *Store, name string, at time.Time) (*lockRow, error) {
	t.Helper()
	var result *lockRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		l, err := updateLockHeartbeat(context.Background(), tx, name, at)
		result = l
		return err
	})
	return result, classifyReadWriteErr(err)
}

func deleteLockForTest(t *testing.T, s *Store, name string) error {
	t.Helper()
	return classifyReadWriteErr(s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return deleteLock(context.Background(), tx, name)
	}))
}

func loadLockForTest(t *testing.T, s *Store, name string) (*lockRow, error) {
	t.Helper()
	var result *lockRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		l, err := loadLockByName(context.Background(), tx, name)
		result = l
		return err
	})
	return result, classifyReadWriteErr(err)
}

func TestInsertLock_CreatesAndReadsAndHeartbeatsAndDeletes(t *testing.T) {
	s := newStoreForTest(t)
	cycle, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "cron", StartedAt: time.Now(), BudgetUSD: 1})
	if err != nil {
		t.Fatalf("insertCycle() error = %v", err)
	}
	holder := mustParseCycleIDForTest(t, cycle.ID)
	acquiredAt := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

	created, err := insertLockForTest(t, s, insertLockInput{
		Name: "cycle", Holder: holder, PID: 1234, Host: "host-a", AcquiredAt: acquiredAt, HeartbeatAt: acquiredAt,
	})
	if err != nil {
		t.Fatalf("insertLock() error = %v", err)
	}
	if created.Name != "cycle" || created.Holder != cycle.ID {
		t.Fatalf("insertLock() = %+v, want Name=cycle Holder=%s", created, cycle.ID)
	}

	got, err := loadLockForTest(t, s, "cycle")
	if err != nil || got == nil || *got != *created {
		t.Fatalf("loadLockByName() = %+v, %v, want %+v, nil", got, err, created)
	}

	next := acquiredAt.Add(60 * time.Second)
	updated, err := updateLockHeartbeatForTest(t, s, "cycle", next)
	if err != nil {
		t.Fatalf("updateLockHeartbeat() error = %v", err)
	}
	if !updated.HeartbeatAt.Equal(next) {
		t.Fatalf("updateLockHeartbeat() HeartbeatAt = %v, want %v", updated.HeartbeatAt, next)
	}

	if err := deleteLockForTest(t, s, "cycle"); err != nil {
		t.Fatalf("deleteLock() error = %v", err)
	}
	got, err = loadLockForTest(t, s, "cycle")
	if err != nil || got != nil {
		t.Fatalf("loadLockByName() after delete = %+v, %v, want nil, nil", got, err)
	}

	// 削除済みの名前は再取得できる（insertLock の重複は #79・#80 が扱う）。
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: "cycle", Holder: holder, PID: 1234, Host: "host-a", AcquiredAt: acquiredAt, HeartbeatAt: acquiredAt,
	}); err != nil {
		t.Fatalf("insertLock(削除後の再作成) error = %v", err)
	}
}

func TestInsertLock_RejectsDuplicateName(t *testing.T) {
	s := newStoreForTest(t)
	cycle, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "cron", StartedAt: time.Now(), BudgetUSD: 1})
	if err != nil {
		t.Fatalf("insertCycle() error = %v", err)
	}
	holder := mustParseCycleIDForTest(t, cycle.ID)
	at := time.Now()

	if _, err := insertLockForTest(t, s, insertLockInput{Name: "cycle", Holder: holder, PID: 1, Host: "h", AcquiredAt: at, HeartbeatAt: at}); err != nil {
		t.Fatalf("insertLock(1回目) error = %v", err)
	}
	if _, err := insertLockForTest(t, s, insertLockInput{Name: "cycle", Holder: holder, PID: 2, Host: "h", AcquiredAt: at, HeartbeatAt: at}); err == nil {
		t.Fatalf("insertLock(2回目・同じ name) error = nil, want a constraint violation")
	}
}

func TestInsertLock_RejectsInvalidInput(t *testing.T) {
	s := newStoreForTest(t)
	cycle, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "cron", StartedAt: time.Now(), BudgetUSD: 1})
	if err != nil {
		t.Fatalf("insertCycle() error = %v", err)
	}
	holder := mustParseCycleIDForTest(t, cycle.ID)
	at := time.Now()
	valid := insertLockInput{Name: "cycle", Holder: holder, PID: 1, Host: "h", AcquiredAt: at, HeartbeatAt: at}

	cases := map[string]func(in *insertLockInput){
		"name が空":           func(in *insertLockInput) { in.Name = "" },
		"holder が 0":        func(in *insertLockInput) { in.Holder = 0 },
		"pid が 0":           func(in *insertLockInput) { in.PID = 0 },
		"host が空":           func(in *insertLockInput) { in.Host = "" },
		"acquired_at がゼロ値":  func(in *insertLockInput) { in.AcquiredAt = time.Time{} },
		"heartbeat_at がゼロ値": func(in *insertLockInput) { in.HeartbeatAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := valid
			mutate(&in)
			if _, err := insertLockForTest(t, s, in); !errors.Is(err, ErrValidation) {
				t.Fatalf("insertLock() error = %v, want ErrValidation", err)
			}
		})
	}
}
