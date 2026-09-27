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
	spec := loadSpecErrorCodeTable(t)

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
