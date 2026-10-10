package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func TestExitCodeFor_KnownCodes(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want int
	}{
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
		{CodeConfigNotFound, 2},
		{CodeConfigInvalid, 2},
		{CodeUpstreamUnavailable, 2},
		// M3 S1 の 4 つ（AC-158〜161。#86）。
		{CodeInvokerUnavailable, 2},
		{CodeLocked, 1},
		{CodeRunInProgress, 1},
		{CodeBudgetExceeded, 1},
	}
	for _, c := range cases {
		if got := ExitCodeFor(c.code); got != c.want {
			t.Errorf("ExitCodeFor(%q) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeFor_UnknownCodeFallsBackToInternalError(t *testing.T) {
	if got := ExitCodeFor(ErrorCode("not_a_real_code")); got != 2 {
		t.Errorf("ExitCodeFor(unknown) = %d, want 2 (internal_error 相当)", got)
	}
}

// TestMapCoreErr_VerificationErrors は #11 で足した 3 つの sentinel error
// （core.ErrTTYRequired・ErrConfirmationMismatch・ErrVerificationRejected）が
// mapCoreErr で正しい ErrorCode（終了コード 1）へ写像されることを検証する
// （AC-37・AC-44・AC-50）。
func TestMapCoreErr_VerificationErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"tty required", core.ErrTTYRequired, CodeTTYRequired},
		{"confirmation mismatch", core.ErrConfirmationMismatch, CodeConfirmationMismatch},
		{"verification rejected", core.ErrVerificationRejected, CodeVerificationRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapCoreErr(c.err)
			if got.Code != c.want {
				t.Fatalf("mapCoreErr(%v).Code = %q, want %q", c.err, got.Code, c.want)
			}
			if ExitCodeFor(got.Code) != 1 {
				t.Fatalf("ExitCodeFor(%q) = %d, want 1", got.Code, ExitCodeFor(got.Code))
			}
		})
	}
}

// TestMapCoreErr_ConfigErrors は #54 で足した core.ErrConfigNotFound・
// core.ErrConfigInvalid が正しい ErrorCode（終了コード 2）へ写像されることを
// 検証する（AC-1・AC-2）。
func TestMapCoreErr_ConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"config not found", core.ErrConfigNotFound, CodeConfigNotFound},
		{"config invalid", core.ErrConfigInvalid, CodeConfigInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapCoreErr(c.err)
			if got.Code != c.want {
				t.Fatalf("mapCoreErr(%v).Code = %q, want %q", c.err, got.Code, c.want)
			}
			if ExitCodeFor(got.Code) != 2 {
				t.Fatalf("ExitCodeFor(%q) = %d, want 2", got.Code, ExitCodeFor(got.Code))
			}
		})
	}
}

// TestMapCoreErr_M3Errors は M3 S1 の core の sentinel（ErrLocked・ErrRunInProgress・
// ErrBudgetExceeded）が mapCoreErr で正しい ErrorCode（AC-159〜161: いずれも終了コード 1）へ
// 写像されることを検証する。invoker_unavailable（AC-158: 終了コード 2）は core の sentinel を
// 経由せず、runClassifyAuto・runPlanAuto・runCycle が invoker.ErrClaudeNotFound から直接作る
// （その終了コードは各コマンドのテストが検証する）。
func TestMapCoreErr_M3Errors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"locked", core.ErrLocked, CodeLocked},                          // AC-159
		{"run in progress", core.ErrRunInProgress, CodeRunInProgress},   // AC-160
		{"budget exceeded", core.ErrBudgetExceeded, CodeBudgetExceeded}, // AC-161
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapCoreErr(c.err)
			if got.Code != c.want {
				t.Fatalf("mapCoreErr(%v).Code = %q, want %q", c.err, got.Code, c.want)
			}
			if ExitCodeFor(got.Code) != 1 {
				t.Fatalf("ExitCodeFor(%q) = %d, want 1", got.Code, ExitCodeFor(got.Code))
			}
		})
	}
}

// TestMapCoreErr_InvalidTransition は #10 で足した core.ErrInvalidTransition が
// CodeInvalidTransition（終了コード 1）へ写像されることを検証する（AC-24）。
func TestMapCoreErr_InvalidTransition(t *testing.T) {
	got := mapCoreErr(core.ErrInvalidTransition)
	if got.Code != CodeInvalidTransition {
		t.Fatalf("mapCoreErr(ErrInvalidTransition).Code = %q, want %q", got.Code, CodeInvalidTransition)
	}
	if ExitCodeFor(got.Code) != 1 {
		t.Fatalf("ExitCodeFor(%q) = %d, want 1", got.Code, ExitCodeFor(got.Code))
	}
}

// TestMapCoreErr_Conflict は #12 で足した core.ErrConflict が CodeConflict
// （終了コード 1）へ写像されることを検証する（AC-46）。
func TestMapCoreErr_Conflict(t *testing.T) {
	got := mapCoreErr(core.ErrConflict)
	if got.Code != CodeConflict {
		t.Fatalf("mapCoreErr(ErrConflict).Code = %q, want %q", got.Code, CodeConflict)
	}
	if ExitCodeFor(got.Code) != 1 {
		t.Fatalf("ExitCodeFor(%q) = %d, want 1", got.Code, ExitCodeFor(got.Code))
	}
}

func TestNewError_SetsCodeAndMessage(t *testing.T) {
	err := NewError(CodeNotFound, "C-1 not found")
	if err.Code != CodeNotFound {
		t.Errorf("Code = %q, want %q", err.Code, CodeNotFound)
	}
	if err.Error() != "C-1 not found" {
		t.Errorf("Error() = %q, want %q", err.Error(), "C-1 not found")
	}
}

func TestParseErrorCodeTable_Synthetic(t *testing.T) {
	doc := "intro\n\n| エラーコード | 終了コード | 意味 |\n" +
		"|---|---|---|\n" +
		"| `foo_error` | 2 | test |\n" +
		"| `bar_error` | 1 | test2 |\n\n" +
		"after\n"
	got := parseErrorCodeTable(t, doc)
	want := []codeExit{{"foo_error", 2}, {"bar_error", 1}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestErrorCodeTableMatchesSpec(t *testing.T) {
	spec := append(loadSpecErrorCodeTable(t), loadM4ErrorCodeTable(t)...)

	if len(spec) != len(errorCodeTable) {
		t.Fatalf("spec table has %d rows, implementation has %d (spec=%+v impl=%+v)",
			len(spec), len(errorCodeTable), spec, errorCodeTable)
	}
	for i := range spec {
		if spec[i] != errorCodeTable[i] {
			t.Errorf("row %d differs (順序も含めて一致が必要): spec=%+v impl=%+v", i, spec[i], errorCodeTable[i])
		}
	}

	specSet := map[ErrorCode]int{}
	for _, e := range spec {
		specSet[e.Code] = e.ExitCode
	}
	implSet := map[ErrorCode]int{}
	for _, e := range errorCodeTable {
		implSet[e.Code] = e.ExitCode
	}
	for code, exit := range specSet {
		if got, ok := implSet[code]; !ok || got != exit {
			t.Errorf("spec code %q (exit %d) missing or mismatched in implementation (got exit=%d, ok=%v)", code, exit, got, ok)
		}
	}
	for code, exit := range implSet {
		if got, ok := specSet[code]; !ok || got != exit {
			t.Errorf("implementation code %q (exit %d) not present in spec table (got exit=%d, ok=%v)", code, exit, got, ok)
		}
	}
}

func loadSpecErrorCodeTable(t *testing.T) []codeExit {
	t.Helper()
	path := filepath.Join(repoRoot(t), "docs", "features", "m1-core.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	return parseErrorCodeTable(t, string(data))
}

// loadM4ErrorCodeTable は docs/features/m4-ui-server.md の「エラーコードの追加」の表を読む。
func loadM4ErrorCodeTable(t *testing.T) []codeExit {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m4-ui-server.md"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	return parseErrorCodeTable(t, string(data))
}

// parseErrorCodeTable は docs/features/m1-core.md の「CLI 共通」節にある
// エラーコード表（機能要件側。見出し行 "| エラーコード | 終了コード | 意味 |" を
// 目印にする）を解析する。AC-76 の枠組みが参照する「表の集合」の取得元。
func parseErrorCodeTable(t *testing.T, doc string) []codeExit {
	t.Helper()
	lines := strings.Split(doc, "\n")
	var table []codeExit
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inTable {
			if trimmed == "| エラーコード | 終了コード | 意味 |" {
				inTable = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "|---") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		cols := strings.Split(trimmed, "|")
		if len(cols) < 4 {
			t.Fatalf("malformed error code table row: %q", line)
		}
		code := strings.Trim(strings.TrimSpace(cols[1]), "`")
		exit, err := strconv.Atoi(strings.Trim(strings.TrimSpace(cols[2]), "`"))
		if err != nil {
			t.Fatalf("parse exit code from %q: %v", line, err)
		}
		table = append(table, codeExit{Code: ErrorCode(code), ExitCode: exit})
	}
	if len(table) == 0 {
		t.Fatal("error code table not found in document")
	}
	return table
}

// m3S2OnlyErrorCodes は m3 の「エラーコードの追加」の表にあるが S2 で足すコード
// （AC-162 の対象は S1 の 4 つだけ）。
var m3S2OnlyErrorCodes = map[ErrorCode]bool{"slot_unavailable": true, "serialized": true}

// m3S2ImplementedErrorCodes は m3S2OnlyErrorCodes のうち、実装済みで期待の集合に数えるもの
// （slot_unavailable は #102、serialized は #106 が実装し、両方が期待の集合に入る。AC-367）。
var m3S2ImplementedErrorCodes = map[ErrorCode]bool{"slot_unavailable": true, "serialized": true}

// m1BaseErrorCodeCount は M1 の表のうち、M1 自身が定めた行数（`verification_rejected` まで。
// その後ろの行は M2・M3 の実装チケットが同じ表へ足したもの）。
const m1BaseErrorCodeCount = 14

// AC-162: 実装が出しうるエラーコードの集合は、M1・M2 の表に S1 の 4 つ（invoker_unavailable・
// locked・run_in_progress・budget_exceeded）と S2 の 2 つ（slot_unavailable・serialized）を足した集合と一致する。M1・M2・M3 それぞれの
// 仕様の表から期待の集合を導き、実装の表（errorCodeTable）と終了コードも含めて双方向に照合する
// （実装が実際に出しうることの静的な照合は TestErrorCodeUsageInSourcesMatchesSpecInBothDirections が
// M1 の表との間で行う）。
func TestErrorCodeSet_IsM1M2PlusS1CodesInBothDirections(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	m1 := parseErrorCodeTable(t, read("m1-core.md"))
	if len(m1) < m1BaseErrorCodeCount || m1[m1BaseErrorCodeCount-1].Code != "verification_rejected" {
		t.Fatalf("the M1 error code table does not start with the %d M1 rows ending at verification_rejected: %+v", m1BaseErrorCodeCount, m1)
	}
	want := map[ErrorCode]int{}
	for _, e := range m1[:m1BaseErrorCodeCount] {
		want[e.Code] = e.ExitCode
	}
	m2 := parseErrorCodeTable(t, read("m2-github-issue-ingest.md"))
	if len(m2) != 3 {
		t.Fatalf("M2 adds 3 error codes, table has %d: %+v", len(m2), m2)
	}
	for _, e := range m2 {
		want[e.Code] = e.ExitCode
	}
	m3 := parseErrorCodeTable(t, read("m3-invoker-delegation.md"))
	s1 := 0
	for _, e := range m3 {
		if m3S2OnlyErrorCodes[e.Code] {
			if m3S2ImplementedErrorCodes[e.Code] {
				want[e.Code] = e.ExitCode
			}
			continue
		}
		want[e.Code] = e.ExitCode
		s1++
	}
	if s1 != 4 {
		t.Fatalf("M3 S1 adds 4 error codes (invoker_unavailable, locked, run_in_progress, budget_exceeded), table has %d besides the S2-only ones: %+v", s1, m3)
	}

	m4 := parseErrorCodeTable(t, read("m4-ui-server.md"))
	if len(m4) != 2 {
		t.Fatalf("M4 adds 2 error codes (listen_failed, forbidden_origin), table has %d: %+v", len(m4), m4)
	}
	for _, e := range m4 {
		want[e.Code] = e.ExitCode
	}

	got := map[ErrorCode]int{}
	for _, e := range errorCodeTable {
		got[e.Code] = e.ExitCode
	}
	for code, exit := range want {
		if g, ok := got[code]; !ok || g != exit {
			t.Errorf("expected code %q (exit %d) is missing or mismatched in the implementation (got exit=%d, present=%v)", code, exit, g, ok)
		}
	}
	for code, exit := range got {
		if w, ok := want[code]; !ok || w != exit {
			t.Errorf("implementation code %q (exit %d) is not in the M1+M2+S1 set (want exit=%d, present=%v)", code, exit, w, ok)
		}
	}
}
