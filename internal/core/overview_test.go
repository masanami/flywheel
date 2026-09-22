package core

import (
	"context"
	"database/sql"
	"testing"
)

// このファイルは `flywheel status`（Issue #14）の core 側 API GetOverview を
// 検証する。GetOverview は読み取り専用（状態・作業ログを一切変えない）で、
// 課題を「人間の操作を待っているもの」（計画承認待ち・完了確認待ち・
// 人間対応待ち）と「システムが次に進められるもの」（未分類・分類済・着手中・
// 検証中）に、不可逆操作を「未承認（pending）」と「承認済み（approved）」に
// 分ける（docs/features/m1-core.md §状況の表示）。完了（done）の課題と
// 差し戻し済み（rejected）の不可逆操作はどちらの区分にも出さない。

// --- 空のストア ---

func TestGetOverview_EmptyStoreReturnsEmptyListsForAllFourBuckets(t *testing.T) {
	s := newStoreForTest(t)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.NeedsHumanChallenges) != 0 {
		t.Errorf("NeedsHumanChallenges = %+v, want empty", ov.NeedsHumanChallenges)
	}
	if len(ov.NeedsHumanOperations) != 0 {
		t.Errorf("NeedsHumanOperations = %+v, want empty", ov.NeedsHumanOperations)
	}
	if len(ov.ActionableChallenges) != 0 {
		t.Errorf("ActionableChallenges = %+v, want empty", ov.ActionableChallenges)
	}
	if len(ov.ApprovedOperations) != 0 {
		t.Errorf("ApprovedOperations = %+v, want empty", ov.ApprovedOperations)
	}
}

// --- 課題の8状態すべての振り分け ---

func TestGetOverview_ChallengeStatusClassification(t *testing.T) {
	// bucket は課題がどの区分に出るべきかを表す（"human"|"actionable"|"none"）。
	tests := []struct {
		status Status
		bucket string
	}{
		{StatusUnclassified, "actionable"},
		{StatusClassified, "actionable"},
		{StatusAwaitingPlanApproval, "human"},
		{StatusInProgress, "actionable"},
		{StatusVerifying, "actionable"},
		{StatusAwaitingCompletionApproval, "human"},
		{StatusDone, "none"},
		{StatusAwaitingHuman, "human"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, tt.status)

			ov, err := s.GetOverview(context.Background())
			if err != nil {
				t.Fatalf("GetOverview() error = %v", err)
			}

			inHuman := containsChallengeID(ov.NeedsHumanChallenges, c.ID)
			inActionable := containsChallengeID(ov.ActionableChallenges, c.ID)

			switch tt.bucket {
			case "human":
				if !inHuman {
					t.Errorf("status=%s: want in NeedsHumanChallenges, got human=%v actionable=%v", tt.status, inHuman, inActionable)
				}
				if inActionable {
					t.Errorf("status=%s: unexpectedly in ActionableChallenges", tt.status)
				}
			case "actionable":
				if !inActionable {
					t.Errorf("status=%s: want in ActionableChallenges, got human=%v actionable=%v", tt.status, inHuman, inActionable)
				}
				if inHuman {
					t.Errorf("status=%s: unexpectedly in NeedsHumanChallenges", tt.status)
				}
			case "none":
				if inHuman || inActionable {
					t.Errorf("status=%s: want in neither bucket, got human=%v actionable=%v", tt.status, inHuman, inActionable)
				}
			}
		})
	}
}

func containsChallengeID(challenges []Challenge, id string) bool {
	for _, c := range challenges {
		if c.ID == id {
			return true
		}
	}
	return false
}

// TestOverviewBuckets_CoverStatusVocabularyExactlyOnce は、needsHumanStatuses・
// actionableStatuses・StatusDone の3つを合わせると語彙8値をちょうど1回ずつ
// 覆うことを検査する（将来 StatusVocabulary に値が増えたとき、区分の定義を
// 更新し忘れて「どちらの区分にも現れない・両方に現れる」課題が黙って生じるのを防ぐ。
// docs/features/m1-core.md §状態機械 の閉包テストと同じ発想）。
func TestOverviewBuckets_CoverStatusVocabularyExactlyOnce(t *testing.T) {
	seen := map[Status]int{}
	for _, st := range needsHumanStatuses {
		seen[st]++
	}
	for _, st := range actionableStatuses {
		seen[st]++
	}
	seen[StatusDone]++

	if len(seen) != len(StatusVocabulary) {
		t.Fatalf("covered %d distinct statuses, want %d (StatusVocabulary)", len(seen), len(StatusVocabulary))
	}
	for _, entry := range StatusVocabulary {
		if seen[entry.Code] != 1 {
			t.Errorf("status %s covered %d times, want exactly 1", entry.Code, seen[entry.Code])
		}
	}
}

// --- 不可逆操作の状態による振り分け ---

func TestGetOverview_PendingOperationGoesToNeedsHumanOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if !containsOperationID(ov.NeedsHumanOperations, op.ID) {
		t.Errorf("NeedsHumanOperations = %+v, want to contain %s", ov.NeedsHumanOperations, op.ID)
	}
	if containsOperationID(ov.ApprovedOperations, op.ID) {
		t.Errorf("ApprovedOperations unexpectedly contains pending operation %s", op.ID)
	}
}

func TestGetOverview_ApprovedOperationGoesToApprovedOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att); err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if !containsOperationID(ov.ApprovedOperations, op.ID) {
		t.Errorf("ApprovedOperations = %+v, want to contain %s", ov.ApprovedOperations, op.ID)
	}
	if containsOperationID(ov.NeedsHumanOperations, op.ID) {
		t.Errorf("NeedsHumanOperations unexpectedly contains approved operation %s", op.ID)
	}
}

// AC: 差し戻し済み（rejected）の不可逆操作はどこにも出さない。
func TestGetOverview_RejectedOperationExcludedFromBothLists(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "delete")

	prev, err := s.PrepareOperationRejection(context.Background(), op.ID, "not needed")
	if err != nil {
		t.Fatalf("PrepareOperationRejection() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	reason := "not needed"
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, att); err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if containsOperationID(ov.NeedsHumanOperations, op.ID) {
		t.Errorf("NeedsHumanOperations unexpectedly contains rejected operation %s", op.ID)
	}
	if containsOperationID(ov.ApprovedOperations, op.ID) {
		t.Errorf("ApprovedOperations unexpectedly contains rejected operation %s", op.ID)
	}
}

func containsOperationID(ops []IrreversibleOperation, id string) bool {
	for _, o := range ops {
		if o.ID == id {
			return true
		}
	}
	return false
}

// --- 完了した課題は除外され、その課題に残る未承認の release だけが出る ---

func TestGetOverview_DoneChallengeExcludedButItsPendingOperationStillShown(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	setChallengeStatus(t, s, mustParseChallengeID(t, c.ID), StatusDone)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if containsChallengeID(ov.NeedsHumanChallenges, c.ID) || containsChallengeID(ov.ActionableChallenges, c.ID) {
		t.Errorf("done challenge %s unexpectedly present in a challenge bucket", c.ID)
	}
	if !containsOperationID(ov.NeedsHumanOperations, op.ID) {
		t.Errorf("pending operation %s of a done challenge must still be listed", op.ID)
	}
}

// --- 読み取り専用: 状態・作業ログを変えない ---

func TestGetOverview_DoesNotMutateChallengeVersionOrActivityLog(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	op := createPendingOperation(t, s, c.ID, "release")

	before, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() before error = %v", err)
	}
	beforeActs := activitiesFor(t, s, c.ID)

	if _, err := s.GetOverview(context.Background()); err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() after error = %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("challenge version changed: before=%d after=%d", before.Version, after.Version)
	}
	afterActs := activitiesFor(t, s, c.ID)
	if len(afterActs) != len(beforeActs) {
		t.Errorf("activity log changed: before=%d entries after=%d entries", len(beforeActs), len(afterActs))
	}

	// 不可逆操作の版も変わらないことを確認する（GetOverview は unrelated だが、
	// 「読み取りだけ」を op 側でも固定する）。found の有無を確認しないと、対象の
	// 操作が消えてしまった場合（まさに検出したい変更）でも1度も比較されずに
	// 通ってしまう（self-review 指摘）。
	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	var found *IrreversibleOperation
	for i, o := range detail.Operations {
		if o.ID == op.ID {
			found = &detail.Operations[i]
		}
	}
	if found == nil {
		t.Fatalf("operation %s not found in %+v", op.ID, detail.Operations)
	}
	if found.Version != 1 {
		t.Errorf("operation %s version = %d, want 1 (unchanged)", found.ID, found.Version)
	}
}

// --- 順序: id 昇順 ---

// insertChallengeWithID は生 SQL で id を明示指定して課題を1件挿入する
// （transition_exec_test.go の insertUnansweredHold と同じ手法）。
func insertChallengeWithID(t *testing.T, s *Store, id int64, title string, status Status) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO challenge (id, title, description, done_criteria, status, version, reporter, created_at, updated_at)
			 VALUES (?, ?, '', '', ?, 1, 'tester', '2026-09-21T00:00:00.000Z', '2026-09-21T00:00:00.000Z')`,
			id, title, string(status),
		)
		return err
	}); err != nil {
		t.Fatalf("insertChallengeWithID(%d, %q): %v", id, title, err)
	}
}

// enableReverseUnorderedSelectsForTest は、このストアの接続（プロセス内の書き込み
// 接続は1本に絞られる＝internal/core/internal/store/store.go の SetMaxOpenConns(1)）に
// `PRAGMA reverse_unordered_selects = ON` を設定する。以後、ORDER BY の無い問い合わせは
// SQLite の全表走査を逆順（降順）で行うようになる（ORDER BY を明示したクエリの結果には
// 影響しない）。TestGetOverview_ChallengesAndOperationsAreOrderedByIDAscending が、
// overview.go の ORDER BY id ASC が本当に必要であることを検出できるようにするために使う。
func enableReverseUnorderedSelectsForTest(t *testing.T, s *Store) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`PRAGMA reverse_unordered_selects = ON`)
		return err
	}); err != nil {
		t.Fatalf("enableReverseUnorderedSelectsForTest: %v", err)
	}
}

// insertOperationWithID は生 SQL で id を明示指定して不可逆操作を1件挿入する。
func insertOperationWithID(t *testing.T, s *Store, id, challengeID int64, state OperationState) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO operation (id, challenge_id, kind, summary, ref, state, version, created_at)
			 VALUES (?, ?, 'release', 's', NULL, ?, 1, '2026-09-21T00:00:00.000Z')`,
			id, challengeID, string(state),
		)
		return err
	}); err != nil {
		t.Fatalf("insertOperationWithID(%d, challenge=%d): %v", id, challengeID, err)
	}
}

// TestGetOverview_ChallengesAndOperationsAreOrderedByIDAscending は、課題・
// 不可逆操作のどちらも id 昇順で返ることを検証する（旧版は課題しか検証しておらず、
// 不可逆操作の順序は1度も検査されていなかった＝self-review 指摘）。
//
// `id INTEGER PRIMARY KEY AUTOINCREMENT` は SQLite の rowid そのものであり、索引の
// 無いテーブルの全表走査は既定で rowid（＝id）昇順に物理走査されるため、素朴に
// 「挿入順を id 昇順と逆にする」だけでは ORDER BY 句を消しても検出できない
// （self-review 2周目の指摘: 旧版のコメント「rowid 順は 5,2」は誤りで、実際は
// rowid も 2,5 だった）。このテストでは `PRAGMA reverse_unordered_selects = ON`
// （ORDER BY の無い問い合わせの全表走査を逆順にする SQLite の診断用 PRAGMA）を
// 明示的に有効化し、overview.go の ORDER BY id ASC が無ければこのテストが実際に
// 失敗する状態を作ってから検証する。
func TestGetOverview_ChallengesAndOperationsAreOrderedByIDAscending(t *testing.T) {
	s := newStoreForTest(t)
	enableReverseUnorderedSelectsForTest(t, s)

	// 課題を id 昇順（2, 5）とは逆の順（5, 2）で挿入する。
	insertChallengeWithID(t, s, 5, "second", StatusUnclassified)
	insertChallengeWithID(t, s, 2, "first", StatusUnclassified)

	// 不可逆操作も同様に、id=5, id=2 の順に、いずれも課題 C-2 に対して挿入する。
	insertOperationWithID(t, s, 5, 2, OperationStatePending)
	insertOperationWithID(t, s, 2, 2, OperationStatePending)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}

	var gotChallenges []string
	for _, c := range ov.ActionableChallenges {
		gotChallenges = append(gotChallenges, c.ID)
	}
	wantChallenges := []string{"C-2", "C-5"}
	if len(gotChallenges) != len(wantChallenges) {
		t.Fatalf("ActionableChallenges ids = %v, want %v", gotChallenges, wantChallenges)
	}
	for i := range wantChallenges {
		if gotChallenges[i] != wantChallenges[i] {
			t.Errorf("ActionableChallenges[%d] = %s, want %s (id ascending, not insertion order)", i, gotChallenges[i], wantChallenges[i])
		}
	}

	var gotOps []string
	for _, o := range ov.NeedsHumanOperations {
		gotOps = append(gotOps, o.ID)
	}
	wantOps := []string{"OP-2", "OP-5"}
	if len(gotOps) != len(wantOps) {
		t.Fatalf("NeedsHumanOperations ids = %v, want %v", gotOps, wantOps)
	}
	for i := range wantOps {
		if gotOps[i] != wantOps[i] {
			t.Errorf("NeedsHumanOperations[%d] = %s, want %s (id ascending, not insertion order)", i, gotOps[i], wantOps[i])
		}
	}
}
