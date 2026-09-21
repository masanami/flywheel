package cli

import (
	"errors"

	"github.com/masanami/flywheel/internal/core"
)

// ErrorCode は CLI が返すエラーの機械可読な識別子の閉集合である。
// 正本は docs/features/m1-core.md の「CLI 共通」節にあるエラーコード表であり、
// このファイルの errorCodeTable はその表と 1 対 1 に対応する
// （internal/cli/errors_test.go の TestErrorCodeTableMatchesSpec が双方向に照合する）。
//
// 語彙の置き場所について: 14 コードのうち大半（store_* / not_found / invalid_transition
// / conflict / tty_required 等）は意味的には internal/core（と core 配下の store）で
// 発生する事象だが、Issue #5 の時点では internal/core が存在しない。CLI は core の公開
// API だけを呼ぶ設計（P2）のため、依存方向は cli → core に決まっている。後続チケットで
// core を作る際は、(a) core が型付き sentinel error（コード文字列を持たない）を返し
// cli 側で ErrorCode へ写像する、(b) 語彙の正本を core 側（例えば internal/core が公開
// する型）へ移し cli はそれを再輸出する、のいずれかを選び、cli と core で同じ 14 コード
// を二重に持たないようにすること。本チケットでは「1 箇所に定義する」という完了条件を
// 満たすため、暫定的に cli 側に置く（design-reviewer 指摘・実装チケットでの仮定として
// 記録）。
type ErrorCode string

// エラーコードの定数。ErrorCode を生成する箇所は必ずこれらの定数を経由する
// （internal/cli/codeusage_test.go が静的検査で担保する）。
const (
	CodeUsageError           ErrorCode = "usage_error"
	CodeStoreNotFound        ErrorCode = "store_not_found"
	CodeStoreTooNew          ErrorCode = "store_too_new"
	CodeStoreBusy            ErrorCode = "store_busy"
	CodeStoreError           ErrorCode = "store_error"
	CodeInternalError        ErrorCode = "internal_error"
	CodeNotFound             ErrorCode = "not_found"
	CodeValidationFailed     ErrorCode = "validation_failed"
	CodeInvalidTransition    ErrorCode = "invalid_transition"
	CodeTerminalState        ErrorCode = "terminal_state"
	CodeConflict             ErrorCode = "conflict"
	CodeTTYRequired          ErrorCode = "tty_required"
	CodeConfirmationMismatch ErrorCode = "confirmation_mismatch"
	CodeVerificationRejected ErrorCode = "verification_rejected"
)

// codeExit は 1 つのエラーコードと、それに対応する終了コード（0/1/2 のみ）の組。
type codeExit struct {
	Code     ErrorCode
	ExitCode int
}

// errorCodeTable はエラーコード→終了コードの写像を持つ唯一の箇所。
// 順序は docs/features/m1-core.md の表の順序と一致させる。
var errorCodeTable = []codeExit{
	{CodeUsageError, 2},
	{CodeStoreNotFound, 2},
	{CodeStoreTooNew, 2},
	{CodeStoreBusy, 2},
	{CodeStoreError, 2},
	{CodeInternalError, 2},
	{CodeNotFound, 1},
	{CodeValidationFailed, 1},
	{CodeInvalidTransition, 1},
	{CodeTerminalState, 1},
	{CodeConflict, 1},
	{CodeTTYRequired, 1},
	{CodeConfirmationMismatch, 1},
	{CodeVerificationRejected, 1},
}

// ExitCodeFor は既知の ErrorCode に対応する終了コードを返す。
// 表に無いコードは呼び出し側の誤りであり、internal_error と同じ終了コード 2 を返す。
func ExitCodeFor(code ErrorCode) int {
	for _, e := range errorCodeTable {
		if e.Code == code {
			return e.ExitCode
		}
	}
	return 2
}

// Error は ErrorCode を伴うエラー。生成は NewError を経由する。
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// NewError は Error を生成する唯一の関数。code には必ずこのファイルの
// Code* 定数を渡す（文字列リテラルや型変換によるコード生成は
// internal/cli/codeusage_test.go が検出する）。
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

// mapCoreErr は internal/core が返す sentinel error（errors.go の設計メモの
// 案 (a)）を、CLI の ErrorCode を持つ *Error へ写像する唯一の箇所。
// 表に無い・core 由来ではないエラーは internal_error にする（fail-closed）。
func mapCoreErr(err error) *Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, core.ErrStoreNotFound):
		return NewError(CodeStoreNotFound, err.Error())
	case errors.Is(err, core.ErrStoreTooNew):
		return NewError(CodeStoreTooNew, err.Error())
	case errors.Is(err, core.ErrStoreBusy):
		return NewError(CodeStoreBusy, err.Error())
	case errors.Is(err, core.ErrStoreError):
		return NewError(CodeStoreError, err.Error())
	default:
		return NewError(CodeInternalError, err.Error())
	}
}
