// Package coretest はテスト支援専用のヘルパーを持つ。internal/core 配下の
// パッケージ（Go の internal 規則で internal/core からしか import できない
// internal/core/internal/store を含む）だけが持つ知識に、internal/cli の
// テストからアクセスするために存在する。
//
// このパッケージは本番バイナリ（cmd/flywheel）からは import してはならない
// （internal/cli/depcheck_test.go が go list -deps で検査する）。*_test.go
// からだけ import すること。
package coretest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core/internal/store"
	_ "modernc.org/sqlite" // HoldRawWriteLock がテスト専用の生の接続を開くために使う
)

func dbPathFor(workspace string) string {
	return filepath.Join(workspace, ".flywheel", "flywheel.db")
}

func openExisting(t *testing.T, workspace string) *store.DB {
	t.Helper()
	migrations, err := store.Migrations()
	if err != nil {
		t.Fatalf("coretest: store.Migrations(): %v", err)
	}
	dbPath := dbPathFor(workspace)
	db, err := store.OpenExisting(dbPath, migrations)
	if err != nil {
		t.Fatalf("coretest: store.OpenExisting(%s): %v", dbPath, err)
	}
	return db
}

// SetStoreVersion は本番 API を経由せず、workspace 配下のストアへ直接
// PRAGMA user_version を書き込む。store_too_new のフィクスチャ（バイナリが
// 知らない将来の版）を作るためだけに使う。ワークスペースには既に
// core.Init 済みのストアがあること。
func SetStoreVersion(t *testing.T, workspace string, version int) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
		return err
	}); err != nil {
		t.Fatalf("coretest: SetStoreVersion(%d): %v", version, err)
	}
}

// InsertChallenge はテスト専用のフィクスチャとして課題を 1 件挿入する
// （internal/core の課題 CRUD の公開 API がまだ無い時点のテストが使う）。
func InsertChallenge(t *testing.T, workspace, title string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			title, "unclassified", "tester", "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
		)
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertChallenge(%q): %v", title, err)
	}
}

// InsertSourceBinding はテスト専用のフィクスチャとして source_binding を 1 件
// 挿入する（#56。internal/cli の show のテストが、まだ結線されていない
// `ingest` コマンド〔#59〕を経由せず、GitHub Issue から取り込まれた課題の形を
// 作るために使う）。challengeID は内部整数 ID（"C-1" なら 1）。
func InsertSourceBinding(t *testing.T, workspace string, challengeID int, sourceID, externalKey, url, fingerprint, upstreamState, policyState, at string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			challengeID, sourceID, externalKey, url, fingerprint, upstreamState, policyState, at, at,
		)
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertSourceBinding(challenge_id=%d): %v", challengeID, err)
	}
}

// SetSourceBindingObservation はテスト専用のフィクスチャとして、challengeID の
// 対応（source_binding）の観測値・読んだ時点の値を直接書き換える（#72。
// InsertSourceBinding が既定値（コメント数 0・更新日時未設定）で作った対応に、
// 未読の更新がある状態を作るために使う）。upstreamUpdatedAt・
// readUpstreamUpdatedAt が空文字列なら NULL（未設定）にする。
func SetSourceBindingObservation(t *testing.T, workspace string, challengeID int, commentsCount int, upstreamUpdatedAt string, readCommentsCount int, readUpstreamUpdatedAt string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE source_binding SET comments_count = ?, upstream_updated_at = ?, read_comments_count = ?, read_upstream_updated_at = ? WHERE challenge_id = ?`,
			commentsCount, nullable(upstreamUpdatedAt), readCommentsCount, nullable(readUpstreamUpdatedAt), challengeID,
		)
		return err
	}); err != nil {
		t.Fatalf("coretest: SetSourceBindingObservation(challenge_id=%d): %v", challengeID, err)
	}
}

// SetChallengeStatus は id（内部整数 ID。"C-1" なら 1）の課題の status を直接
// 書き換える。internal/cli のテストが、状態遷移コマンド（#10 で実装）を経由せず
// 完了（done）状態の課題を用意して edit の terminal_state を検証するために使う。
func SetChallengeStatus(t *testing.T, workspace string, id int, status string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE challenge SET status = ? WHERE id = ?", status, id)
		return err
	}); err != nil {
		t.Fatalf("coretest: SetChallengeStatus(%d, %q): %v", id, status, err)
	}
}

// InsertRunInput はテスト専用のフィクスチャとして run を1件挿入するための
// 値（#81。internal/cli の runs コマンドのテストが、まだ結線されていない
// 判断の呼び出し〈#84・#85〉を経由せず、run の一覧の表示を検証するために
// 使う）。EndedAt・Result・CostUSD・CostSource が空文字列/nilなら、その run は
// 終了していない（NULL）ものとして挿入する。金額はUSDの100万分の1を単位と
// する整数（docs/features/m3-invoker-delegation.md §クリティカル設計決定 1）。
type InsertRunInput struct {
	ChallengeID      int    // 内部整数ID（"C-1"なら1）
	CycleID          *int64 // #83。nilならNULL（周の外で記録されたrun）
	Kind             string
	Judgment         string // ""ならNULL（委譲。#81の時点では"judgment"のみ使う）
	ChallengeVersion int
	SessionID        string
	PID              int64
	Host             string
	HeartbeatAt      string
	StartedAt        string
	EndedAt          string
	Result           string
	RateLimited      bool
	MaxBudgetUSD     int64
	BudgetBucket     string
	CostUSD          *int64
	CostSource       string
}

// InsertRun は run を1件挿入し、割り当てられた内部整数IDを返す。
func InsertRun(t *testing.T, workspace string, in InsertRunInput) int64 {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()

	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}

	var cycleID any
	if in.CycleID != nil {
		cycleID = *in.CycleID
	}

	var id int64
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO run (cycle_id, challenge_id, kind, judgment, challenge_version, session_id,
				session_id_mismatch, pid, host, heartbeat_at, started_at, ended_at, result,
				rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source)
			 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cycleID, in.ChallengeID, in.Kind, nullable(in.Judgment), in.ChallengeVersion, in.SessionID,
			in.PID, in.Host, in.HeartbeatAt, in.StartedAt, nullable(in.EndedAt), nullable(in.Result),
			in.RateLimited, in.MaxBudgetUSD, in.BudgetBucket, in.CostUSD, nullable(in.CostSource),
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertRun: %v", err)
	}
	return id
}

// InsertCycle はテスト専用のフィクスチャとして cycle を1件挿入し、割り当て
// られた内部整数IDを返す（#83。internal/cli の runs コマンドのテストが、
// まだ結線されていない `flywheel cycle`〈#86〉を経由せず、cycle_id を持つ
// run の cycle_budget_usd の表示を検証するために使う）。budgetUSDMicros は
// USDの100万分の1を単位とする整数。
func InsertCycle(t *testing.T, workspace, trigger string, budgetUSDMicros int64, startedAt string) int64 {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()

	var id int64
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO cycle (trigger, started_at, ended_at, result, budget_usd, spent_usd) VALUES (?, ?, NULL, NULL, ?, 0)`,
			trigger, startedAt, budgetUSDMicros,
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertCycle: %v", err)
	}
	return id
}

// InsertLock はテスト専用のフィクスチャとして、サイクルの排他ロック（lock 表の
// name="cycle" の行）を、周 holderCycleID（InsertCycle が返した内部整数ID）が
// 保持している状態で挿入する（#86。internal/cli の cycle のテストが、生きている
// 保持者〈自プロセスの pid・現ホスト〉と stale な保持者〈死んだ pid・古い
// heartbeat〉のいずれも、別プロセスを起動せずに作るために使う）。pid・host・
// acquiredAt・heartbeatAt は保持者の記録そのもの（時刻は "2026-09-28T00:00:00.000Z"
// の形式）。
func InsertLock(t *testing.T, workspace string, holderCycleID int64, pid int64, host, acquiredAt, heartbeatAt string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO lock (name, holder, pid, host, acquired_at, heartbeat_at) VALUES ('cycle', ?, ?, ?, ?, ?)`,
			holderCycleID, pid, host, acquiredAt, heartbeatAt,
		)
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertLock: %v", err)
	}
}

// CycleLockHolder は lock 表の name="cycle" の行の保持者の周の内部整数IDを返す
// （行が無ければ 0, false）。
func CycleLockHolder(t *testing.T, workspace string) (int64, bool) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var holder int64
	found := false
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT holder FROM lock WHERE name = 'cycle'`).Scan(&holder)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	}); err != nil {
		t.Fatalf("coretest: CycleLockHolder: %v", err)
	}
	return holder, found
}

// CycleResultOf は周 cycleID（内部整数ID）の result と ended_at が NULL でないかを
// 返す（result が NULL なら ""）。
func CycleResultOf(t *testing.T, workspace string, cycleID int64) (result string, ended bool) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var res, endedAt sql.NullString
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT result, ended_at FROM cycle WHERE id = ?`, cycleID).Scan(&res, &endedAt)
	}); err != nil {
		t.Fatalf("coretest: CycleResultOf: %v", err)
	}
	return res.String, endedAt.Valid
}

// CountChallenges は workspace 配下のストアにある課題の件数を返す。
func CountChallenges(t *testing.T, workspace string) int {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var count int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM challenge").Scan(&count)
	}); err != nil {
		t.Fatalf("coretest: CountChallenges: %v", err)
	}
	return count
}

// HoldRawWriteLock は dbPath を生の database/sql 接続で開き（WAL・
// foreign_keys 等は設定しない。store.Open を経由しない）、BEGIN IMMEDIATE の
// トランザクションを stdin が閉じられるまで保持し続ける。
//
// internal/cli の busy テスト（別プロセスが書き込みロックを保持している間の
// store_busy の検証）が、modernc.org/sqlite を直接 import せずに「初めて
// 開かれる（journal_mode がまだ WAL 化されていない）ファイルへ、他プロセスが
// 先に BEGIN IMMEDIATE で書き込みロックを取っている」状況を再現するために使う。
// 標準出力へ "ready\n" を書いた時点でロックを取得済みであることを示す。
// 戻り値はプロセスの終了コードとして使うことを想定する。
func HoldRawWriteLock(dbPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "coretest.HoldRawWriteLock: open:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	tx, err := db.Begin()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "coretest.HoldRawWriteLock: begin immediate:", err)
		return 1
	}

	if _, err := fmt.Fprint(stdout, "ready\n"); err != nil {
		return 1
	}

	// 親プロセスが停止させる（stdin を閉じる）まで、書き込みロックを保持し続ける。
	_, _ = io.Copy(io.Discard, stdin)

	_ = tx.Rollback()
	return 0
}

// InsertSlot はテスト専用のフィクスチャとしてスロットを 1 件挿入し、割り当てられた
// 内部整数 ID を返す（#101。internal/cli の `slot clear`・`status` のテストが、
// git を使わずに needs_attention のスロットを用意するために使う）。state が
// needs_attention のとき reason を attention_reason に入れる（それ以外では無視）。
func InsertSlot(t *testing.T, workspace, repo, provider, path, state, reason string) int64 {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var reasonCol any
	if state == "needs_attention" {
		reasonCol = reason
	}
	var id int64
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO slot (repo, provider, path, state, run_id, attention_reason) VALUES (?, ?, ?, ?, NULL, ?)`,
			repo, provider, path, state, reasonCol)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertSlot: %v", err)
	}
	return id
}

// EndRun はテスト専用のフィクスチャとして run（表示形 "R-<n>"）を終了させる
// （result = succeeded。#101。スロットの解放は run の終了の後に行う〈idx_run_active_slot〉
// 手順を、internal/cli のテストが core の公開 API だけで再現するために使う）。
func EndRun(t *testing.T, workspace, runID string) {
	t.Helper()
	var n int64
	if _, err := fmt.Sscanf(runID, "R-%d", &n); err != nil {
		t.Fatalf("coretest: EndRun: bad run ID %q", runID)
	}
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET result = 'succeeded', ended_at = '2026-10-02T00:00:00.000Z' WHERE id = ?`, n)
		return err
	}); err != nil {
		t.Fatalf("coretest: EndRun: %v", err)
	}
}

// InsertApprovedPlan はテスト専用のフィクスチャとして、challengeID（内部整数 ID）の課題に
// 計画の版 1（body・spec。spec が空なら構造化した出力の無い計画）と、その版の計画の承認を
// 挿入し、課題を着手中にする（internal/cli の `run` のテストが、本人確認つきの承認を
// 経由せず、委譲の対象の課題を用意するために使う）。
func InsertApprovedPlan(t *testing.T, workspace string, challengeID int, body, spec string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var specCol any
	if spec != "" {
		specCol = spec
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 1, ?, ?, ?)`,
			challengeID, body, "2026-09-25T00:00:00.000Z", specCol); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			 VALUES (?, 'plan', 'approved', 1, 'tester', 'cli', 'tty_confirm', ?)`,
			challengeID, "2026-09-25T00:00:00.000Z"); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress', version = version + 1 WHERE id = ?`, challengeID)
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertApprovedPlan(%d): %v", challengeID, err)
	}
}

// InsertRunArtifact はテスト専用のフィクスチャとして、runID（内部整数 ID）の run の成果物を
// 1 件挿入する（kind は branch | pr | commit。state・base は PR のときだけ。空は NULL）。
// internal/cli の `verify --auto` のテストが、委譲の照合を経由せず、PR を成果物に持つ課題を
// 用意するために使う。
func InsertRunArtifact(t *testing.T, workspace string, runID int64, kind, ref, state, base string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO run_artifact (run_id, kind, ref, state, base, verified_at) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, kind, ref, nullable(state), nullable(base), "2026-10-02T00:00:00.000Z")
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertRunArtifact: %v", err)
	}
}
