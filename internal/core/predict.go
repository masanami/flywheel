package core

// このファイルは衝突の予測の口（`harness predict-conflicts`・出力の形
// `harness.conflict-prediction/v1`）の IF と出力の型を持つ。委譲の段は予測の口を呼ばない。
// 口の起動（子プロセス・時間の上限・出力の保存・JSON の正規化）は internal/invoker が持ち、
// core は ConflictPredictor の IF だけを知る。

import "context"

// PredictedIssue は出力の issues[] の 1 要素（読むフィールドだけ）。
type PredictedIssue struct {
	Issue  int
	Status string
}

// PredictedSharedFile は出力の pairs[].shared_files[] の 1 要素。
type PredictedSharedFile struct {
	Path          string
	MergeFriendly bool
	Ignored       bool
}

// PredictedDependEdge は出力の pairs[].dependency.stated[] の 1 要素（Issue の予測が相手の Issue を
// 前提に挙げた）。
type PredictedDependEdge struct {
	Issue     int
	DependsOn int
}

// PredictedPair は出力の pairs[] の 1 要素。DependencyFirst は dependency.first
// （先に入れるべき側の Issue 番号。null は nil）、DependencyStated は dependency.stated[]。
type PredictedPair struct {
	Issues           [2]int
	Status           string
	SharedFiles      []PredictedSharedFile
	DependencyFirst  *int
	DependencyStated []PredictedDependEdge
}

// ConflictPredictionOutput は予測の口の出力を、flywheel が読むフィールドだけに正規化した形
// （invoker が JSON から写す。規則の検査は core が行う）。
type ConflictPredictionOutput struct {
	Schema   string
	Complete bool
	// ErrorSet は出力の error が null でない（存在し、null 以外の値を持つ）こと。
	ErrorSet bool
	HeadSHA  string
	// CostUSD は cost_usd（無い・数でないは nil）。UnknownCostCount は unknown_cost_count
	// （無いは nil）。UnknownCostCountInvalid は unknown_cost_count があるが数でないこと。
	CostUSD                 *float64
	UnknownCostCount        *float64
	UnknownCostCountInvalid bool
	Issues                  []PredictedIssue
	Pairs                   []PredictedPair
}

// PredictLaunchInput は ConflictPredictor へ渡す、予測の口の起動 1 回ぶんの情報。
type PredictLaunchInput struct {
	Workspace string
	// WorkDir は作業ディレクトリ（元のクローン、または idle の作業用クローン）。
	WorkDir string
	// RunDir は出力の保存先（.flywheel/runs/<run の ID>/）。
	RunDir string
	// Command は宣言の command（引数の配列）。起動は command の後ろに
	// `--max-budget-usd <額>` と Issue 番号を別々の要素として足す。
	Command      []string
	Issues       []int
	MaxBudgetUSD float64
	TimeoutSec   int
}

// PredictLaunchOutput は予測の口の起動 1 回の結果。Result は launch_failed | timed_out |
// malformed | errored | succeeded のどれか（errored は終了コードが 0 でない）。
// Prediction は標準出力が JSON として読めたときだけ非 nil（終了コードに関わらず）。
type PredictLaunchOutput struct {
	Result       RunResult
	Prediction   *ConflictPredictionOutput
	RawOutput    []byte
	ErrorSummary string
}

// ConflictPredictor は core が定義する、予測の口の起動の IF（internal/invoker.Launcher が実装する）。
type ConflictPredictor interface {
	Predict(ctx context.Context, in PredictLaunchInput) (PredictLaunchOutput, error)
}
