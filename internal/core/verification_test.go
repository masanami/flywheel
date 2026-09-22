package core

import (
	"errors"
	"testing"
)

// fakeVerifier は core のテスト内だけで使う Verifier のフェイク実装
// （#11 設計方針: 「フェイク Verifier は core のテスト内だけで使う」）。
// confirmCalls で Confirm の呼び出し回数を数え、Verify が登録簿に無い組で
// Confirm を一切呼ばないことを検証する。
type fakeVerifier struct {
	method       Verification
	confirmCalls int
	actor        string
	err          error
}

func (f *fakeVerifier) Method() Verification { return f.method }

func (f *fakeVerifier) Confirm(_, _ string) (string, error) {
	f.confirmCalls++
	return f.actor, f.err
}

func TestVerificationRegistry_AllowsOnlyCLITTYConfirm(t *testing.T) {
	if !registryAllows(ChannelCLI, VerificationTTYConfirm) {
		t.Fatal("want (cli, tty_confirm) to be allowed")
	}
	if registryAllows(ChannelCLI, VerificationNone) {
		t.Fatal("want (cli, none) to be rejected")
	}
	if registryAllows(Channel("unknown"), VerificationTTYConfirm) {
		t.Fatal("want unknown channel to be rejected")
	}
	if registryAllows(ChannelCLI, Verification("unknown")) {
		t.Fatal("want unknown verification method to be rejected")
	}
}

func TestVerificationRegistry_HasExactlyOneEntryInM1(t *testing.T) {
	if len(verificationRegistry) != 1 {
		t.Fatalf("len(verificationRegistry) = %d, want 1", len(verificationRegistry))
	}
	want := verificationRegistryEntry{Channel: ChannelCLI, Verification: VerificationTTYConfirm}
	if verificationRegistry[0] != want {
		t.Fatalf("verificationRegistry[0] = %+v, want %+v", verificationRegistry[0], want)
	}
}

func TestVerify_RejectsUnregisteredChannelWithoutCallingConfirm(t *testing.T) {
	fv := &fakeVerifier{method: VerificationNone, actor: "alice"}
	_, err := Verify(ChannelCLI, fv, "summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify() error = %v, want ErrVerificationRejected", err)
	}
	if fv.confirmCalls != 0 {
		t.Fatalf("Confirm called %d times, want 0 (registry check must short-circuit before touching the verifier)", fv.confirmCalls)
	}
}

func TestVerify_RejectsUnknownVerificationMethodWithoutCallingConfirm(t *testing.T) {
	fv := &fakeVerifier{method: Verification("unknown"), actor: "alice"}
	_, err := Verify(ChannelCLI, fv, "summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify() error = %v, want ErrVerificationRejected", err)
	}
	if fv.confirmCalls != 0 {
		t.Fatalf("Confirm called %d times, want 0", fv.confirmCalls)
	}
}

func TestVerify_RejectsUnknownChannelWithoutCallingConfirm(t *testing.T) {
	fv := &fakeVerifier{method: VerificationTTYConfirm, actor: "alice"}
	_, err := Verify(Channel("mobile"), fv, "summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify() error = %v, want ErrVerificationRejected", err)
	}
	if fv.confirmCalls != 0 {
		t.Fatalf("Confirm called %d times, want 0", fv.confirmCalls)
	}
}

func TestVerify_SucceedsAndReturnsAttestationForRegisteredEntry(t *testing.T) {
	fv := &fakeVerifier{method: VerificationTTYConfirm, actor: "alice"}
	att, err := Verify(ChannelCLI, fv, "summary", "C-1")
	if err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
	if fv.confirmCalls != 1 {
		t.Fatalf("Confirm called %d times, want 1", fv.confirmCalls)
	}
	if att.Actor() != "alice" {
		t.Fatalf("Attestation.Actor() = %q, want %q", att.Actor(), "alice")
	}
	if att.Channel() != ChannelCLI {
		t.Fatalf("Attestation.Channel() = %q, want %q", att.Channel(), ChannelCLI)
	}
	if att.Verification() != VerificationTTYConfirm {
		t.Fatalf("Attestation.Verification() = %q, want %q", att.Verification(), VerificationTTYConfirm)
	}
}

// TestVerify_RejectsNilVerifierWithoutPanicking は self-review 指摘の再発防止:
// verifier が nil でも Verify が panic せず ErrVerificationRejected で
// fail-closed に拒否すること。
func TestVerify_RejectsNilVerifierWithoutPanicking(t *testing.T) {
	_, err := Verify(ChannelCLI, nil, "summary", "C-1")
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("Verify() error = %v, want ErrVerificationRejected", err)
	}
}

func TestVerify_PropagatesErrTTYRequiredFromConfirm(t *testing.T) {
	fv := &fakeVerifier{method: VerificationTTYConfirm, err: ErrTTYRequired}
	_, err := Verify(ChannelCLI, fv, "summary", "C-1")
	if !errors.Is(err, ErrTTYRequired) {
		t.Fatalf("Verify() error = %v, want ErrTTYRequired", err)
	}
	if fv.confirmCalls != 1 {
		t.Fatalf("Confirm called %d times, want 1", fv.confirmCalls)
	}
}

func TestVerify_PropagatesErrConfirmationMismatchFromConfirm(t *testing.T) {
	fv := &fakeVerifier{method: VerificationTTYConfirm, err: ErrConfirmationMismatch}
	_, err := Verify(ChannelCLI, fv, "summary", "C-1")
	if !errors.Is(err, ErrConfirmationMismatch) {
		t.Fatalf("Verify() error = %v, want ErrConfirmationMismatch", err)
	}
}
