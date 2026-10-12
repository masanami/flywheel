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
	CodeSerialized           = "serialized"
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
	case CodeStoreTooNew, CodeStoreBusy, CodeStoreError:
		return http.StatusServiceUnavailable
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
	case errors.Is(err, core.ErrSerialized):
		return CodeSerialized
	default:
		return CodeInternalError
	}
}

// apiCodeForCoreErr は API の失敗の本文に載せる code。§IF / API の表に列挙した値だけを通し、
// 表に無いコード（CLI の tty_required・config_*・run_in_progress・budget_exceeded・
// slot_unavailable・serialized、今後 CodeForCoreErr に足されるものを含む）は internal_error に寄せる。
func apiCodeForCoreErr(err error) string {
	switch code := CodeForCoreErr(err); code {
	case CodeNotFound, CodeStoreNotFound, CodeValidationFailed, CodeUsageError,
		CodeInvalidTransition, CodeTerminalState, CodeConflict, CodeLocked,
		CodeVerificationRejected, CodeConfirmationMismatch,
		CodeStoreTooNew, CodeStoreBusy, CodeStoreError, CodeInternalError:
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
