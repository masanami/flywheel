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
	// Kind は保留の種類（HoldKind の値）。空は人の対応を待つ保留（human）。閉集合外は ErrValidation。
	Kind string
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
	// stage は書き込む段階の欄（既定は current の値。patchStages が書き換える）。
	// runTransition が課題の UPDATE と同じトランザクションで書き、戻り値の Challenge に写す。
	stage StageFields
	// target は、この遷移で確定した遷移先の状態（tr.Target の解決の結果）。apply が戻り先で書き込みを分ける
	// （T15 が人間対応待ちへ戻るときだけ保留を立てる）のに使う。
	target Status
	nowStr string    // rec.at をミリ秒精度に丸めた文字列（task_plan.created_at・hold.raised_at に使う）
	now    time.Time // nowStr を parseTimestamp した値（返却する Plan.CreatedAt に使う）
	// runID は、この遷移の原因になった run の内部整数 ID（rec.runID と同じ値。
	// nil なら run に由来しない＝#9・#10・#12 の既存の呼び出し元）。
	// insertHold（#84 の J1 の uncertain）が hold.run_id へ書くために使う。
	runID *int64
	// rec は、この遷移が属する書き込みトランザクションの activityRecorder。
	// apply（操作固有の書き込み）は、当該遷移そのものの1件（entity="challenge"）
	// とは別に、他エンティティへのエントリを追加で積みたい場合に使う
	// （#13: D12 の完了の承認が、release ごとに entity="operation" のエントリを
	// 追加で積む）。
	rec *activityRecorder
	// taskID は、この遷移で作る保留（hold.task_id）が結び付くタスクの内部整数 ID。委譲の子の問い・停止で作る
	// 保留が apply で設定する（nil は NULL＝タスクに結び付かない保留）。
	taskID *int64
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
	// runID は、この遷移の原因になった run の内部整数 ID（#84。nil なら
	// run に由来しない＝#9・#10・#12 の既存の呼び出し元）。mutateAsRun・
	// activityRecorder.runID・transitionCtx.runID へそのまま伝わる。
	runID *int64
	// attested は verifiedTransition（Verify を通った Attestation を経由した
	// 呼び出し）だけが true にする。本人確認つきの遷移かどうかの不変条件は
	// verification の値（方式 none の (ui, none) も本人確認つき）ではなくこれで判定する。
	attested bool
	// replan は回答に「範囲が変わる」印が付いたこと（T12 だけ）。真のとき、遷移先は IfReplan の分類済になる。
	replan bool
	// replanResolved は、preceding の解決の後に読む replan（選んだ選択肢が計画の作り直しを表すとき、解決の中で決まる）。nil か偽なら replan だけで決まる。
	replanResolved func() bool
	// att は本人確認つきの遷移（attested）の Attestation。経路 chat の印の検査と種類の絞りに使う。
	att Attestation
	// restricted は、遷移表の制限つきの行を LookupRestricted で引くこと。立てるのは、その行の専用の書き込みだけ
	// （スーパーバイザーの手の supervisor_resolve〔T20〕と、仕様の文書の場所を付けた編集の spec_draft_unhold〔T21〕だけ。終端から出る T18 は、終端の検査が先に断るのでこの道を通らない）。本人確認の要る行は、これを立てても本人確認つきの呼び出しだけが通る。
	restricted bool
	// holdCheck が非 nil のとき、版の照合の代わりに呼ぶ（回答だけ。確かめた保留が開いたままで問いと選択肢が同じかを、書き込みの中で確かめる）。
	// 遷移表の検査より前に呼ぶ（保留が閉じていれば、状態の食い違いではなくコンフリクトで断る）。
	holdCheck func(ctx context.Context, tx *sql.Tx, cid int64) error
}

// runTransition は transition（#10: 本人確認の無い5操作）・verifiedTransition
// （#12: 本人確認つきの approve・reject・answer）・transitionAsInvoker
// （#84: 判断点の出力による mine・uncertain の写像）が共有する判定順序と
// 書き込みを持つ内部ヘルパー。
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
	err := s.mutateAsRun(ctx, p.actor, p.channel, p.verification, p.runID, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		result, err = s.transitionInTx(ctx, tx, rec, p, cid, apply)
		return err
	})
	if err != nil {
		return nil, err
	}
	// 完了・取り下げへ進む遷移は、すべてここを通る（transitionInTx を直接呼ぶ経路は、終端の状態へ進まない）。
	if result.Status == StatusDone || result.Status == StatusWithdrawn {
		s.cleanupSpecDraft(ctx, cid, p.actor)
	}
	if result.Status == StatusWithdrawn {
		s.discardWithdrawnSpecDraft(ctx, cid, p.actor)
	}
	return result, nil
}

// transitionGateInTx は遷移の入口の検査（版の照合→終端→遷移表→本人確認つきかの不変条件→登録簿→経路 chat の絞り）。何も書かない。
// 遷移（transitionInTx）と、遷移を成立させずに成功の書き込みで終わる回答の出し直し（reissueOverLimitQuestion）が共有する。
func (s *Store) transitionGateInTx(ctx context.Context, tx *sql.Tx, p runTransitionParams, cid int64) (*Challenge, Transition, error) {
	current, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, Transition{}, err
	}
	if p.holdCheck != nil {
		if err := p.holdCheck(ctx, tx, cid); err != nil {
			return nil, Transition{}, err
		}
	} else if p.expectedVersion != nil && current.Version != *p.expectedVersion {
		return nil, Transition{}, ErrConflict
	}
	if IsTerminal(Table, StatusVocabulary, current.Status) {
		return nil, Transition{}, ErrTerminalState
	}
	lookup := Lookup
	if p.restricted {
		lookup = LookupRestricted
	}
	tr, ok := lookup(current.Status, p.op)
	if !ok {
		return nil, Transition{}, ErrInvalidTransition
	}
	// 遷移表の RequiresVerification と、この呼び出しが本人確認つき
	// （expectedVersion 非 nil）かどうかは常に一致していなければならない
	// （self-review 指摘: これが無いと、verifiedTransition を経由せず
	// transition() から T5・T6・T12・T13・T15 を呼んでも遷移表の検査だけは
	// 通ってしまい、「承認・差し戻し・保留への回答は本人確認つきの操作
	// としてだけ成立する」という不変条件がコードで強制されていなかった）。
	// 到達すれば呼び出し側の実装誤りであり fail-closed に拒否する。
	//
	// expectedVersion の有無だけでなく、Verify を通った Attestation を
	// 経由したか（p.attested。方式 none の (ui, none) も本人確認つき）も
	// 独立に照合する（self-review 指摘・ラウンド2: どちらか
	// 一方だけを見る代理検査は、将来 core 内に「expectedVersion は
	// 渡すが verification は none のまま」のような呼び出し元が増えると
	// すり抜ける余地を残す）。
	hasExpectedVersion := p.expectedVersion != nil
	hasVerification := p.attested
	if tr.RequiresVerification != hasExpectedVersion || tr.RequiresVerification != hasVerification {
		return nil, Transition{}, fmt.Errorf("core: operation %q requires_verification=%v but was invoked with expectedVersion set=%v attested=%v (programming error: verified and unverified transition paths must not cross)", p.op, tr.RequiresVerification, hasExpectedVersion, p.attested)
	}
	if p.attested {
		// 登録簿の組が受けられる操作かを、遷移表の行が確定したこの時点で照らす
		// （何も書く前。拒否は ErrVerificationRejected）。
		act, ok := actionForTransition(tr)
		if !ok || !registryPermits(p.channel, p.verification, act) {
			return nil, Transition{}, ErrVerificationRejected
		}
		// 経路 chat は、印の検査と手がかりの解決、反映の承認を含む完了の承認の絞りをここで行う。
		if err := s.chatGate(ctx, tx, p.att, act, ""); err != nil {
			return nil, Transition{}, err
		}
	}

	return current, tr, nil
}

// transitionInTx は runTransition の書き込み本体（呼び出し側の書き込みトランザクションの中で使う）。同じ書き込みの中で、検査・足す処理と
// 遷移（保留など）を続けて行う呼び出し元が使う。判定順は runTransition と同じ。
func (s *Store) transitionInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, p runTransitionParams, cid int64, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	current, tr, err := s.transitionGateInTx(ctx, tx, p, cid)
	if err != nil {
		return nil, err
	}

	preceding := p.preceding
	if p.resolvePreceding != nil {
		preceding, err = p.resolvePreceding(ctx, tx, cid, current)
		if err != nil {
			return nil, err
		}
	}
	// 遷移先が課題の completion_direct で分かれる行（T15）だけ、値をこのトランザクションの中で読む。
	completionDirect := false
	if tr.Target.IfCompletionDirect != NoStatus {
		completionDirect, err = loadCompletionDirect(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
	}
	var target Status
	if p.replan || (p.replanResolved != nil && p.replanResolved()) {
		target, err = tr.Target.ResolveForReplan(Table, preceding)
		if err != nil {
			if tr.Target.IfReplan != NoStatus && ReplanAllowedFrom(preceding) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", ErrValidation, err)
		}
	} else {
		target, err = tr.Target.ResolveForCompletionDirect(Table, preceding, completionDirect)
		if err != nil {
			return nil, err
		}
	}

	// 統合先を使う課題の完了の差し戻し（T15）は、着手中へ戻さず、計画を作り直す道（分類済）へ送る（済みのタスクは残る）。
	if tr.Target.IfUsesIntegration != NoStatus && target == tr.Target.Fixed {
		integ, err := challengeUsesIntegration(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if integ != nil && *integ {
			target = tr.Target.IfUsesIntegration
		}
	}

	nowStr := formatTimestamp(rec.at)
	now, err := parseTimestamp(nowStr)
	if err != nil {
		return nil, err
	}

	tc := &transitionCtx{
		tx:       tx,
		id:       cid,
		current:  current,
		before:   map[string]any{},
		after:    map[string]any{},
		priority: current.Priority,
		stage:    current.StageFields,
		target:   target,
		nowStr:   nowStr,
		now:      now,
		runID:    rec.runID,
		rec:      rec,
	}
	// 状態が変わらない遷移（T4: plan の改訂）では before/after に status を
	// 含めない（変わっていないため。H7）。
	if current.Status != target {
		tc.before["status"] = string(current.Status)
		tc.after["status"] = string(target)
	}

	if err := apply(ctx, tc); err != nil {
		return nil, err
	}
	// 保留を閉じる遷移（T12・T20）は、ほかの開いている保留が残っていれば、保留の戻り先ではなく人間対応待ちのまま（版は進める）。
	// 残りは、閉じる書き込み（apply）の後に、同じ書き込みの中で数える。
	if tr.Target.ReturnToPrecedingHoldStatus && target != StatusAwaitingHuman {
		open, err := countOpenHolds(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if open > 0 && target == StatusClassified {
			// 計画を作り直す回答は、ほかの開いている保留を同じ書き込みで閉じて、分類済へ戻る。
			if _, err := closeOtherOpenHolds(ctx, tc, 0, "計画を作り直す回答が成立したため"); err != nil {
				return nil, err
			}
		} else if open > 0 {
			target = StatusAwaitingHuman
			tc.target = target
			delete(tc.before, "status")
			delete(tc.after, "status")
		}
	}

	newVersion := current.Version + 1
	stageArgs, err := tc.stage.args()
	if err != nil {
		return nil, err
	}
	// completion_direct は遷移のたびにここで一元的に書く（T17 で真、ほかのすべての遷移で偽。
	// 完了確認待ちを出る遷移で消え、T8 で入るときも偽になる）。
	args := append([]any{string(target), nullablePriority(tc.priority), newVersion, nowStr, completionDirectColumn(p.op)}, stageArgs...)
	args = append(args, cid, current.Version)
	res, err := tx.ExecContext(ctx,
		`UPDATE challenge SET status = ?, priority = ?, version = ?, updated_at = ?, completion_direct = ?, `+stageUpdateSQL+` WHERE id = ? AND version = ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, fmt.Errorf("core: transition challenge %s: expected to update 1 row, updated %d", p.id, affected)
	}

	// 課題の版（challenge.version）は状態が変わらない遷移でも常に +1 する
	// （T4 も改訂として版を進める。edit と同じ規約）。
	tc.after["version"] = newVersion
	if err := rec.record("challenge", cid, string(p.op), tc.before, tc.after); err != nil {
		return nil, err
	}
	// 経路 chat の本人確認つきの操作は、成立した同じ書き込みの中で会話と課題を紐づける。
	if p.attested && p.att.chat != nil {
		act, ok := actionForTransition(tr)
		if !ok {
			return nil, fmt.Errorf("core: transition %s: no attested action for the chat link", tr.ID)
		}
		if action, ok := chatLinkActionOfTransition(act); ok {
			if err := linkChatOperation(ctx, tx, p.att, cid, action, nowStr); err != nil {
				return nil, err
			}
		}
	}
	// 完了（T13・T14 の両方）になる課題へ上げてある未のフォローアップを、同じトランザクションで済みにする。
	if target == StatusDone {
		if err := completeFollowupsPromotedTo(ctx, tx, rec, cid); err != nil {
			return nil, err
		}
	}

	return &Challenge{
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
		StageFields:  tc.stage,
		SpecPath:     current.SpecPath,
		SpecBasePath: current.SpecBasePath,
	}, nil
}

// completionDirectColumn は、操作 op の遷移の後の challenge.completion_direct の値。
// 親が直接行った完了の記録（T17）だけが 1、ほかの操作はすべて 0。
func completionDirectColumn(op Operation) int {
	if op == OpCompleteDirect {
		return 1
	}
	return 0
}

// loadCompletionDirect は challenge.completion_direct（いまの完了確認待ちが T17 で入ったものか）を読む。
func loadCompletionDirect(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var v int
	if err := tx.QueryRowContext(ctx, `SELECT completion_direct FROM challenge WHERE id = ?`, cid).Scan(&v); err != nil {
		return false, err
	}
	return v == 1, nil
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
	actor, err := s.resolveActor()
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

// transitionAsInvoker は判断点の出力による写像（#84: J1 の mine・uncertain。
// 将来の J2〈#85〉の plan・hold も同じ形になる想定）が共有する入口。
// transition() と同じ判定順序・書き込みを、経路 ChannelInvoker・本人確認
// VerificationNone・原因の run の ID（runID）つきで行う（§判断点の共通の規則
// 「判断点の出力による変更の作業ログは、経路を invoker・本人確認の方式を
// none…とし、原因の run の ID を持つ」・§機能全体の設計「判断点の出力を
// core の遷移へ写すときは、M1 の core の公開 API…と同じ遷移を呼ぶ」）。
//
// runIDDisplay（"R-<n>"）の形式が不正なら ErrValidation。id（"C-<n>"）の
// 形式が不正なら ErrNotFound（parseChallengeID を使う他の公開 API と同じ
// fail-closed の規則。§課題の起票・参照・編集「ID の形式が閉集合に一致
// しない場合も、存在しない ID と同じく ErrNotFound を返す」。code-reviewer
// 指摘・round2 CONFIRMED: 以前はここも ErrValidation と書いていたが、実装は
// ErrNotFound を返していた）。
// apply が ErrInvalidTransition・ErrTerminalState を返す（遷移表に現在の
// 状態からの行が無い・完了している）ことは、写す直前に読み直した課題が
// 判断点の遷移元でなくなっていたことを表す（§アーキテクチャ決定
// 「読み直した状態が遷移元でなくなっていれば写さず、run の出力だけを
// 残す」）。呼び出し元（judgment_j1.go の applyJ1Mine・applyJ1Uncertain）は
// この2つを errors.Is で判別し、mapped=false（エラーではない）として扱う。
func (s *Store) transitionAsInvoker(ctx context.Context, runIDDisplay, id string, op Operation, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	runIDInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return nil, ErrValidation
	}
	if _, ok := parseChallengeID(id); !ok {
		return nil, ErrNotFound
	}
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	return s.runTransition(ctx, runTransitionParams{
		actor:        actor,
		channel:      ChannelInvoker,
		verification: VerificationNone,
		id:           id,
		op:           op,
		preceding:    NoStatus,
		runID:        &runIDInt,
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
	return s.verifiedTransitionReplan(ctx, att, id, expectedVersion, op, preceding, resolvePreceding, false, nil, apply)
}

// verifiedTransitionReplan は verifiedTransition に、回答の「範囲が変わる」印（replan）を足した版。
func (s *Store) verifiedTransitionReplan(ctx context.Context, att Attestation, id string, expectedVersion int, op Operation, preceding Status, resolvePreceding transitionPrecedingResolver, replan bool, replanResolved func() bool, apply func(ctx context.Context, tc *transitionCtx) error) (*Challenge, error) {
	p, err := verifiedTransitionParams(att, id, expectedVersion, op, preceding, resolvePreceding, replan, replanResolved)
	if err != nil {
		return nil, err
	}
	return s.runTransition(ctx, p, apply)
}

// verifiedTransitionParams は本人確認つきの遷移（verifiedTransitionReplan）の runTransitionParams を作る。本人確認が id に対して成立していなければ ErrVerificationRejected。
func verifiedTransitionParams(att Attestation, id string, expectedVersion int, op Operation, preceding Status, resolvePreceding transitionPrecedingResolver, replan bool, replanResolved func() bool) (runTransitionParams, error) {
	if !attestationValid(att, id) {
		return runTransitionParams{}, ErrVerificationRejected
	}
	ev := expectedVersion
	return runTransitionParams{
		actor:            att.actor,
		channel:          att.channel,
		verification:     att.verification,
		id:               id,
		expectedVersion:  &ev,
		op:               op,
		preceding:        preceding,
		resolvePreceding: resolvePreceding,
		attested:         true,
		replan:           replan,
		replanResolved:   replanResolved,
		att:              att,
	}, nil
}

// attestationValid は att が Verify を経由した登録簿内の (channel,
// verification) の組を持ち、actor が非空であること、かつ本人確認が id
// （呼び出し側がこれから書き込もうとしている対象）に対して成立したことを
// 再検査する。操作の範囲は attestationPermits が照らす。core.Attestation はゼロ値も他パッケージから構築できてしまう
// うえ（verification.go のコメント）、target を検査しないと「別の対象向けに
// 成立した Attestation」を Execute* がそのまま受理してしまう
// （self-review 指摘: 確認の入力は対象の ID の完全一致という設計決定〈H9〉が
// Verifier の中だけで閉じており、core の API 境界では担保されていなかった）。
func attestationValid(att Attestation, id string) bool {
	if att.channel == ChannelChat {
		// 経路 chat の手がかりは、トランザクションの中で印から解決する。ここでは印の 2 値が空でないことだけを見る。
		if att.chat == nil || !att.chat.complete() {
			return false
		}
	} else if att.verification == VerificationRelayed && !att.hasEvidence() {
		return false
	}
	return att.actor != "" && att.target == id && registryAllows(att.channel, att.verification)
}

// insertHold は hold テーブルへ 1 行追加する（T10・T11 が共有する）。
// from_status は遷移前の状態（tc.current.Status）。answer 系は NULL のまま。
func insertHold(ctx context.Context, tc *transitionCtx, question string) error {
	_, err := insertHoldID(ctx, tc, question)
	return err
}

// insertHoldID は insertHold と同じ行を追加し、その hold.id を返す。
func insertHoldID(ctx context.Context, tc *transitionCtx, question string) (int64, error) {
	return insertHoldKind(ctx, tc, question, HoldKindHuman)
}

// insertHoldKind は insertHoldID の、保留の種類を指定する版。human 以外のときだけ作業ログの after に種類を載せる
// （既存の保留の作業ログの形は変えない）。計画承認待ちから入れる保留の検査は、保留を作る入口（insertHoldWithOptions）にある。
func insertHoldKind(ctx context.Context, tc *transitionCtx, question string, kind HoldKind) (int64, error) {
	return insertHoldKindFrom(ctx, tc, question, kind, tc.current.Status)
}

// insertHoldKindFrom は insertHoldKind の、保留に入る直前の状態（hold.from_status。回答で戻る先）を指定する版。
// 遷移前の状態と戻り先が違う T15（親が直接した完了の差し戻し）が使う。
func insertHoldKindFrom(ctx context.Context, tc *transitionCtx, question string, kind HoldKind, from Status) (int64, error) {
	return insertHoldKindFromTask(ctx, tc, question, kind, from, tc.taskID)
}

// insertHoldKindFromTask は insertHoldKindFrom の、保留が結び付くタスク（hold.task_id。nil は NULL）を指定する版。
func insertHoldKindFromTask(ctx context.Context, tc *transitionCtx, question string, kind HoldKind, from Status, taskID *int64) (int64, error) {
	return insertHoldWithOptions(ctx, tc, question, kind, from, taskID, HoldOptionsInput{})
}

// insertHoldWithOptions は保留を作る入口（選択肢の並び・「識別の無い回答を断る」印・止まり方を受ける）。いまある保留を作る関数は
// 追加の指定なしでここへ委ねる。選択肢の誤りは ErrHoldOptionInvalid、止まり方が閉集合の外か、保留に渡さない下書きの問い
// （spec_question）なら ErrValidation で断り、何も書かない。
//
// 計画承認待ちの課題へ作れる保留は、対話のセッションが引き取る保留（parent_direct）だけで、それ以外は ErrInvalidTransition で断る
// （承認の待ち先が人のままで、人の対応を待つ保留は意味を持たない。保留を書く入口すべてが通るここで検査する）。
//
// 止まり方（opts.StopKind）が渡され、opts.SupervisorEnabled（宣言の supervisor。呼び出し元が渡す）が真のとき、行を作るのと同じ書き込みの中で
// supervisor_state を pending にする。止まり方を渡さない呼び出しは stop_kind も supervisor_state も NULL のまま。
//
// 止まり方が渡されたときは、行を作る前に、同じ書き込みの中で回数の上限（supervisor_limit.go）を判定する。当たったときは
// supervisor_state を NULL のまま（stop_kind は残す）にし、問いの先頭に固定の 1 行を足してオーナーへ上げる。
func insertHoldWithOptions(ctx context.Context, tc *transitionCtx, question string, kind HoldKind, from Status, taskID *int64, opts HoldOptionsInput) (int64, error) {
	if tc.current.Status == StatusAwaitingPlanApproval && kind != HoldKindParentDirect {
		return 0, ErrInvalidTransition
	}
	if opts.Scope != "" && (opts.Scope != HoldScopeTask || taskID == nil) {
		return 0, fmt.Errorf("%w: hold scope %q needs a task-bound hold", ErrValidation, opts.Scope)
	}
	if opts.Scope == HoldScopeTask && tc.current.Status != StatusInProgress && tc.current.Status != StatusAwaitingHuman {
		// タスクを止める保留は、課題が着手中のときか、重ねる条件を満たす人間対応待ちのときだけ立てられる。
		return 0, ErrInvalidTransition
	}
	if tc.current.Status == StatusAwaitingHuman {
		// 保留を課題に重ねる（T11 の人間対応待ちからの行）。重ねられるのは、開いている保留がどれもタスクを止める保留で、
		// 新しい保留が、開いている保留の無い別のタスクを止める保留のとき。満たさなければ、いまの「保留にできない」と同じ遷移の誤り。
		// 課題を止める保留とタスクを止める保留は同時に開かない（索引では防げないので、ここで確かめる）。
		if opts.Scope != HoldScopeTask {
			return 0, ErrInvalidTransition
		}
		may, err := taskMayProceed(ctx, tc.tx, tc.id, *taskID)
		if err != nil {
			return 0, err
		}
		if !may {
			return 0, ErrInvalidTransition
		}
		from = StatusInProgress
	}
	if opts.StopKind != "" {
		if _, ok := ParseStopKind(string(opts.StopKind)); !ok {
			return 0, fmt.Errorf("%w: unknown stop kind %q", ErrValidation, opts.StopKind)
		}
		if opts.StopKind == StopKindSpecQuestion {
			// 下書きの問いは保留の前に扱う。保留に渡すと、答える手を実行できないスーパーバイザー待ちの保留ができる。
			return 0, fmt.Errorf("%w: stop kind %q is not for a hold", ErrValidation, opts.StopKind)
		}
	}
	_, optionsJSON, err := resolveHoldOptions(opts.Options)
	if err != nil {
		return 0, err
	}
	var optionsColumn any
	if optionsJSON != nil {
		optionsColumn = *optionsJSON
	}
	required := 0
	if opts.Required {
		required = 1
	}
	if opts.Escalation != nil && opts.StopKind != StopKindChildQuestion {
		return 0, fmt.Errorf("%w: an escalation is only for child_question", ErrValidation)
	}
	var stopKindColumn, supervisorStateColumn, findingsColumn, recommendationColumn, actionColumn any
	if opts.StopKind != "" {
		stopKindColumn = string(opts.StopKind)
		if opts.StopKind == StopKindChildQuestion {
			// 子の問い: 答えによる再開の上限に達していれば、判断点を呼ばずに上げる（印は NULL）。達していなければ、
			// スーパーバイザーが上げた保留（escalated）か、判断点を呼ばずに上げた保留（NULL）。スーパーバイザー待ちにはしない。
			reached, err := supervisorAnswerLimitReached(ctx, tc.tx, tc.id)
			if err != nil {
				return 0, err
			}
			if reached {
				question = prefixSupervisorAnswerLimitQuestion(question)
			} else if e := opts.Escalation; e != nil {
				supervisorStateColumn = string(SupervisorStateEscalated)
				findingsColumn, recommendationColumn, actionColumn = e.Findings, e.Recommendation, e.ActionID
				if e.FailedQuestion {
					question = prefixSupervisorFailedQuestion(question)
				}
			}
		} else {
			limitHands, err := supervisorLimitHit(ctx, tc.tx, tc.id, taskID, opts.StopKind)
			if err != nil {
				return 0, err
			}
			if limitHands != nil {
				question = supervisorLimitNotice(limitHands) + "\n" + question
			} else if opts.SupervisorEnabled {
				supervisorStateColumn = string(SupervisorStatePending)
			}
		}
	}
	var scopeColumn any
	if opts.Scope != "" {
		scopeColumn = string(opts.Scope)
	}
	res, err := tc.tx.ExecContext(ctx,
		`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, run_id, kind, task_id, options, option_required, stop_kind, supervisor_state,
		   supervisor_findings, supervisor_recommendation, supervisor_action_id, scope)
		 VALUES (?, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.id, question, string(from), tc.nowStr, int64PtrColumn(tc.runID), string(kind), int64PtrColumn(taskID), optionsColumn, required, stopKindColumn, supervisorStateColumn,
		findingsColumn, recommendationColumn, actionColumn, scopeColumn,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if actionColumn != nil {
		// 保留を作る前に書いた対処の記録（子の問いを上げた・判断できなかった）に、できた保留を結び付ける。
		if _, err := tc.tx.ExecContext(ctx, `UPDATE supervisor_action SET hold_id = ? WHERE id = ? AND hold_id IS NULL`, id, actionColumn); err != nil {
			return 0, err
		}
	}
	tc.after["question"] = question
	tc.after["hold_id"] = formatHoldID(id)
	tc.after["hold_scope"] = holdScopeLabel(opts.Scope)
	if kind != HoldKindHuman {
		tc.after["hold_kind"] = string(kind)
	}
	return id, nil
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
		p, err := insertTaskPlanVersion(ctx, tc, in.Body, nil)
		if err != nil {
			return err
		}
		plan = p
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, &plan, nil
}

// insertTaskPlanVersion は計画の新しい版（既存の最大版+1。無ければ 1）を task_plan へ
// 追加し、作業ログの before・after へ plan_version を載せる（T3・T4 が共有する。
// PlanChallenge〈人が登録する計画。spec は NULL〉と、J2 の写像〈#85。spec に
// 構造化した出力の JSON を持つ〉が使う）。spec が nil なら spec 列は NULL。
func insertTaskPlanVersion(ctx context.Context, tc *transitionCtx, body string, spec *string) (Plan, error) {
	var maxVersion sql.NullInt64
	if err := tc.tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_plan WHERE challenge_id = ?`, tc.id).Scan(&maxVersion); err != nil {
		return Plan{}, err
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
	var specArg any
	if spec != nil {
		specArg = *spec
	}
	if _, err := tc.tx.ExecContext(ctx,
		`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, ?, ?, ?, ?)`,
		tc.id, newPlanVersion, body, tc.nowStr, specArg,
	); err != nil {
		return Plan{}, err
	}
	tc.after["plan_version"] = newPlanVersion
	plan := Plan{Version: newPlanVersion, Body: body, CreatedAt: tc.now}
	if spec != nil {
		v := *spec
		plan.Spec = &v
	}
	return plan, nil
}

// SubmitChallenge は着手中の課題を検証中へ進める（T7）。起動できるタスクがちょうど 1 個なら、そのタスクも検証中にする。
func (s *Store) SubmitChallenge(ctx context.Context, ch Channel, id string) (*Challenge, error) {
	return s.transition(ctx, ch, id, OpSubmit, submitTaskInTransition)
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
		if err := rejectHandVerificationForIntegration(ctx, tc); err != nil {
			return err
		}
		// 検証中のタスクがちょうど 1 個なら、完了の検証の判断点（J5）と同じ結果にする（保留の結び付けは、保留を作る前に決める）。
		if err := verifyTaskByHandInTransition(ctx, tc, result); err != nil {
			return err
		}
		if result == VerifyResultUncertain {
			return insertHold(ctx, tc, *in.Question)
		}
		return nil
	})
}

// HoldChallenge は課題を人間対応待ちへ進める（T11）。Question が
// strings.TrimSpace で空なら ErrValidation。Kind が空でも HoldKind の値でもなければ ErrValidation
// （空は人の対応を待つ保留）。
func (s *Store) HoldChallenge(ctx context.Context, ch Channel, id string, in HoldInput) (*Challenge, error) {
	if strings.TrimSpace(in.Question) == "" {
		return nil, ErrValidation
	}
	kind := HoldKindHuman
	if in.Kind != "" {
		k, ok := ParseHoldKind(in.Kind)
		if !ok {
			return nil, ErrValidation
		}
		kind = k
	}
	return s.transition(ctx, ch, id, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldKind(ctx, tc, in.Question, kind)
		return err
	})
}

// attestationPermits は attestationValid に加えて、att の (channel, verification)
// の組が操作 act を受けられることを照らす述語。ExecuteBudget・
// ExecuteOperationApproval の入口と、遷移（ExecuteApproval・ExecuteAnswer）の
// 判定（runTransition）が同じ registryPermits で照らす。
func attestationPermits(att Attestation, id string, act attestedAction) bool {
	return attestationValid(att, id) && registryPermits(att.channel, att.verification, act)
}
