package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeAgentJSON はワークスペース dir/.flywheel/agent.json に content を書き、
// dir を返す（sources_test.go の writeSourcesJSON と同じ規則）。
func writeAgentJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	fwDir := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fwDir, err)
	}
	if err := os.WriteFile(filepath.Join(fwDir, "agent.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent.json: %v", err)
	}
	return dir
}

// --- agent.json が無い場合: 全既定値（config_invalid ではない） ---

func TestLoadAgentDeclaration_FileNotFoundUsesAllDefaults(t *testing.T) {
	dir := t.TempDir() // .flywheel/agent.json を作らない
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if !decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = false, want true")
	}
	if decl.PositionFile != "" {
		t.Errorf("PositionFile = %q, want \"\"", decl.PositionFile)
	}
	if decl.CycleBudgetUSD != 300 {
		t.Errorf("CycleBudgetUSD = %v, want 300", decl.CycleBudgetUSD)
	}
	if decl.MaxRunBudgetUSD != 200 {
		t.Errorf("MaxRunBudgetUSD = %v, want 200", decl.MaxRunBudgetUSD)
	}
	wantSize := SizeBudgetsUSD{S: SizeBudgetPair{30, 25}, M: SizeBudgetPair{50, 30}, L: SizeBudgetPair{100, 40}}
	if decl.SizeBudgetsUSD != wantSize {
		t.Errorf("SizeBudgetsUSD = %+v, want %+v", decl.SizeBudgetsUSD, wantSize)
	}
	wantJudgment := JudgmentBudgetUSD{J1: 1, J2: 5, J3: 3, J4: 2, J5: 5}
	if decl.JudgmentBudgetUSD != wantJudgment {
		t.Errorf("JudgmentBudgetUSD = %+v, want %+v", decl.JudgmentBudgetUSD, wantJudgment)
	}
	wantTimeout := AgentTimeoutSec{Judgment: 900, Delegate: 14400}
	if decl.TimeoutSec != wantTimeout {
		t.Errorf("TimeoutSec = %+v, want %+v", decl.TimeoutSec, wantTimeout)
	}
	if decl.MaxParallelRuns != 2 {
		t.Errorf("MaxParallelRuns = %v, want 2", decl.MaxParallelRuns)
	}
	if decl.ReworkLimit != 3 {
		t.Errorf("ReworkLimit = %v, want 3", decl.ReworkLimit)
	}
	if decl.FailureLimit != 2 {
		t.Errorf("FailureLimit = %v, want 2", decl.FailureLimit)
	}
}

// AC: position_file だけを持つ agent.json は、DefaultsUsed が真になる。
func TestLoadAgentDeclaration_PositionFileOnly_DefaultsUsedTrue(t *testing.T) {
	dir := writeAgentJSON(t, `{"position_file": "positions/harness.md"}`)
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if !decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = false, want true")
	}
	if decl.PositionFile != "positions/harness.md" {
		t.Errorf("PositionFile = %q", decl.PositionFile)
	}
	if decl.CycleBudgetUSD != 300 {
		t.Errorf("CycleBudgetUSD = %v, want 300 (default)", decl.CycleBudgetUSD)
	}
}

// AC: すべてのキーを明示した agent.json は、DefaultsUsed が偽になる。
func TestLoadAgentDeclaration_AllKeysExplicit_DefaultsUsedFalse(t *testing.T) {
	dir := writeAgentJSON(t, `{
		"version": 1,
		"position_file": "positions/harness.md",
		"cycle_budget_usd": 300,
		"size_budgets_usd": {"S": {"impl": 30, "review": 25}, "M": {"impl": 50, "review": 30}, "L": {"impl": 100, "review": 40}},
		"max_run_budget_usd": 200,
		"judgment_budget_usd": {"J1": 1, "J2": 5, "J3": 3, "J4": 2, "J5": 5},
		"timeout_sec": {"judgment": 900, "delegate": 14400},
		"max_parallel_runs": 2,
		"rework_limit": 3,
		"failure_limit": 2
	}`)
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = true, want false")
	}
}

// 部分指定: size_budgets_usd.S.impl だけを指定した場合、その値を反映しつつ
// review・M・L は既定値のままで、DefaultsUsed は真になる（部分指定の仮定）。
func TestLoadAgentDeclaration_PartialSizeBudgets_AppliesLeafDefaults(t *testing.T) {
	dir := writeAgentJSON(t, `{"size_budgets_usd": {"S": {"impl": 99}}}`)
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if !decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = false, want true")
	}
	if decl.SizeBudgetsUSD.S.Impl != 99 {
		t.Errorf("SizeBudgetsUSD.S.Impl = %v, want 99", decl.SizeBudgetsUSD.S.Impl)
	}
	if decl.SizeBudgetsUSD.S.Review != 25 {
		t.Errorf("SizeBudgetsUSD.S.Review = %v, want 25 (default)", decl.SizeBudgetsUSD.S.Review)
	}
	if decl.SizeBudgetsUSD.M != (SizeBudgetPair{50, 30}) {
		t.Errorf("SizeBudgetsUSD.M = %+v, want default", decl.SizeBudgetsUSD.M)
	}
}

// --- AC-5〜8: 金額が0以下 ---

func TestLoadAgentDeclaration_CycleBudgetUSDMustBePositive(t *testing.T) {
	for _, v := range []string{"0", "-1", "-0.5"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"cycle_budget_usd": %s}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_MaxRunBudgetUSDMustBePositive(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"max_run_budget_usd": %s}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_JudgmentBudgetUSDMustBePositive(t *testing.T) {
	keys := []string{"J1", "J2", "J3", "J4", "J5"}
	for _, k := range keys {
		for _, v := range []string{"0", "-1"} {
			t.Run(k+"="+v, func(t *testing.T) {
				dir := writeAgentJSON(t, fmt.Sprintf(`{"judgment_budget_usd": {%q: %s}}`, k, v))
				_, err := LoadAgentDeclaration(dir)
				if !errors.Is(err, ErrConfigInvalid) {
					t.Fatalf("error = %v, want ErrConfigInvalid", err)
				}
			})
		}
	}
}

func TestLoadAgentDeclaration_SizeBudgetsUSDMustBePositive(t *testing.T) {
	sizes := []string{"S", "M", "L"}
	fields := []string{"impl", "review"}
	for _, sz := range sizes {
		for _, f := range fields {
			for _, v := range []string{"0", "-1"} {
				t.Run(sz+"."+f+"="+v, func(t *testing.T) {
					dir := writeAgentJSON(t, fmt.Sprintf(`{"size_budgets_usd": {%q: {%q: %s}}}`, sz, f, v))
					_, err := LoadAgentDeclaration(dir)
					if !errors.Is(err, ErrConfigInvalid) {
						t.Fatalf("error = %v, want ErrConfigInvalid", err)
					}
				})
			}
		}
	}
}

// --- AC-9〜13: 整数キーが正の整数でない（0・負・小数） ---

func TestLoadAgentDeclaration_TimeoutJudgmentMustBePositiveInteger(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.5", "1.0"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"timeout_sec": {"judgment": %s}}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_TimeoutDelegateMustBePositiveInteger(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.5"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"timeout_sec": {"delegate": %s}}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_MaxParallelRunsMustBePositiveInteger(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.5"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"max_parallel_runs": %s}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_ReworkLimitMustBePositiveInteger(t *testing.T) {
	for _, v := range []string{"0", "-1", "2.5"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"rework_limit": %s}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_FailureLimitMustBePositiveInteger(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.1"} {
		t.Run(v, func(t *testing.T) {
			dir := writeAgentJSON(t, fmt.Sprintf(`{"failure_limit": %s}`, v))
			_, err := LoadAgentDeclaration(dir)
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- 未知のキー・型違い ---

func TestLoadAgentDeclaration_UnknownTopLevelKey(t *testing.T) {
	dir := writeAgentJSON(t, `{"unknown_field": true}`)
	_, err := LoadAgentDeclaration(dir)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadAgentDeclaration_UnknownNestedKey(t *testing.T) {
	cases := []string{
		`{"size_budgets_usd": {"XL": {"impl": 1, "review": 1}}}`,
		`{"size_budgets_usd": {"S": {"impl": 1, "review": 1, "extra": 1}}}`,
		`{"judgment_budget_usd": {"J6": 1}}`,
		`{"timeout_sec": {"extra": 1}}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeAgentJSON(t, doc)
			if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_TypeMismatch(t *testing.T) {
	cases := []string{
		`{"cycle_budget_usd": "300"}`,
		`{"cycle_budget_usd": true}`,
		`{"position_file": 1}`,
		`{"max_parallel_runs": "2"}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeAgentJSON(t, doc)
			if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadAgentDeclaration_UnparseableJSON(t *testing.T) {
	dir := writeAgentJSON(t, `{ not json `)
	if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- position_file の要求（結線は #84〜#86。ここでは手段だけを確認する） ---

func TestAgentDeclaration_RequirePositionFile_EmptyIsInvalid(t *testing.T) {
	dir := t.TempDir()
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if err := decl.RequirePositionFile(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("RequirePositionFile() error = %v, want ErrConfigInvalid", err)
	}
}

func TestAgentDeclaration_RequirePositionFile_MissingFileIsInvalid(t *testing.T) {
	dir := writeAgentJSON(t, `{"position_file": "positions/does-not-exist.md"}`)
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if err := decl.RequirePositionFile(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("RequirePositionFile() error = %v, want ErrConfigInvalid", err)
	}
}

func TestAgentDeclaration_RequirePositionFile_ExistingFileIsValid(t *testing.T) {
	dir := writeAgentJSON(t, `{"position_file": "positions/harness.md"}`)
	if err := os.MkdirAll(filepath.Join(dir, "positions"), 0o755); err != nil {
		t.Fatalf("mkdir positions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "positions", "harness.md"), []byte("# harness"), 0o644); err != nil {
		t.Fatalf("write positions/harness.md: %v", err)
	}
	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration() error = %v, want nil", err)
	}
	if err := decl.RequirePositionFile(dir); err != nil {
		t.Fatalf("RequirePositionFile() error = %v, want nil", err)
	}
}

// --- self-review 指摘: 明示的な JSON null は「省略」と区別し型違いとして
// 拒否する（encoding/json は非ポインタのスカラー値を null で変えず、
// map/slice は nil のまま無エラーで返すため、対処しないと省略と同じ既定値へ
// 静かにフォールバックしてしまう） ---

func TestLoadAgentDeclaration_WholeFileNull(t *testing.T) {
	dir := writeAgentJSON(t, `null`)
	if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadAgentDeclaration_NullLeafValuesAreRejected(t *testing.T) {
	cases := []string{
		`{"position_file": null}`,
		`{"size_budgets_usd": null}`,
		`{"size_budgets_usd": {"S": null}}`,
		`{"judgment_budget_usd": null}`,
		`{"timeout_sec": null}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeAgentJSON(t, doc)
			if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- fail-closed: 検証の失敗はワークスペースに何も書かない ---

func TestLoadAgentDeclaration_InvalidDeclarationChangesNothingOnDisk(t *testing.T) {
	dir := writeAgentJSON(t, `{"cycle_budget_usd": -1}`)
	before := snapshotDir(t, dir)

	if _, err := LoadAgentDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}

	after := snapshotDir(t, dir)
	if before != after {
		t.Fatalf("workspace changed after a rejected declaration:\nbefore=%s\nafter=%s", before, after)
	}
}
