package invoker

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func hasConsecutive(args []string, seq ...string) bool {
	if len(seq) == 0 || len(args) < len(seq) {
		return false
	}
	for i := 0; i+len(seq) <= len(args); i++ {
		match := true
		for j, s := range seq {
			if args[i+j] != s {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func contains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// AC-28〜33・37〜40: 判断の呼び出しの引数が仕様どおりの6要素＋道具の制限3点を
// 含む。
func TestBuildArgs_ContainsAllRequiredFlags(t *testing.T) {
	in := core.JudgmentLaunchInput{
		SessionID:    "11111111-1111-1111-1111-111111111111",
		OutputSchema: []byte(`{"type":"object"}`),
		MaxBudgetUSD: 1.5,
	}
	args := buildArgs(in)

	if args[0] != "-p" {
		t.Errorf("args[0] = %q, want -p", args[0])
	}
	if !hasConsecutive(args, "--session-id", in.SessionID) {
		t.Errorf("args = %v, missing --session-id %s", args, in.SessionID)
	}
	if !hasConsecutive(args, "--output-format", "json") {
		t.Errorf("args = %v, missing --output-format json", args)
	}
	if !hasConsecutive(args, "--json-schema", `{"type":"object"}`) {
		t.Errorf("args = %v, missing --json-schema", args)
	}
	if !hasConsecutive(args, "--max-budget-usd", "1.5") {
		t.Errorf("args = %v, missing --max-budget-usd 1.5", args)
	}
	if !hasConsecutive(args, "--allowedTools", "Read", "Grep", "Glob") {
		t.Errorf("args = %v, missing --allowedTools Read Grep Glob", args)
	}
	if !hasConsecutive(args, "--disallowedTools", "Bash", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task") {
		t.Errorf("args = %v, missing --disallowedTools list", args)
	}
	if !hasConsecutive(args, "--permission-mode", "default") {
		t.Errorf("args = %v, missing --permission-mode default", args)
	}
}

// AC「--allowedTools にRead・Grep・Globだけを含む」: 直後のトークンがこの3つ
// だけで、次のフラグ（--disallowedTools）が続くこと。
func TestBuildArgs_AllowedToolsIsExactlyThreeTools(t *testing.T) {
	args := buildArgs(core.JudgmentLaunchInput{SessionID: "s", MaxBudgetUSD: 1})
	idx := -1
	for i, a := range args {
		if a == "--allowedTools" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("--allowedTools not found")
	}
	got := args[idx+1 : idx+4]
	want := []string{"Read", "Grep", "Glob"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("--allowedTools values = %v, want %v", got, want)
		}
	}
	if idx+4 >= len(args) || args[idx+4] != "--disallowedTools" {
		t.Fatalf("--allowedTools has extra values beyond Read/Grep/Glob: %v", args[idx:idx+5])
	}
}

// AC「課題の本文は、判断の呼び出しの引数に現れず」: 外部由来の文字列は
// buildArgs に一切渡さない設計であることを、引数の要素数が固定（外部入力に
// 依存しない）であることで裏付ける。
func TestBuildArgs_DoesNotEmbedStdinContent(t *testing.T) {
	args := buildArgs(core.JudgmentLaunchInput{SessionID: "s", MaxBudgetUSD: 1, Stdin: []byte("SECRET-BODY-TEXT")})
	if contains(args, "SECRET-BODY-TEXT") {
		t.Fatalf("args contain stdin content: %v", args)
	}
}

// self-review 指摘（round2, code-reviewer PLAUSIBLE）を受けて足した:
// in.IsResume の有無で --session-id / --resume を切り替えることを固定する。
func TestBuildArgs_UsesResumeFlagWhenResuming(t *testing.T) {
	in := core.JudgmentLaunchInput{SessionID: "22222222-2222-2222-2222-222222222222", MaxBudgetUSD: 1, IsResume: true}
	args := buildArgs(in)
	if !hasConsecutive(args, "--resume", in.SessionID) {
		t.Errorf("args = %v, want --resume %s for a resumed run", args, in.SessionID)
	}
	if contains(args, "--session-id") {
		t.Errorf("args = %v, must not also contain --session-id when resuming", args)
	}
}

func TestBuildArgs_UsesSessionIDFlagForNewSession(t *testing.T) {
	in := core.JudgmentLaunchInput{SessionID: "11111111-1111-1111-1111-111111111111", MaxBudgetUSD: 1, IsResume: false}
	args := buildArgs(in)
	if !hasConsecutive(args, "--session-id", in.SessionID) {
		t.Errorf("args = %v, want --session-id %s for a new session", args, in.SessionID)
	}
	if contains(args, "--resume") {
		t.Errorf("args = %v, must not contain --resume for a new session", args)
	}
}

func TestFormatUSDArg(t *testing.T) {
	cases := map[float64]string{1: "1", 0.5: "0.5", 1.25: "1.25", 200: "200"}
	for in, want := range cases {
		if got := formatUSDArg(in); got != want {
			t.Errorf("formatUSDArg(%v) = %q, want %q", in, got, want)
		}
	}
}
