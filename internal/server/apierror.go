package server

import (
	"errors"
	"net/http"

	"github.com/masanami/flywheel/internal/core"
)

// API が返すエラーコード。internal/cli のエラーコードの表と同じ値で、cli のテストが
// 一致を検査する。CodeForCoreErr は CLI の写像と同じ（cli のテストが検査）。API の本文は apiCodeForCoreErr で表の閉集合に寄せ、errorStatus と共に以降の全 API が共有する。
const (
	CodeUsageError           = "usage_error"
	CodeStoreBusy            = "store_busy"
	CodeInternalError        = "internal_error"
	CodeValidationFailed     = "validation_failed"
	CodeInvalidTransition    = "invalid_transition"
	CodeTerminalState        = "terminal_state"
	CodeConflict             = "conflict"
	CodeLocked               = "locked"
	CodeVerificationRejected = "verification_rejected"
	CodeConfirmationMismatch = "confirmation_mismatch"
	CodeTTYRequired          = "tty_required"
	CodeConfigNotFound       = "config_not_found"
	CodeConfigInvalid        = "config_invalid"
	CodeRunInProgress        = "run_in_progress"
	CodeBudgetExceeded       = "budget_exceeded"
	CodeSlotUnavailable      = "slot_unavailable"
	// CodeUpstreamUnavailable は上流（GitHub）を読めなかった（取り込みの口。502）。CLI も同じコードを持つ（CLI の写像は internal/cli）。この API では、取り込めるものでない指定（ErrValidation）を先に 400 にするため、CodeForCoreErr の前で判定する。
	CodeUpstreamUnavailable = "upstream_unavailable"
)

// errorStatus はエラーコードから HTTP の状態コードを決める（閉集合の表。表に無いものは 500）。
func errorStatus(code string) int {
	switch code {
	case CodeNotFound, CodeStoreNotFound:
		return http.StatusNotFound
	case CodeValidationFailed, CodeUsageError:
		return http.StatusBadRequest
	case CodeInvalidTransition, CodeTerminalState, CodeConflict, CodeLocked:
		return http.StatusConflict
	case CodeVerificationRejected, CodeConfirmationMismatch:
		return http.StatusForbidden
	case CodeStoreTooNew, CodeStoreBusy, CodeStoreError, CodeEngineUnavailable:
		return http.StatusServiceUnavailable
	case CodeUpstreamUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// CodeForCoreErr は core のエラーを CLI の mapCoreErr と同じエラーコードへ写す。
func CodeForCoreErr(err error) string {
	switch {
	case errors.Is(err, core.ErrStoreNotFound):
		return CodeStoreNotFound
	case errors.Is(err, core.ErrStoreTooNew):
		return CodeStoreTooNew
	case errors.Is(err, core.ErrStoreBusy):
		return CodeStoreBusy
	case errors.Is(err, core.ErrStoreError):
		return CodeStoreError
	case errors.Is(err, core.ErrNotFound):
		return CodeNotFound
	case errors.Is(err, core.ErrValidation):
		return CodeValidationFailed
	case errors.Is(err, core.ErrTerminalState):
		return CodeTerminalState
	case errors.Is(err, core.ErrInvalidTransition):
		return CodeInvalidTransition
	case errors.Is(err, core.ErrConflict):
		return CodeConflict
	case errors.Is(err, core.ErrActorUnavailable):
		return CodeInternalError
	case errors.Is(err, core.ErrTTYRequired):
		return CodeTTYRequired
	case errors.Is(err, core.ErrConfirmationMismatch):
		return CodeConfirmationMismatch
	case errors.Is(err, core.ErrVerificationRejected):
		return CodeVerificationRejected
	case errors.Is(err, core.ErrConfigNotFound):
		return CodeConfigNotFound
	case errors.Is(err, core.ErrConfigInvalid):
		return CodeConfigInvalid
	case errors.Is(err, core.ErrLocked):
		return CodeLocked
	case errors.Is(err, core.ErrRunInProgress):
		return CodeRunInProgress
	case errors.Is(err, core.ErrBudgetExceeded):
		return CodeBudgetExceeded
	case errors.Is(err, core.ErrSlotUnavailable):
		return CodeSlotUnavailable
	default:
		return CodeInternalError
	}
}

// apiCodeForCoreErr は API の失敗の本文に載せる code。§IF / API の表に列挙した値だけを通し、
// 表に無いコード（CLI の tty_required・config_*・run_in_progress・budget_exceeded・
// slot_unavailable、今後 CodeForCoreErr に足されるものを含む）は internal_error に寄せる。
//
// 画面のボタンの口（回答・取り込み・フォローアップ）の誤りは、この表で次の状態コードになる: 問いか選択肢が変わった・保留が閉じていた（core.ErrConflict）は 409、
// ボタンで受けない操作（core.ErrVerificationRejected）は 403、保留に無い識別・識別なし・添え書きつき・取り込めるものでない指定（core.ErrValidation）は 400、
// 保留が無い・ほかの課題の保留（core.ErrNotFound）は 404、上流を読めない（core.ErrUpstreamFetchFailed）は 502。
func apiCodeForCoreErr(err error) string {
	if errors.Is(err, core.ErrUpstreamFetchFailed) && !errors.Is(err, core.ErrValidation) {
		return CodeUpstreamUnavailable
	}
	switch code := CodeForCoreErr(err); code {
	case CodeNotFound, CodeStoreNotFound, CodeValidationFailed, CodeUsageError,
		CodeInvalidTransition, CodeTerminalState, CodeConflict, CodeLocked,
		CodeVerificationRejected, CodeConfirmationMismatch,
		CodeStoreTooNew, CodeStoreBusy, CodeStoreError, CodeConfigInvalid, CodeInternalError:
		return code
	default:
		return CodeInternalError
	}
}

// writeCoreError は core のエラーを、写像した状態コードと本文で返す。
func writeCoreError(w http.ResponseWriter, err error) {
	code := apiCodeForCoreErr(err)
	writeError(w, errorStatus(code), code, err.Error())
}

// notImplementedPost は、経路を登録しただけで中身を置いていない書き込みの口の応答。中身を置くまでは、ルートが無いときと同じ 404 を返す。
func notImplementedPost(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, CodeNotFound, "not found")
}
