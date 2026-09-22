package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// TestTTYConfirmVerifier_MethodIsTTYConfirm は Verifier.Method() が
// core.VerificationTTYConfirm を返すこと（登録簿の判定に使われる値）。
func TestTTYConfirmVerifier_MethodIsTTYConfirm(t *testing.T) {
	v := newTTYConfirmVerifier(strings.NewReader(""))
	if v.Method() != core.VerificationTTYConfirm {
		t.Fatalf("Method() = %q, want %q", v.Method(), core.VerificationTTYConfirm)
	}
}

// TestTTYConfirmVerifier_Confirm_NonFileStdinIsNotATerminal は、stdin が
// *os.File でなければ（*strings.Reader など）、他の条件を検査するまでもなく
// 端末ではないと判定して ErrTTYRequired を返すことを検証する（プロセスを
// 起動しない単体テスト。子プロセスでの端末あり・なしの検証は
// confirm_process_test.go が行う）。
func TestTTYConfirmVerifier_Confirm_NonFileStdinIsNotATerminal(t *testing.T) {
	v := newTTYConfirmVerifier(strings.NewReader("C-12\n"))
	_, err := v.Confirm("summary", "C-12")
	if !errors.Is(err, core.ErrTTYRequired) {
		t.Fatalf("Confirm() error = %v, want ErrTTYRequired", err)
	}
}
