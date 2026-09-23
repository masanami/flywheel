package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// このファイルは状態機械の操作（classify・plan・submit・verify・hold）の
// 公開 API を持つ（Issue #10）。可否の判定は必ず transition.go の
// Table／Lookup／IsTerminal だけを見る（自前の if 分岐で遷移を再定義しない）。
//
// 5 つの公開メソッドはすべて内部の transition ヘルパーを通る（判定順は
// ④ IsTerminal → ⑤ Lookup → ⑥ Resolve を共通化するため）。入力の検証
// （閉集合・必須値）は各公開メソッドがトランザクションを開く前に行う。

// ClassifyInput は ClassifyChallenge の入力。
type ClassifyInput struct {
	Priority string
}

// PlanInput は PlanChallenge の入力。
type PlanInput struct {
	Body string
}

// VerifyInput は VerifyChallenge の入力。
type VerifyInput struct {
	Result   string
	Question *string // uncertain のときだけ必須。met/not_met では nil であること
}

// HoldInput は HoldChallenge の入力。
type HoldInput struct {
	Question string
}

// transitionCtx は transition ヘルパーが操作固有の書き込み（apply）へ渡す
// 可変の作業領域。apply は before・after へ操作固有の項目を足し、priority を
// 上書きできる（classify だけが使う）。
type transitionCtx struct {
	tx       *sql.Tx
	id       int64 // 課題の内部整数 ID
	current  *Challenge
	before   map[string]any
	after    map[string]any
	priority *Priority // 既定は current.Priority。classify だけが上書きする
	nowStr   string    // rec.at をミリ秒精度に丸めた文字列（task_plan.created_at・hold.raised_at に使う）
	now      time.Time // nowStr を parseTimestamp した値（返却する Plan.CreatedAt に使う）
	// rec は、この遷移が属する書き込みトランザクションの activityRecorder。
	// apply（操作固有の書き込み）は、当該遷移そのものの1件（entity="challenge"）
	// とは別に、他エンティティへのエントリを追加で積みたい場合に使う
	// （#13: D12 の完了の承認が、release ごとに entity="operation" のエントリを
	// 追加で積む）。
	rec *activityRecorder
}

// transitionPrecedingResolver は、遷移先の解決（tr.Target.Resolve）に渡す
// preceding をトランザクションの中で動的に決めるための差し込み口（T12:
// answer が使う。「保留に入る直前の状態」は静的な値ではなく、その時点で
// 未回答の保留行から読む必要があるため）。cid は課題の内部整数 ID、current は
// 直前に読んだ課題。
type transitionPrecedingResolver func(ctx context.Context, tx *sql.Tx, cid int64, current *Challenge) (Status, error)

// runTransitionParams は runTransition が使う操作固有のパラメータ。
// expectedVersion が非 nil の呼び出し（#12: 本人確認つきの操作）だけが
// バージョン不一致を ErrConflict として検出する（§クリティカル設計決定 1）。
// nil の呼び出し（#10: 本人確認の無い5操作）は PD7 のとおり、
// BEGIN IMMEDIATE による直列化と最新状態での再検査だけに頼り、conflict は
// 返さない。
type runTransitionParams struct {
	actor            string
	channel          Channel
	verification     Verification
	id               string
	expectedVersion  *int
	op               Operation
	preceding        Status
	resolvePreceding transitionPrecedingResolver // nil なら preceding をそのまま使う
}

// runTransition は transition（#10: 本人確認の無い5操作）と verifiedTransition
// （#12: 本人確認つきの approve・reject・answer）が共有する判定順序と書き込みを
// 持つ内部ヘルパー。
//
// 判定順: ② parseChallengeID 不正は ErrNotFound → ③ mutateAs 内
// （BEGIN IMMEDIATE の中）で loadChallenge（無ければ ErrNotFound）→
// ④ expectedVersion が非 nil かつ一致しなければ ErrConflict（状態の再判定より
// 先＝§クリティカル設計決定1）→ ⑤ IsTerminal なら ErrTerminalState →
// ⑥ Lookup(current.Status, op) が ok=false なら ErrInvalidTransition →
// ⑦ tr.Target.Resolve で遷移先を確定（resolvePreceding があれば先に
// preceding を解決する）→ ⑧ apply（操作固有の書き込み）→ ⑨ UPDATE challenge →
// ⑩ rec.record。
//
// 入力の検証（各公開メソッドの閉集合・必須値の検査）は呼び出し元がこの
// ヘルパーを呼ぶ前に行う。
func (s *Store) runTransition(ctx context.Context, p runTransitionParams, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	cid, ok := parseChallengeID(p.id)
	if !ok {
		return nil, ErrNotFound
	}

	var result *Challenge
	err := s.mutateAs(ctx, p.actor, p.channel, p.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if p.expectedVersion != nil && current.Version != *p.expectedVersion {
			return ErrConflict
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}
		tr, ok := Lookup(current.Status, p.op)
		if !ok {
			return ErrInvalidTransition
		}
		// 遷移表の RequiresVerification と、この呼び出しが本人確認つき
		// （expectedVersion 非 nil）かどうかは常に一致していなければならない
		// （self-review 指摘: これが無いと、verifiedTransition を経由せず
		// transition() から T5・T6・T12・T13・T15 を呼んでも遷移表の検査だけは
		// 通ってしまい、「承認・差し戻し・保留への回答は本人確認つきの操作
		// としてだけ成立する」という不変条件がコードで強制されていなかった）。
		// 到達すれば呼び出し側の実装誤りであり fail-closed に拒否する。
		//
		// expectedVersion の有無だけでなく verification（VerificationNone
		// かどうか）も独立に照合する（self-review 指摘・ラウンド2: どちらか
		// 一方だけを見る代理検査は、将来 core 内に「expectedVersion は
		// 渡すが verification は none のまま」のような呼び出し元が増えると
		// すり抜ける余地を残す）。
		hasExpectedVersion := p.expectedVersion != nil
		hasVerification := p.verification != VerificationNone
		if tr.RequiresVerification != hasExpectedVersion || tr.RequiresVerification != hasVerification {
			return fmt.Errorf("core: operation %q requires_verification=%v but was invoked with expectedVersion set=%v verification=%q (programming error: verified and unverified transition paths must not cross)", p.op, tr.RequiresVerification, hasExpectedVersion, p.verification)
		}

		preceding := p.preceding
		if p.resolvePreceding != nil {
			preceding, err = p.resolvePreceding(ctx, tx, cid, current)
			if err != nil {
				return err
			}
		}
		target, err := tr.Target.Resolve(Table, preceding)
		if err != nil {
			return err
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}

		tc := &transitionCtx{
			tx:       tx,
			id:       cid,
			current:  current,
			before:   map[string]any{},
			after:    map[string]any{},
			priority: current.Priority,
			nowStr:   nowStr,
			now:      now,
			rec:      rec,
		}
		// 状態が変わらない遷移（T4: plan の改訂）では before/after に status を
		// 含めない（変わっていないため。H7）。
		if current.Status != target {
			tc.before["status"] = string(current.Status)
			tc.after["status"] = string(target)
		}

		if err := apply(ctx, tc); err != nil {
			return err
		}

		newVersion := current.Version + 1
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET status = ?, priority = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			string(target), nullablePriority(tc.priority), newVersion, nowStr, cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: transition challenge %s: expected to update 1 row, updated %d", p.id, affected)
		}

		// 課題の版（challenge.version）は状態が変わらない遷移でも常に +1 する
		// （T4 も改訂として版を進める。edit と同じ規約）。
		tc.after["version"] = newVersion
		if err := rec.record("challenge", cid, string(p.op), tc.before, tc.after); err != nil {
			return err
		}

		result = &Challenge{
			ID:           formatChallengeID(cid),
			Title:        current.Title,
			Description:  current.Description,
			DoneCriteria: current.DoneCriteria,
			Urgency:      current.Urgency,
			Priority:     tc.priority,
			Status:       target,
			Version:      newVersion,
			Reporter:     current.Reporter,
			CreatedAt:    current.CreatedAt,
			UpdatedAt:    now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// transition は 5 操作（classify・plan・submit・verify・hold）が共有する
// 判定順序と書き込みを持つ内部ヘルパー（実体は runTransition。actor は
// resolveActor()・verification は VerificationNone に固定し、expectedVersion
// を持たない＝conflict を返さない。PD7）。
//
// 判定順は ID の形式（ErrNotFound）→ actor の解決（ErrActorUnavailable）の
// 順を保つ（#9・#10 と同じ。self-review 指摘: 以前の実装は resolveActor を
// 先に呼んでいたため、actor を解決できない環境で不正な ID を渡すと
// ErrNotFound ではなく ErrActorUnavailable になってしまい、既存5操作の
// エラー優先順位が変わっていた）。
func (s *Store) transition(ctx context.Context, ch Channel, id string, op Operation, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	if _, ok := parseChallengeID(id); !ok {
		return nil, ErrNotFound
	}
	actor, err := resolveActor()
	if err != nil {
		return nil, err
	}
	return s.runTransition(ctx, runTransitionParams{
		actor:        actor,
		channel:      ch,
		verification: VerificationNone,
		id:           id,
		op:           op,
		preceding:    NoStatus,
	}, apply)
}

// verifiedTransition は本人確認つきの操作（#12: approve・reject・answer）が
// 共有する入口。att を fail-closed で再検査してから runTransition を呼ぶ
// （§クリティカル設計決定1: 「ゼロ値・登録簿外の Attestation を
// ErrVerificationRejected で拒否する」。呼び出し側が core.Verify を経由した
// かどうかに関わらず、この関数自身がもう一度検査する）。expectedVersion を
// 必ず渡すため、対象の版が Prepare* の時点から変わっていれば ErrConflict に
// なる。
func (s *Store) verifiedTransition(ctx context.Context, att Attestation, id string, expectedVersion int, op Operation, preceding Status, resolvePreceding transitionPrecedingResolver, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	if !attestationValid(att, id) {
		return nil, ErrVerificationRejected
	}
	ev := expectedVersion
	return s.runTransition(ctx, runTransitionParams{
		actor:            att.actor,
		channel:          att.channel,
		verification:     att.verification,
		id:               id,
		expectedVersion:  &ev,
		op:               op,
		preceding:        preceding,
		resolvePreceding: resolvePreceding,
	}, apply)
}

// attestationValid は att が Verify を経由した登録簿内の (channel,
// verification) の組を持ち、actor が非空であること、かつ本人確認が id
// （呼び出し側がこれから書き込もうとしている対象）に対して成立したことを
// 再検査する。core.Attestation はゼロ値も他パッケージから構築できてしまう
// うえ（verification.go のコメント）、target を検査しないと「別の対象向けに
// 成立した Attestation」を Execute* がそのまま受理してしまう
// （self-review 指摘: 確認の入力は対象の ID の完全一致という設計決定〈H9〉が
// Verifier の中だけで閉じており、core の API 境界では担保されていなかった）。
func attestationValid(att Attestation, id string) bool {
	return att.actor != "" && att.target == id && registryAllows(att.channel, att.verification)
}

// insertHold は hold テーブルへ 1 行追加する（T10・T11 が共有する）。
// from_status は遷移前の状態（tc.current.Status）。answer 系は NULL のまま。
func insertHold(ctx context.Context, tc *transitionCtx, question string) error {
	if _, err := tc.tx.ExecContext(ctx,
		`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
		 VALUES (?, ?, ?, ?, NULL, NULL, NULL)`,
		tc.id, question, string(tc.current.Status), tc.nowStr,
	); err != nil {
		return err
	}
	tc.after["question"] = question
	return nil
}

// ClassifyChallenge は課題に優先度を設定する（T2）。Priority が閉集合外なら
// ErrValidation（トランザクションを開く前に判定）。
func (s *Store) ClassifyChallenge(ctx context.Context, ch Channel, id string, in ClassifyInput) (*Challenge, error) {
	p, ok := ParsePriority(in.Priority)
	if !ok {
		return nil, ErrValidation
	}
	return s.transition(ctx, ch, id, OpClassify, func(_ context.Context, tc *transitionCtx) error {
		tc.before["priority"] = nullablePriority(tc.current.Priority)
		tc.after["priority"] = nullablePriority(&p)
		tc.priority = &p
		return nil
	})
}

// PlanChallenge は計画を版つきで登録・改訂する（T3・T4）。Body が
// strings.TrimSpace で空なら ErrValidation。新しい版は既存の最大版+1
// （無ければ 1）。作業ログの after には plan_version だけを載せ、本文は
// 載せない。
func (s *Store) PlanChallenge(ctx context.Context, ch Channel, id string, in PlanInput) (*Challenge, *Plan, error) {
	if strings.TrimSpace(in.Body) == "" {
		return nil, nil, ErrValidation
	}

	var plan Plan
	c, err := s.transition(ctx, ch, id, OpPlan, func(ctx context.Context, tc *transitionCtx) error {
		var maxVersion sql.NullInt64
		if err := tc.tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_plan WHERE challenge_id = ?`, tc.id).Scan(&maxVersion); err != nil {
			return err
		}
		newPlanVersion := 1
		if maxVersion.Valid {
			// 改訂（T4）では、置き換えられた計画の版を before に残す（H7:
			// 変わった項目の変更前。code-reviewer 指摘: これが無いと T4 の
			// before が NULL になり、create 以外で before が null になる）。
			newPlanVersion = int(maxVersion.Int64) + 1
			tc.before["plan_version"] = int(maxVersion.Int64)
		} else {
			// S3（Issue #39）: 未設定（計画が無い）から値が入る初回（T3）は、
			// before にそのキーを null で載せる（classify の priority・edit の
			// urgency とそろえる。キー自体を省く旧挙動は仕様に定めが無く、
			// 未設定から値が入る項目の書き方が操作ごとに不揃いだった）。
			tc.before["plan_version"] = nil
		}
		if _, err := tc.tx.ExecContext(ctx,
			`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, ?, ?, ?)`,
			tc.id, newPlanVersion, in.Body, tc.nowStr,
		); err != nil {
			return err
		}
		tc.after["plan_version"] = newPlanVersion
		plan = Plan{Version: newPlanVersion, Body: in.Body, CreatedAt: tc.now}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, &plan, nil
}

// SubmitChallenge は着手中の課題を検証中へ進める（T7）。操作固有の書き込みは無い。
func (s *Store) SubmitChallenge(ctx context.Context, ch Channel, id string) (*Challenge, error) {
	return s.transition(ctx, ch, id, OpSubmit, func(_ context.Context, _ *transitionCtx) error {
		return nil
	})
}

// VerifyChallenge は検証結果を記録する（T8・T9・T10）。Result が閉集合外なら
// ErrValidation。uncertain で Question が nil または trim 空なら ErrValidation。
// met・not_met で Question が非 nil なら ErrValidation（CLI が usage_error で
// 先に弾くが、core も fail-closed で同じ入力を拒否する）。
func (s *Store) VerifyChallenge(ctx context.Context, ch Channel, id string, in VerifyInput) (*Challenge, error) {
	result, ok := ParseVerifyResult(in.Result)
	if !ok {
		return nil, ErrValidation
	}
	if result == VerifyResultUncertain {
		if in.Question == nil || strings.TrimSpace(*in.Question) == "" {
			return nil, ErrValidation
		}
	} else if in.Question != nil {
		return nil, ErrValidation
	}

	op, ok := OperationForVerify(result)
	if !ok {
		// ParseVerifyResult が成功した3値は OperationForVerify が必ず解決できる
		// （transition.go §OperationForVerify の写像と同じ閉集合）。到達しない
		// はずだが fail-closed で拒否する。
		return nil, ErrValidation
	}

	return s.transition(ctx, ch, id, op, func(ctx context.Context, tc *transitionCtx) error {
		if result == VerifyResultUncertain {
			return insertHold(ctx, tc, *in.Question)
		}
		return nil
	})
}

// HoldChallenge は課題を人間対応待ちへ進める（T11）。Question が
// strings.TrimSpace で空なら ErrValidation。
func (s *Store) HoldChallenge(ctx context.Context, ch Channel, id string, in HoldInput) (*Challenge, error) {
	if strings.TrimSpace(in.Question) == "" {
		return nil, ErrValidation
	}
	return s.transition(ctx, ch, id, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		return insertHold(ctx, tc, in.Question)
	})
}
