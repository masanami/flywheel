package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// --- .flywheel/agent.json（宣言）§IF / API ---
//
// docs/features/m3-invoker-delegation.md §IF / API「.flywheel/agent.json」に
// 定める形。読み込みと検証（LoadAgentDeclaration）・既定値の適用は本チケット
// （#79）の範囲。position_file を要求する J1・J2 の入口（classify・plan・
// cycle）への結線は #84〜#86 の範囲（このファイルは RequirePositionFile で
// 手段だけを用意する）。

// agentFileName はワークスペース直下の宣言ファイル名。
const agentFileName = "agent.json"

// agentFilePath は base の下の宣言ファイルの絶対パスを返す。
func agentFilePath(base string) string {
	return filepath.Join(base, flywheelDirName, agentFileName)
}

// 既定値（docs/features/m3-invoker-delegation.md §IF / API の例の値）。
// position_file だけは既定を持たない（AgentDeclaration.PositionFile の
// 零値 "" が「既定なし・未設定」を表す）。
const (
	defaultCycleBudgetUSD     = 300.0
	defaultMaxRunBudgetUSD    = 200.0
	defaultTimeoutJudgmentSec = 900
	defaultTimeoutDelegateSec = 14400
	defaultMaxParallelRuns    = 2
	defaultReworkLimit        = 3
	defaultFailureLimit       = 2

	// defaultConflictPredictionBudgetUSD は conflict_prediction_budget_usd の既定
	// （衝突の予測 1 件あたりの上限額、USD）。
	defaultConflictPredictionBudgetUSD = 1.0
)

var defaultSizeBudgetsUSD = SizeBudgetsUSD{
	S: SizeBudgetPair{Impl: 30, Review: 25},
	M: SizeBudgetPair{Impl: 50, Review: 30},
	L: SizeBudgetPair{Impl: 100, Review: 40},
}

var defaultJudgmentBudgetUSD = JudgmentBudgetUSD{J1: 1, J2: 5, J3: 3, J4: 2, J5: 5}

// SizeBudgetPair はサイズ1段（S・M・L のいずれか）の実装・レビューの予算(USD)。
type SizeBudgetPair struct {
	Impl   float64
	Review float64
}

// SizeBudgetsUSD は size_budgets_usd（S・M・L の3段）。
type SizeBudgetsUSD struct {
	S SizeBudgetPair
	M SizeBudgetPair
	L SizeBudgetPair
}

// JudgmentBudgetUSD は judgment_budget_usd（判断点 J1〜J5 ごとの上限額）。
type JudgmentBudgetUSD struct {
	J1, J2, J3, J4, J5 float64
}

// AgentTimeoutSec は timeout_sec（判断の呼び出し・委譲の時間の上限、秒）。
type AgentTimeoutSec struct {
	Judgment int
	Delegate int
}

// AgentDeclaration は .flywheel/agent.json の中身（LoadAgentDeclaration の
// 戻り値）。ファイルが無い場合もこの型の値（全既定値）を返す
// （docs/features/m3-invoker-delegation.md「不在を『上限なし』に縮退させない」）。
type AgentDeclaration struct {
	Version           int
	PositionFile      string // "" は「省略・未設定」（既定を持たない）
	CycleBudgetUSD    float64
	SizeBudgetsUSD    SizeBudgetsUSD
	MaxRunBudgetUSD   float64
	JudgmentBudgetUSD JudgmentBudgetUSD
	TimeoutSec        AgentTimeoutSec
	MaxParallelRuns   int
	ReworkLimit       int
	FailureLimit      int

	// ConflictPredictionBudgetUSD は conflict_prediction_budget_usd
	// （衝突の予測 1 件あたりの上限額、USD）。
	ConflictPredictionBudgetUSD float64

	// DefaultsUsed は、agent.json 自体が無かった、または position_file を除く
	// いずれかのキー（ネストした葉の値を含む）が省略され既定値を適用した
	// ことを表す。呼び出し側（#86 の cycle 等）が `config_defaults_used` に
	// "agent.json" を含めるかどうかの判定に使う。
	DefaultsUsed bool
}

// defaultAgentDeclaration は agent.json が無い場合に使う、全既定値の
// AgentDeclaration を返す（PositionFile は既定を持たないため空文字列）。
func defaultAgentDeclaration() *AgentDeclaration {
	return &AgentDeclaration{
		Version:           1,
		CycleBudgetUSD:    defaultCycleBudgetUSD,
		SizeBudgetsUSD:    defaultSizeBudgetsUSD,
		MaxRunBudgetUSD:   defaultMaxRunBudgetUSD,
		JudgmentBudgetUSD: defaultJudgmentBudgetUSD,
		TimeoutSec:        AgentTimeoutSec{Judgment: defaultTimeoutJudgmentSec, Delegate: defaultTimeoutDelegateSec},
		MaxParallelRuns:   defaultMaxParallelRuns,
		ReworkLimit:       defaultReworkLimit,
		FailureLimit:      defaultFailureLimit,

		ConflictPredictionBudgetUSD: defaultConflictPredictionBudgetUSD,
		DefaultsUsed:                true,
	}
}

// LoadAgentDeclaration は base（ワークスペースの基点ディレクトリ）の下の
// .flywheel/agent.json を読み、検証する。ファイルが無ければエラーにせず、
// 全既定値の *AgentDeclaration（DefaultsUsed: true）を返す（sources.json とは
// 異なり、agent.json の不在は config_not_found ではない。§宣言「agent.json が
// 無い、またはキーを省略したときは…既定値として扱い」）。
//
// JSON として解釈できない・未知のキー（最上位・size_budgets_usd・
// judgment_budget_usd・timeout_sec のいずれの階層も）・型違い・金額が 0 以下・
// 整数であるべき値が整数でない場合はすべて ErrConfigInvalid（fail-closed。
// 何も起動・変更しない。読み込みは os.ReadFile の読み取りだけで、失敗時も
// ワークスペースには何も書かない）。
func LoadAgentDeclaration(base string) (*AgentDeclaration, error) {
	path := agentFilePath(base)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return defaultAgentDeclaration(), nil
		}
		return nil, fmt.Errorf("%w: read %s: %w", ErrConfigInvalid, path, err)
	}

	decl, err := parseAgentDeclaration(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s: %w", ErrConfigInvalid, path, err)
	}
	if err := decl.validate(); err != nil {
		return nil, err
	}
	return decl, nil
}

// JudgmentBudgetFor は判断点 j（J1〜J5）の1回の呼び出しの上限額
// （judgment_budget_usd.<j>）を返す（#83・§予算ガード「判断の呼び出しの
// --max-budget-usd は、judgment_budget_usd の値」）。呼び出し元
// （#84〜#86の判断点の入口）は、この値を RunJudgmentInput.MaxBudgetUSD に
// 渡す。j が J1〜J5 の外なら 0 を返す【仮定】（呼び出し元はJ1〜J5のいずれか
// しか渡さない。core.JudgmentPoint.valid() で事前に検査されている前提）。
func (d *AgentDeclaration) JudgmentBudgetFor(j JudgmentPoint) float64 {
	switch j {
	case JudgmentJ1:
		return d.JudgmentBudgetUSD.J1
	case JudgmentJ2:
		return d.JudgmentBudgetUSD.J2
	case JudgmentJ3:
		return d.JudgmentBudgetUSD.J3
	case JudgmentJ4:
		return d.JudgmentBudgetUSD.J4
	case JudgmentJ5:
		return d.JudgmentBudgetUSD.J5
	default:
		return 0
	}
}

// SizeBudgetFor はサイズ size の予算の既定（size_budgets_usd.<size>。#85・
// §J2「実装枠・レビュー対応枠の額が出力に無ければ、サイズとサイズごとの予算の
// 既定から決める」）を返す。size が S・M・L の外なら ok=false。
func (d *AgentDeclaration) SizeBudgetFor(size JudgmentSize) (SizeBudgetPair, bool) {
	switch size {
	case JudgmentSizeS:
		return d.SizeBudgetsUSD.S, true
	case JudgmentSizeM:
		return d.SizeBudgetsUSD.M, true
	case JudgmentSizeL:
		return d.SizeBudgetsUSD.L, true
	default:
		return SizeBudgetPair{}, false
	}
}

// RequirePositionFile は d.PositionFile が設定されており、base からの相対
// パスとして存在するファイルを指すことを確認する。J1・J2 の入口
// （#84〜#86。ここでは結線しない）がこれを呼ぶことを想定する
// （docs/features/m3-invoker-delegation.md「position_file が無い…または
// 指すファイルが無いときは、J1・J2 を起動せずに config_invalid で終わる」）。
func (d *AgentDeclaration) RequirePositionFile(base string) error {
	if d.PositionFile == "" {
		return fmt.Errorf("%w: position_file is required to run J1/J2", ErrConfigInvalid)
	}
	p := filepath.Join(base, d.PositionFile)
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("%w: position_file %q does not exist: %w", ErrConfigInvalid, d.PositionFile, err)
	}
	return nil
}

var agentTopLevelKeys = map[string]bool{
	"version": true, "position_file": true, "cycle_budget_usd": true,
	"size_budgets_usd": true, "max_run_budget_usd": true, "judgment_budget_usd": true,
	"timeout_sec": true, "max_parallel_runs": true, "rework_limit": true, "failure_limit": true,
	"conflict_prediction_budget_usd": true,
}

var sizeBudgetsSizeKeys = map[string]bool{"S": true, "M": true, "L": true}
var sizeBudgetPairKeys = map[string]bool{"impl": true, "review": true}
var judgmentBudgetKeys = map[string]bool{"J1": true, "J2": true, "J3": true, "J4": true, "J5": true}
var agentTimeoutSecKeys = map[string]bool{"judgment": true, "delegate": true}

// parseAgentDeclaration は data を AgentDeclaration へ変換する。省略された
// キー（position_file を除く。ネストした葉の値を含む）には既定値を入れ、
// DefaultsUsed をその旨立てる。
func parseAgentDeclaration(data []byte) (*AgentDeclaration, error) {
	raw, err := decodeStrictJSONObject(data)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownKeys(raw, agentTopLevelKeys, "top-level"); err != nil {
		return nil, err
	}

	decl := &AgentDeclaration{}
	usedDefault := false

	if v, ok := raw["version"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return nil, fmt.Errorf("version: %w", err)
		}
		decl.Version = i
	} else {
		decl.Version = 1
		usedDefault = true
	}

	if v, ok := raw["position_file"]; ok {
		// self-review 指摘: json.Unmarshal は非ポインタの string を null で
		// 変えない（"" のまま）ため、明示的な null を拒否せずに通すと
		// 「省略」と区別できず、型違いを静かに素通りさせてしまう。
		if isJSONNull(v) {
			return nil, fmt.Errorf("position_file: must not be null")
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, fmt.Errorf("position_file: %w", err)
		}
		decl.PositionFile = s
	}
	// position_file を省略した場合は既定を持たない（usedDefault は立てない）。

	if v, ok := raw["cycle_budget_usd"]; ok {
		f, err := decodeUSDAmount(v)
		if err != nil {
			return nil, fmt.Errorf("cycle_budget_usd: %w", err)
		}
		decl.CycleBudgetUSD = f
	} else {
		decl.CycleBudgetUSD = defaultCycleBudgetUSD
		usedDefault = true
	}

	if v, ok := raw["max_run_budget_usd"]; ok {
		f, err := decodeUSDAmount(v)
		if err != nil {
			return nil, fmt.Errorf("max_run_budget_usd: %w", err)
		}
		decl.MaxRunBudgetUSD = f
	} else {
		decl.MaxRunBudgetUSD = defaultMaxRunBudgetUSD
		usedDefault = true
	}

	if v, ok := raw["max_parallel_runs"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return nil, fmt.Errorf("max_parallel_runs: %w", err)
		}
		decl.MaxParallelRuns = i
	} else {
		decl.MaxParallelRuns = defaultMaxParallelRuns
		usedDefault = true
	}

	if v, ok := raw["rework_limit"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return nil, fmt.Errorf("rework_limit: %w", err)
		}
		decl.ReworkLimit = i
	} else {
		decl.ReworkLimit = defaultReworkLimit
		usedDefault = true
	}

	if v, ok := raw["failure_limit"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return nil, fmt.Errorf("failure_limit: %w", err)
		}
		decl.FailureLimit = i
	} else {
		decl.FailureLimit = defaultFailureLimit
		usedDefault = true
	}

	if v, ok := raw["conflict_prediction_budget_usd"]; ok {
		f, err := decodeUSDAmount(v)
		if err != nil {
			return nil, fmt.Errorf("conflict_prediction_budget_usd: %w", err)
		}
		decl.ConflictPredictionBudgetUSD = f
	} else {
		decl.ConflictPredictionBudgetUSD = defaultConflictPredictionBudgetUSD
		usedDefault = true
	}

	if v, ok := raw["size_budgets_usd"]; ok {
		sb, used, err := parseSizeBudgets(v)
		if err != nil {
			return nil, fmt.Errorf("size_budgets_usd: %w", err)
		}
		decl.SizeBudgetsUSD = sb
		if used {
			usedDefault = true
		}
	} else {
		decl.SizeBudgetsUSD = defaultSizeBudgetsUSD
		usedDefault = true
	}

	if v, ok := raw["judgment_budget_usd"]; ok {
		jb, used, err := parseJudgmentBudgets(v)
		if err != nil {
			return nil, fmt.Errorf("judgment_budget_usd: %w", err)
		}
		decl.JudgmentBudgetUSD = jb
		if used {
			usedDefault = true
		}
	} else {
		decl.JudgmentBudgetUSD = defaultJudgmentBudgetUSD
		usedDefault = true
	}

	if v, ok := raw["timeout_sec"]; ok {
		ts, used, err := parseAgentTimeoutSec(v)
		if err != nil {
			return nil, fmt.Errorf("timeout_sec: %w", err)
		}
		decl.TimeoutSec = ts
		if used {
			usedDefault = true
		}
	} else {
		decl.TimeoutSec = AgentTimeoutSec{Judgment: defaultTimeoutJudgmentSec, Delegate: defaultTimeoutDelegateSec}
		usedDefault = true
	}

	decl.DefaultsUsed = usedDefault
	return decl, nil
}

// decodeIntLiteral は raw を「整数のリテラル」として解釈する。小数点・指数表記
// （"1.0"・"1.5"・"1e3" など）は、整数キーの表記として認めない仮定
// （feature-implementer #79 の仮定。json.Number の文字列表現に '.'・'e'・'E' が
// 一切含まれないことを求める。返却の「置いた仮定」に明記する）。
func decodeIntLiteral(raw json.RawMessage) (int, error) {
	n, err := decodeJSONNumber(raw)
	if err != nil {
		return 0, err
	}
	s := string(n)
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("must be an integer literal without a decimal point or exponent, got %s", s)
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be an integer, got %s", s)
	}
	return int(i), nil
}

// decodeUSDAmount は raw を金額（USD）として解釈する。小数を認める仮定
// （feature-implementer #79 の仮定。金額は USD なので小数が自然）。
func decodeUSDAmount(raw json.RawMessage) (float64, error) {
	n, err := decodeJSONNumber(raw)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return 0, fmt.Errorf("must be a number, got %s", n)
	}
	return f, nil
}

// parseSizeBudgets は size_budgets_usd の値（S・M・L の3段）を解釈する。
// S・M・L のいずれか、またはその中の impl・review のいずれかが省略された
// 場合は、その葉だけに既定値を入れ、used を立てる（部分指定を許す仮定。
// 返却の「置いた仮定」に明記する）。
func parseSizeBudgets(raw json.RawMessage) (SizeBudgetsUSD, bool, error) {
	// self-review 指摘: json.Unmarshal(null, &m) は m=nil を無エラーで返す
	// ため、"size_budgets_usd": null が「省略」と区別できず全既定値へ静かに
	// フォールバックしてしまう。明示的な null は型違いとして拒否する。
	if isJSONNull(raw) {
		return SizeBudgetsUSD{}, false, fmt.Errorf("must not be null")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return SizeBudgetsUSD{}, false, err
	}
	if err := rejectUnknownKeys(m, sizeBudgetsSizeKeys, "size_budgets_usd"); err != nil {
		return SizeBudgetsUSD{}, false, err
	}

	result := SizeBudgetsUSD{}
	used := false

	sizes := []struct {
		key    string
		def    SizeBudgetPair
		target *SizeBudgetPair
	}{
		{"S", defaultSizeBudgetsUSD.S, &result.S},
		{"M", defaultSizeBudgetsUSD.M, &result.M},
		{"L", defaultSizeBudgetsUSD.L, &result.L},
	}
	for _, sz := range sizes {
		v, ok := m[sz.key]
		if !ok {
			*sz.target = sz.def
			used = true
			continue
		}
		pair, pairUsed, err := parseSizeBudgetPair(v, sz.def)
		if err != nil {
			return SizeBudgetsUSD{}, false, fmt.Errorf("%s: %w", sz.key, err)
		}
		*sz.target = pair
		if pairUsed {
			used = true
		}
	}
	return result, used, nil
}

func parseSizeBudgetPair(raw json.RawMessage, def SizeBudgetPair) (SizeBudgetPair, bool, error) {
	if isJSONNull(raw) {
		return SizeBudgetPair{}, false, fmt.Errorf("must not be null")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return SizeBudgetPair{}, false, err
	}
	if err := rejectUnknownKeys(m, sizeBudgetPairKeys, "size pair"); err != nil {
		return SizeBudgetPair{}, false, err
	}

	pair := def
	used := false
	if v, ok := m["impl"]; ok {
		f, err := decodeUSDAmount(v)
		if err != nil {
			return SizeBudgetPair{}, false, fmt.Errorf("impl: %w", err)
		}
		pair.Impl = f
	} else {
		used = true
	}
	if v, ok := m["review"]; ok {
		f, err := decodeUSDAmount(v)
		if err != nil {
			return SizeBudgetPair{}, false, fmt.Errorf("review: %w", err)
		}
		pair.Review = f
	} else {
		used = true
	}
	return pair, used, nil
}

func parseJudgmentBudgets(raw json.RawMessage) (JudgmentBudgetUSD, bool, error) {
	if isJSONNull(raw) {
		return JudgmentBudgetUSD{}, false, fmt.Errorf("must not be null")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return JudgmentBudgetUSD{}, false, err
	}
	if err := rejectUnknownKeys(m, judgmentBudgetKeys, "judgment_budget_usd"); err != nil {
		return JudgmentBudgetUSD{}, false, err
	}

	result := defaultJudgmentBudgetUSD
	used := false
	fields := []struct {
		key    string
		target *float64
	}{
		{"J1", &result.J1}, {"J2", &result.J2}, {"J3", &result.J3}, {"J4", &result.J4}, {"J5", &result.J5},
	}
	for _, f := range fields {
		v, ok := m[f.key]
		if !ok {
			used = true
			continue
		}
		val, err := decodeUSDAmount(v)
		if err != nil {
			return JudgmentBudgetUSD{}, false, fmt.Errorf("%s: %w", f.key, err)
		}
		*f.target = val
	}
	return result, used, nil
}

func parseAgentTimeoutSec(raw json.RawMessage) (AgentTimeoutSec, bool, error) {
	if isJSONNull(raw) {
		return AgentTimeoutSec{}, false, fmt.Errorf("must not be null")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return AgentTimeoutSec{}, false, err
	}
	if err := rejectUnknownKeys(m, agentTimeoutSecKeys, "timeout_sec"); err != nil {
		return AgentTimeoutSec{}, false, err
	}

	result := AgentTimeoutSec{Judgment: defaultTimeoutJudgmentSec, Delegate: defaultTimeoutDelegateSec}
	used := false
	if v, ok := m["judgment"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return AgentTimeoutSec{}, false, fmt.Errorf("judgment: %w", err)
		}
		result.Judgment = i
	} else {
		used = true
	}
	if v, ok := m["delegate"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return AgentTimeoutSec{}, false, fmt.Errorf("delegate: %w", err)
		}
		result.Delegate = i
	} else {
		used = true
	}
	return result, used, nil
}

// validate は d が §IF / API・§受入基準「宣言」の規則を満たすかを検査する。
// 最初に見つけた違反を ErrConfigInvalid として返す（fail-closed。sources.go の
// validate と同じ方針）。
func (d *AgentDeclaration) validate() error {
	if d.Version != 1 {
		return fmt.Errorf("%w: version must be 1, got %d", ErrConfigInvalid, d.Version)
	}
	if d.CycleBudgetUSD <= 0 {
		return fmt.Errorf("%w: cycle_budget_usd must be > 0, got %v", ErrConfigInvalid, d.CycleBudgetUSD)
	}
	if d.MaxRunBudgetUSD <= 0 {
		return fmt.Errorf("%w: max_run_budget_usd must be > 0, got %v", ErrConfigInvalid, d.MaxRunBudgetUSD)
	}

	if d.ConflictPredictionBudgetUSD <= 0 {
		return fmt.Errorf("%w: conflict_prediction_budget_usd must be > 0, got %v", ErrConfigInvalid, d.ConflictPredictionBudgetUSD)
	}

	sizePairs := []struct {
		name string
		pair SizeBudgetPair
	}{
		{"S", d.SizeBudgetsUSD.S}, {"M", d.SizeBudgetsUSD.M}, {"L", d.SizeBudgetsUSD.L},
	}
	for _, sp := range sizePairs {
		if sp.pair.Impl <= 0 {
			return fmt.Errorf("%w: size_budgets_usd.%s.impl must be > 0, got %v", ErrConfigInvalid, sp.name, sp.pair.Impl)
		}
		if sp.pair.Review <= 0 {
			return fmt.Errorf("%w: size_budgets_usd.%s.review must be > 0, got %v", ErrConfigInvalid, sp.name, sp.pair.Review)
		}
	}

	judgments := []struct {
		name string
		val  float64
	}{
		{"J1", d.JudgmentBudgetUSD.J1}, {"J2", d.JudgmentBudgetUSD.J2}, {"J3", d.JudgmentBudgetUSD.J3},
		{"J4", d.JudgmentBudgetUSD.J4}, {"J5", d.JudgmentBudgetUSD.J5},
	}
	for _, j := range judgments {
		if j.val <= 0 {
			return fmt.Errorf("%w: judgment_budget_usd.%s must be > 0, got %v", ErrConfigInvalid, j.name, j.val)
		}
	}

	if d.TimeoutSec.Judgment <= 0 {
		return fmt.Errorf("%w: timeout_sec.judgment must be a positive integer, got %d", ErrConfigInvalid, d.TimeoutSec.Judgment)
	}
	if d.TimeoutSec.Delegate <= 0 {
		return fmt.Errorf("%w: timeout_sec.delegate must be a positive integer, got %d", ErrConfigInvalid, d.TimeoutSec.Delegate)
	}
	if d.MaxParallelRuns <= 0 {
		return fmt.Errorf("%w: max_parallel_runs must be a positive integer, got %d", ErrConfigInvalid, d.MaxParallelRuns)
	}
	if d.ReworkLimit <= 0 {
		return fmt.Errorf("%w: rework_limit must be a positive integer, got %d", ErrConfigInvalid, d.ReworkLimit)
	}
	if d.FailureLimit <= 0 {
		return fmt.Errorf("%w: failure_limit must be a positive integer, got %d", ErrConfigInvalid, d.FailureLimit)
	}

	return nil
}
