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

// --- judgment_common.go 自身の検査（design-reviewer 指摘・round1 CONFIRMED:
// 共有の部品を J1 のヘルパー経由でしか触れていなかったため、優先度の並び順・
// 保留の複数件の並び順を単体で固定する） ---

func TestPriorityRank_OrdersP0ThenP1ThenP2ThenUnset(t *testing.T) {
	p0, p1, p2 := PriorityP0, PriorityP1, PriorityP2
	ranks := []int{priorityRank(&p0), priorityRank(&p1), priorityRank(&p2), priorityRank(nil)}
	for i := 1; i < len(ranks); i++ {
		if ranks[i-1] >= ranks[i] {
			t.Fatalf("priorityRank ranks = %v, want strictly increasing (P0 < P1 < P2 < unset)", ranks)
		}
	}
}

// TestLoadChallengesByStatusSorted_OrdersByPriorityThenID は
// §一括の操作（サイクル）「各段の対象は、優先度 P0・P1・P2・未設定の順、
// 同じ優先度では ID の昇順で処理する」を固定する。J1 の対象（未分類）は
// 優先度が常に未設定なので、この並び順は #85 の J2（分類済の課題。優先度が
// 混在する）で初めて意味を持つ。
func TestLoadChallengesByStatusSorted_OrdersByPriorityThenID(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()

	// 作成順は P1・P0・P0・P2 にし、優先度でソートし直すことと、同じ優先度
	// （P0 の2件）では ID 昇順（＝作成順）が保たれることの両方を確かめる。
	titles := []string{"c-p1", "c-p0-a", "c-p0-b", "c-p2"}
	priorities := []string{"P1", "P0", "P0", "P2"}
	ids := make([]string, len(titles))
	for i, title := range titles {
		ch, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: title})
		if err != nil {
			t.Fatalf("CreateChallenge(%s): %v", title, err)
		}
		if _, err := s.ClassifyChallenge(ctx, ChannelCLI, ch.ID, ClassifyInput{Priority: priorities[i]}); err != nil {
			t.Fatalf("ClassifyChallenge(%s): %v", title, err)
		}
		ids[i] = ch.ID
	}

	var got []Challenge
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusClassified)
		got = challenges
		return err
	})
	if err != nil {
		t.Fatalf("loadChallengesByStatusSorted: %v", err)
	}

	wantOrder := []string{ids[1], ids[2], ids[0], ids[3]} // P0,P0(ID昇順),P1,P2
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d challenges, want %d", len(got), len(wantOrder))
	}
	for i, c := range got {
		if c.ID != wantOrder[i] {
			t.Errorf("order[%d] = %s, want %s (got order=%v, want=%v)", i, c.ID, wantOrder[i], challengeIDs(got), wantOrder)
		}
	}
}

func challengeIDs(cs []Challenge) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// TestFormatHoldsSection_MultipleHoldsAreOldestFirstAndBothIncluded は
// §判断点の共通の規則「判断点の入力に…すべての保留の問いと回答を古い順に
// 含める」を、保留が2件以上あるケースで固定する。
func TestFormatHoldsSection_MultipleHoldsAreOldestFirstAndBothIncluded(t *testing.T) {
	older := Hold{Question: "UNIQUE-OLDER-Q", Answer: strPtr("UNIQUE-OLDER-A")}
	newer := Hold{Question: "UNIQUE-NEWER-Q", Answer: strPtr("UNIQUE-NEWER-A")}

	got := formatHoldsSection([]Hold{older, newer})

	olderIdx := indexOfString(got, "UNIQUE-OLDER-Q")
	newerIdx := indexOfString(got, "UNIQUE-NEWER-Q")
	if olderIdx < 0 || newerIdx < 0 {
		t.Fatalf("formatHoldsSection output is missing a question: %q", got)
	}
	if olderIdx >= newerIdx {
		t.Errorf("formatHoldsSection did not keep the oldest-first order: %q", got)
	}
	for _, want := range []string{"UNIQUE-OLDER-A", "UNIQUE-NEWER-A"} {
		if !containsSubstring(got, want) {
			t.Errorf("formatHoldsSection output is missing %q: %q", want, got)
		}
	}
}
