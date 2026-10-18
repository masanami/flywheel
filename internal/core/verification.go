package core

import "strings"

// Verifier は本人確認の方式の実装（docs/features/m1-core.md
// §クリティカル設計決定 1「本人確認の方式は Verifier として実装し、core は
// Verifier が返した結果（本人確認済みの actor・方式）だけを信用する」）。
// M1 は tty_confirm の 1 実装だけを持つ（internal/cli.ttyConfirmVerifier）。
type Verifier interface {
	// Method はこの Verifier が実装する本人確認の方式を返す。
	Method() Verification

	// Confirm は summary を利用者へ示し、対象の ID（expectedID）の完全一致
	// 入力を求める。成立すれば本人確認済みの actor を返す。
	//
	// 契約:
	//   - 成立条件を満たさない環境（例: 端末が無い）なら ErrTTYRequired を返す
	//   - 入力が expectedID と一致しない、または入力の終端（EOF）なら
	//     ErrConfirmationMismatch を返す
	Confirm(summary string, expectedID string) (actor string, err error)

	// Evidence はこの Verifier が持つ会話の手がかりを返す。方式 relayed
	// （(cli, relayed)）では空でない Session・Quote が必須で、Verify が
	// 空の手がかりを verification_rejected で拒否する。他の方式の手がかりは
	// Verify が捨てる（relayed でない記録の relay_* は NULL）。
	Evidence() RelayEvidence
}

// RelayEvidence は対話で受けた操作の会話の手がかり（M4P27）。Session は
// 会話の識別、Quote は発言の要旨。
type RelayEvidence struct {
	Session string
	Quote   string
}

// complete は Session・Quote の両方が空白だけでないかを返す。
func (e RelayEvidence) complete() bool {
	return strings.TrimSpace(e.Session) != "" && strings.TrimSpace(e.Quote) != ""
}

// Attestation は本人確認が成立した結果。core が信用する唯一の情報源であり、
// 呼び出し側が「確認済み」と申告する経路は持たない（承認・作業ログの記録は
// #12 がこれを使う）。フィールドは非公開にしている: 公開フィールドの
// struct だと呼び出し側（internal/cli 等）が Verify を経由せずに
// core.Attestation{...} を直接組み立てられてしまい、「呼び出し側が
// 『確認済み』と申告する経路は持たない」という設計決定を型で強制できない
// （self-review 指摘: 代替案 C の経路を API の形として開いたままにしてしまう）。
// Attestation は core パッケージ内（＝Verify）でしか構築できず、他パッケージは
// Actor()・Channel()・Verification() の読み取り専用アクセサでしか値を
// 取り出せない。
type Attestation struct {
	actor        string
	channel      Channel
	verification Verification
	// target は Confirm に渡された expectedID（本人確認が成立した対象の
	// 表示形 ID）。Execute*（#12）が、要約を表示した対象と書き込み先の対象が
	// 一致することを再検査するために使う（self-review 指摘: actor と
	// 登録簿だけを見る fail-closed 検査は、別の対象向けに成立した
	// Attestation の使い回しを拒否できていなかった）。
	target string
	// evidence は方式 relayed の会話の手がかり。relayed 以外では常にゼロ値
	// （Verify が載せるのは relayed のときだけ）。Execute* は空でないときだけ
	// approval・hold の relay_* と作業ログの after に書く。
	evidence RelayEvidence
}

// hasEvidence は att が手がかりを持つかを返す。
func (a Attestation) hasEvidence() bool { return a.evidence.complete() }

// relayColumns は approval・hold の relay_session・relay_quote 列へ渡す値を返す。
// 手がかりが無ければ両方 NULL（nil）。
func (a Attestation) relayColumns() (session any, quote any) {
	if !a.hasEvidence() {
		return nil, nil
	}
	return a.evidence.Session, a.evidence.Quote
}

// addRelayTo は手がかりがあるときだけ、作業ログの after の最上位に
// relay_session・relay_quote を足す。
func (a Attestation) addRelayTo(after map[string]any) {
	if a.hasEvidence() {
		after["relay_session"] = a.evidence.Session
		after["relay_quote"] = a.evidence.Quote
	}
}

// Actor は本人確認が成立した actor を返す。
func (a Attestation) Actor() string { return a.actor }

// Channel は本人確認が成立した経路を返す。
func (a Attestation) Channel() Channel { return a.channel }

// Verification は本人確認が成立した方式を返す。
func (a Attestation) Verification() Verification { return a.verification }

// attestedAction は Attestation で成立させる操作の種類（登録簿の行が受けられる
// 操作の集合の要素。M4P23）。遷移表の Operation とは別に、完了の承認を保留の
// 有無で、承認と差し戻しを対象で分けて持つ。
type attestedAction string

const (
	actionApprovePlan           attestedAction = "approve_plan"            // T5
	actionApproveCompletion     attestedAction = "approve_completion"      // T13（本番反映の保留を外す）
	actionApproveCompletionHold attestedAction = "approve_completion_hold" // T14（HoldRelease=true）
	actionReject                attestedAction = "reject"                  // T6・T15
	actionAnswer                attestedAction = "answer"                  // T12
	actionBudget                attestedAction = "budget"
	actionOperationApprove      attestedAction = "operation_approve"
	actionOperationReject       attestedAction = "operation_reject"
)

// actionForTransition は本人確認つきの遷移（遷移表の RequiresVerification）の
// 行から操作の種類を引く。本人確認つきの行以外は ok=false。
func actionForTransition(tr Transition) (attestedAction, bool) {
	switch tr.ID {
	case "T5":
		return actionApprovePlan, true
	case "T13":
		return actionApproveCompletion, true
	case "T14":
		return actionApproveCompletionHold, true
	case "T6", "T15":
		return actionReject, true
	case "T12":
		return actionAnswer, true
	}
	return "", false
}

// verificationRegistryEntry は許可した (Channel, Verification) の組と、その組が
// 受けられる操作の集合。
type verificationRegistryEntry struct {
	Channel      Channel
	Verification Verification
	// anyAction が true なら全操作を受ける。false なら actions だけを受ける。
	anyAction bool
	actions   []attestedAction
}

// verificationRegistry は許可した (経路, 本人確認の方式) の組の登録簿
// （§クリティカル設計決定 1）。データ（テーブル）として持ち、組ごとに受けられる
// 操作の集合を持つ（M4P23。検査は core に置き、server を通らない経路でも範囲が
// 崩れないようにする）。(cli, tty_confirm)・(cli, relayed) は全操作、(ui, none) は計画の承認と
// 本番反映の保留つきの完了の承認だけ（差し戻し・回答・予算の増額・不可逆操作・
// 保留を外した完了の承認は受けない）。
var verificationRegistry = []verificationRegistryEntry{
	{Channel: ChannelCLI, Verification: VerificationTTYConfirm, anyAction: true},
	{Channel: ChannelCLI, Verification: VerificationRelayed, anyAction: true},
	{Channel: ChannelUI, Verification: VerificationNone, actions: []attestedAction{actionApprovePlan, actionApproveCompletionHold}},
}

// registryAllows は (ch, v) の組が登録簿にあるかを判定する。
func registryAllows(ch Channel, v Verification) bool {
	for _, e := range verificationRegistry {
		if e.Channel == ch && e.Verification == v {
			return true
		}
	}
	return false
}

// registryPermits は (ch, v) の組が登録簿にあり、かつ操作 act を受けられるかを
// 判定する。
func registryPermits(ch Channel, v Verification, act attestedAction) bool {
	for _, e := range verificationRegistry {
		if e.Channel != ch || e.Verification != v {
			continue
		}
		if e.anyAction {
			return true
		}
		for _, a := range e.actions {
			if a == act {
				return true
			}
		}
		return false
	}
	return false
}

// Verify は本人確認つきの操作の入口（§クリティカル設計決定 1）。
//
// verifier が nil なら（呼び出し側の誤り）状態を変えず ErrVerificationRejected
// を返す（fail-closed。self-review 指摘: 公開 API が nil Verifier で panic
// しないようにする）。
//
// まず (ch, verifier.Method()) が登録簿に無ければ ErrVerificationRejected を
// 返し、verifier.Confirm を一切呼ばない（状態を変えない・端末にも触れない）。
// 登録簿にあれば verifier.Confirm(summary, expectedID) を呼び、エラーは
// そのまま返す。成立すれば Attestation を返す。
func Verify(ch Channel, verifier Verifier, summary, expectedID string) (Attestation, error) {
	if verifier == nil {
		return Attestation{}, ErrVerificationRejected
	}

	method := verifier.Method()
	if !registryAllows(ch, method) {
		return Attestation{}, ErrVerificationRejected
	}

	// 手がかりは relayed のときだけ載せる。手がかりの無い relayed は
	// Confirm にも触れず何も書かずに拒否する。
	var evidence RelayEvidence
	if method == VerificationRelayed {
		evidence = verifier.Evidence()
		if !evidence.complete() {
			return Attestation{}, ErrVerificationRejected
		}
	}

	actor, err := verifier.Confirm(summary, expectedID)
	if err != nil {
		return Attestation{}, err
	}
	return Attestation{actor: actor, channel: ch, verification: method, target: expectedID, evidence: evidence}, nil
}

// relayPtrs は返す Approval・Hold に載せる会話の手がかり（relayed のときだけ非 nil）。
func (a Attestation) relayPtrs() (session *string, quote *string) {
	if a.hasEvidence() {
		s, q := a.evidence.Session, a.evidence.Quote
		return &s, &q
	}
	return nil, nil
}
