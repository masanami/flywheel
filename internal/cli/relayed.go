package cli

import (
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは (cli, relayed)（人間と会話しているエージェントが、会話で受けた
// 操作を記録する口）の Verifier を持つ。端末での確認（tty_confirm）はここでは
// 行わない。人間用の口（tty_confirm）は変えない。

// relayedFlags は approve・reject・answer・budget が受ける、relayed のフラグ。
func relayedFlags() []flagDef {
	return []flagDef{
		{Name: "relayed", HasValue: false},
		{Name: "session", HasValue: true},
		{Name: "quote", HasValue: true},
	}
}

// relayedVerifier は core.Verifier の relayed 実装。手がかり（--session・--quote）を
// core へ渡すだけで、操作の可否は core が決める。
type relayedVerifier struct {
	evidence core.RelayEvidence
}

func (v *relayedVerifier) Method() core.Verification { return core.VerificationRelayed }

func (v *relayedVerifier) Evidence() core.RelayEvidence { return v.evidence }

// Confirm は端末を使わず、OS のログインユーザー名を actor として返す。
func (v *relayedVerifier) Confirm(_, _ string) (string, error) {
	return core.CurrentActor()
}

// verifierFor は a のフラグから本人確認の Verifier を選ぶ。--relayed が無ければ
// tty_confirm。--session・--quote は --relayed のときだけ指定でき、--relayed の
// ときは両方必須（空白だけは無いものとみなす）。違反は usage_error。
func verifierFor(a Args) (core.Verifier, error) {
	session, hasSession := a.Values["session"]
	quote, hasQuote := a.Values["quote"]
	if !a.Bools["relayed"] {
		if hasSession || hasQuote {
			return nil, NewError(CodeUsageError, "--session・--quote は --relayed のときだけ指定できます")
		}
		return newTTYConfirmVerifier(a.Stdin), nil
	}
	if strings.TrimSpace(session) == "" {
		return nil, NewError(CodeUsageError, "--relayed には --session（会話の識別）が必要です")
	}
	if strings.TrimSpace(quote) == "" {
		return nil, NewError(CodeUsageError, "--relayed には --quote（発言の要旨）が必要です")
	}
	return &relayedVerifier{evidence: core.RelayEvidence{Session: session, Quote: quote}}, nil
}
