package core

import (
	"context"
	"database/sql"
	"time"
)

// activityRecorder は mutate が渡す、1 回の書き込みトランザクションの中で
// 作業ログへ記録するためのヘルパー。fn は対象を変更した後、変わった項目ごとに
// record を呼ぶ（変更が無ければ呼ばない＝値を変えない edit は作業ログを
// 残さない）。
type activityRecorder struct {
	tx           *sql.Tx
	at           time.Time
	actor        string
	channel      Channel
	verification Verification
	insert       func(tx *sql.Tx, row activityRow) error
	// runID は、この作業ログの原因になった run（判断の呼び出し・委譲）の内部
	// 整数 ID（#84。docs/features/m3-invoker-delegation.md §判断点の共通の規則
	// 「判断点の出力による変更の作業ログは…run_id が原因の run の ID である」）。
	// nil なら run_id 列は NULL（#9・#10・#12 の既存の呼び出し元は全て nil の
	// まま。mutateAs はこのフィールドを設定しない）。
	runID *int64
}

// record は entity（"challenge" 等）・entityID・action（"create"／"edit" 等）と、
// 変わった項目だけを持つ before／after の JSON オブジェクト（H7）を作業ログへ
// 追加する。create のように変更前が無い操作は before に nil を渡す。
// 同じトランザクションの中で実行されるため、この呼び出しが失敗すれば
// mutate 全体がロールバックされ、対象の変更も作業ログも残らない。
func (r *activityRecorder) record(entity string, entityID int64, action string, before, after map[string]any) error {
	beforeVal, err := marshalDiffField(before)
	if err != nil {
		return err
	}
	afterVal, err := marshalDiffField(after)
	if err != nil {
		return err
	}

	row := activityRow{
		At:           formatTimestamp(r.at),
		Actor:        r.actor,
		Channel:      string(r.channel),
		Verification: string(r.verification),
		Entity:       entity,
		EntityID:     entityID,
		Action:       action,
		Before:       beforeVal,
		After:        afterVal,
		RunID:        r.runID,
	}

	insert := r.insert
	if insert == nil {
		insert = defaultInsertActivity
	}
	return insert(r.tx, row)
}

// mutate は fn を 1 つの書き込みトランザクション（store.DB.Write が
// BEGIN IMMEDIATE で開始する）の中で実行する共通ヘルパーである。
//
// 使い方:
//  1. actor を解決する（本人確認の無い操作は core 内部で Verification を固定する。
//     #9 の Create/Edit は VerificationNone）。actor を解決できなければ
//     ErrActorUnavailable を返し、トランザクションは開かない（何も変更しない）。
//  2. トランザクションを開き、activityRecorder を組み立てて fn を呼ぶ。fn は
//     対象（課題など）を変更し、変わった項目があれば rec.record(...) で
//     作業ログの行を積む。
//  3. fn がエラーを返す、または record の内部の INSERT が失敗すれば、
//     store.DB.Write がロールバックする（対象の変更も作業ログの追加も
//     どちらも残らない＝失敗注入テストが検証する）。
//
// 本人確認の無い操作（#9・#10）は resolveActor() で actor を解決し
// Verification を VerificationNone に固定する。本人確認つきの操作
// （#12: approve・reject・answer）は Attestation が持つ actor・channel・
// verification をそのまま使う必要があるため、その差し替え口として
// mutateAs を切り出す（mutate はその薄いラッパー）。
func (s *Store) mutate(ctx context.Context, ch Channel, fn func(tx *sql.Tx, rec *activityRecorder) error) error {
	actor, err := resolveActor()
	if err != nil {
		return err
	}
	return s.mutateAs(ctx, actor, ch, VerificationNone, fn)
}

// mutateAs は mutate と同じ書き込みトランザクションの枠組みを、呼び出し側が
// actor・channel・verification を明示的に指定できる形で提供する
// （#12: 本人確認つきの操作は Attestation から得た値を使い、
// core 内部の resolveActor()／VerificationNone 固定を経由しない）。run_id は
// 持たない（既存の呼び出し元〈#9・#10・#12〉はどれも判断の呼び出しに由来しない）。
func (s *Store) mutateAs(ctx context.Context, actor string, ch Channel, verification Verification, fn func(tx *sql.Tx, rec *activityRecorder) error) error {
	return s.mutateAsRun(ctx, actor, ch, verification, nil, fn)
}

// mutateAsRun は mutateAs に runID（この作業ログの原因になった run の内部
// 整数 ID。nil なら run_id 列は NULL）を足せる形で提供する（#84。
// judgment_common.go・judgment_j1.go が、経路 ChannelInvoker・本人確認
// VerificationNone・run_id つきで課題を書き換えるために使う）。
func (s *Store) mutateAsRun(ctx context.Context, actor string, ch Channel, verification Verification, runID *int64, fn func(tx *sql.Tx, rec *activityRecorder) error) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		rec := &activityRecorder{
			tx:           tx,
			at:           s.currentTime(),
			actor:        actor,
			channel:      ch,
			verification: verification,
			insert:       s.insertActivity,
			runID:        runID,
		}
		if err := fn(tx, rec); err != nil {
			return err
		}
		// beforeCommit はテスト専用（#10 AC-36 の並行テスト）。fn が成功した後、
		// store.DB.Write が実際にコミットする前に呼ぶことで、このトランザクションに
		// BEGIN IMMEDIATE の書き込みロックを保持させたまま任意の間だけ止められる。
		if s.beforeCommit != nil {
			s.beforeCommit()
		}
		return nil
	})
	// classifyReadWriteErr は SQLITE_BUSY 等の低レベルのストアエラーだけを
	// ErrStoreBusy 等へ写像し、fn が返したアプリケーションレベルの sentinel
	// （ErrNotFound・ErrValidation・ErrTerminalState 等）はそのまま通す
	// （レビュー指摘: これが無いと、別プロセスが書き込みロックを保持している間の
	// create/edit が store_busy ではなく internal_error になってしまう）。
	return classifyReadWriteErr(err)
}
