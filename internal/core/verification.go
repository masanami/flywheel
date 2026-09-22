package core

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
}

// Actor は本人確認が成立した actor を返す。
func (a Attestation) Actor() string { return a.actor }

// Channel は本人確認が成立した経路を返す。
func (a Attestation) Channel() Channel { return a.channel }

// Verification は本人確認が成立した方式を返す。
func (a Attestation) Verification() Verification { return a.verification }

// verificationRegistryEntry は許可した (Channel, Verification) の組。
type verificationRegistryEntry struct {
	Channel      Channel
	Verification Verification
}

// verificationRegistry は許可した (経路, 本人確認の方式) の組の登録簿
// （§クリティカル設計決定 1）。データ（テーブル）として持ち、M4 で経路・方式を
// 足すときはここに行を足すだけで core の遷移規則は変えない。M1 の登録簿は
// (cli, tty_confirm) の 1 組だけ。
var verificationRegistry = []verificationRegistryEntry{
	{Channel: ChannelCLI, Verification: VerificationTTYConfirm},
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

	actor, err := verifier.Confirm(summary, expectedID)
	if err != nil {
		return Attestation{}, err
	}
	return Attestation{actor: actor, channel: ch, verification: method, target: expectedID}, nil
}
