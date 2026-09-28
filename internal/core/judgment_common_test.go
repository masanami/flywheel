package core

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// このファイルは judgment_j1_test.go・judgment_common_test.go 自身が使う、
// run・source_binding・activity・hold の行を直接読み書きするテスト専用の
// ヘルパーを持つ（internal/core/coretest と同じ意図だが、こちらは
// internal/core パッケージ自身のテストなので非公開の型・関数へ直接アクセス
// できる。coretest 側の対応する型〈InsertRunInput 等〉は文字列ベースの
// 公開の形であり、ここでは judgmentPoint 等の非公開の閉集合型をそのまま
// 使えるほうが J1 のテストに都合がよいため複製する）。

func mustChallengeInternalID(t *testing.T, display string) int64 {
	t.Helper()
	id, ok := parseChallengeID(display)
	if !ok {
		t.Fatalf("parseChallengeID(%q) failed", display)
	}
	return id
}

func containsInt64(xs []int64, v int64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func timeoutChan(t *testing.T, seconds int) <-chan time.Time {
	t.Helper()
	return time.After(time.Duration(seconds) * time.Second)
}

func listJ1AutoTargetsForTest(t *testing.T, s *Store) []int64 {
	t.Helper()
	var ids []int64
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		got, err := selectJ1AutoTargets(context.Background(), tx)
		ids = got
		return err
	})
	if err != nil {
		t.Fatalf("selectJ1AutoTargets: %v", err)
	}
	return ids
}

func bindSourceForTest(t *testing.T, s *Store, challengeDisplayID, upstream, policy string) {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertSourceBinding(context.Background(), tx, time.Now(), cid, createSourceBindingInput{
			SourceID: "src", ExternalKey: fmt.Sprintf("o/r#%d", cid), URL: "https://example.com/issues/" + fmt.Sprint(cid),
			Fingerprint: "1:abc", UpstreamState: upstreamState(upstream), PolicyState: policyState(policy),
		})
		return err
	})
	if err != nil {
		t.Fatalf("insertSourceBinding: %v", err)
	}
}

// insertFakeRunForTest は challengeDisplayID の課題に、既に終了した
// （succeeded）run を1件挿入し、その内部整数IDを返す。
func insertFakeRunForTest(t *testing.T, s *Store, challengeDisplayID string, judgment judgmentPoint) int64 {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	var runID int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		row, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgment, ChallengeVersion: 1, SessionID: "11111111-1111-1111-1111-111111111111",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("bad run id %q", row.ID)
		}
		runID = n
		cost := int64(0)
		_, err = updateRunEnd(context.Background(), tx, runID, updateRunEndInput{EndedAt: now, Result: runResultSucceeded, CostUSD: &cost, CostSource: costSourceReported})
		return err
	})
	if err != nil {
		t.Fatalf("insertFakeRunForTest: %v", err)
	}
	return runID
}

// insertActiveFakeRunForTest は challengeDisplayID の課題に、終了していない
// run を1件挿入する（AC-69 の「終了していない run を持つ課題は対象外」の
// フィクスチャ）。
func insertActiveFakeRunForTest(t *testing.T, s *Store, challengeDisplayID string, judgment judgmentPoint) int64 {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	var runID int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		now := time.Now()
		row, err := insertRun(context.Background(), tx, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgment, ChallengeVersion: 1, SessionID: "22222222-2222-2222-2222-222222222222",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketJudgment,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("bad run id %q", row.ID)
		}
		runID = n
		return nil
	})
	if err != nil {
		t.Fatalf("insertActiveFakeRunForTest: %v", err)
	}
	return runID
}

type activityRowForTest struct {
	channel      string
	verification string
	runID        *int64
}

func queryLastActivityForTest(t *testing.T, s *Store, challengeDisplayID string) activityRowForTest {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	var out activityRowForTest
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		row := tx.QueryRowContext(context.Background(),
			`SELECT channel, verification, run_id FROM activity WHERE entity='challenge' AND entity_id=? ORDER BY id DESC LIMIT 1`, cid)
		var runID sql.NullInt64
		if err := row.Scan(&out.channel, &out.verification, &runID); err != nil {
			return err
		}
		if runID.Valid {
			v := runID.Int64
			out.runID = &v
		}
		return nil
	})
	if err != nil {
		t.Fatalf("queryLastActivityForTest: %v", err)
	}
	return out
}

func queryHoldRunIDForTest(t *testing.T, s *Store, challengeDisplayID string) *int64 {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeDisplayID)
	var out *int64
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		row := tx.QueryRowContext(context.Background(), `SELECT run_id FROM hold WHERE challenge_id=? ORDER BY id DESC LIMIT 1`, cid)
		var runID sql.NullInt64
		if err := row.Scan(&runID); err != nil {
			return err
		}
		if runID.Valid {
			v := runID.Int64
			out = &v
		}
		return nil
	})
	if err != nil {
		t.Fatalf("queryHoldRunIDForTest: %v", err)
	}
	return out
}
