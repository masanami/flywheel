package core

// このファイルは衝突の予測の口（`harness predict-conflicts`・出力の形
// `harness.conflict-prediction/v1`。親要件チケット #98 §クリティカル設計決定 7・
// M3H9・M3H10・M3P29・M3P36）の呼び出しの記録と、出力の検査・費用の数え方を持つ。
// 口の起動（子プロセス・時間の上限・出力の保存・JSON の正規化）は internal/invoker が持ち、
// core は ConflictPredictor の IF だけを知る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// 出力の issues[].status・pairs[].status の閉集合。
const (
	predictIssuePredicted       = "predicted"
	predictIssueFailed          = "failed"
	predictIssueBudgetExhausted = "budget_exhausted"
	predictPairPredicted        = "predicted"
	predictPairUnknown          = "unknown"
)

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

// PredictedPair は出力の pairs[] の 1 要素。DependencyFirst は dependency.first
// （先に入れるべき側の Issue 番号。null は nil）。
type PredictedPair struct {
	Issues          [2]int
	Status          string
	SharedFiles     []PredictedSharedFile
	DependencyFirst *int
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

// PredictRunInput は Store.runPrediction の入力。
type predictRunInput struct {
	CycleID      string
	Repo         string
	Declaration  ConflictPrediction
	WorkDir      string
	Issues       []int
	MaxBudgetUSD float64
	TimeoutSec   int
	Predictor    ConflictPredictor
}

// predictRunResult は runPrediction の出力。Output は呼び出し全体が成功したときだけ非 nil。
type predictRunResult struct {
	RunID  string
	Result RunResult
	Output *ConflictPredictionOutput
}

// validatePredictionOutput は出力が呼び出し全体の失敗にあたらないかを検査する:
// schema が宣言と一致する・error が null・issues[].status と pairs[].status が閉集合の中。
func validatePredictionOutput(p *ConflictPredictionOutput, schema string) error {
	switch {
	case p.Schema != schema:
		return fmt.Errorf("prediction schema %q differs from the declared %q", p.Schema, schema)
	case p.ErrorSet:
		return errors.New("prediction output reports an error")
	}
	for _, i := range p.Issues {
		switch i.Status {
		case predictIssuePredicted, predictIssueFailed, predictIssueBudgetExhausted:
		default:
			return fmt.Errorf("prediction issue #%d has an unknown status %q", i.Issue, i.Status)
		}
	}
	for _, pr := range p.Pairs {
		switch pr.Status {
		case predictPairPredicted, predictPairUnknown:
		default:
			return fmt.Errorf("prediction pair has an unknown status %q", pr.Status)
		}
	}
	return nil
}

// predictionCost は §予算ガードの衝突の予測の費用の数え方: 出力が JSON として読め、schema が
// 宣言と一致し、cost_usd が 0 以上の数で、unknown_cost_count が 0（または無い）なら
// cost_usd の額（reported）、そうでなければ渡した上限額（unknown）。起動できなかったときは 0。
func predictionCost(result RunResult, p *ConflictPredictionOutput, schema string, maxBudgetMicros int64) (int64, costSource) {
	if result == RunResultLaunchFailed {
		return 0, ""
	}
	if p != nil && p.Schema == schema && p.CostUSD != nil && *p.CostUSD >= 0 &&
		!p.UnknownCostCountInvalid && (p.UnknownCostCount == nil || *p.UnknownCostCount == 0) {
		return usdToMicros(*p.CostUSD), costSourceReported
	}
	return maxBudgetMicros, costSourceUnknown
}

// runPrediction は衝突の予測の口を 1 回呼び、predict の run として記録する。周の上限
// （既消費額＋予約額＋渡す上限額）を超えるなら何も起動・記録せず ErrBudgetExceeded を返す。
// 起動できたかどうか・呼び出しの成否は結果の Result と Output で表す（エラーにしない）。
func (s *Store) runPrediction(ctx context.Context, in predictRunInput) (*predictRunResult, error) {
	cycleInt, ok := parseCycleID(in.CycleID)
	if !ok || in.Predictor == nil || in.Repo == "" || in.MaxBudgetUSD <= 0 {
		return nil, ErrValidation
	}
	host, _ := os.Hostname()
	pid := int64(os.Getpid())
	now := s.currentTime()
	maxMicros := usdToMicros(in.MaxBudgetUSD)

	var runIDInt int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := evalCycleBudgetTx(ctx, tx, cycleInt, maxMicros); err != nil {
			return err
		}
		row, err := insertRun(ctx, tx, 0, insertRunInput{
			CycleID: &cycleInt, Kind: runKindPredict, PID: pid, Host: host, HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: maxMicros, BudgetBucket: budgetBucketPredict, Repo: in.Repo,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("core: malformed run id %q", row.ID)
		}
		runIDInt = n
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	runID := formatRunID(runIDInt)

	launch := PredictLaunchInput{
		Workspace: s.workspace, WorkDir: in.WorkDir, RunDir: runDirPath(s.workspace, runID),
		Command: in.Declaration.Command, Issues: in.Issues, MaxBudgetUSD: in.MaxBudgetUSD, TimeoutSec: in.TimeoutSec,
	}
	var out PredictLaunchOutput
	_, invokeErr := s.invokeFnWithHeartbeat(ctx, runIDInt, func(ctx context.Context) (JudgmentLaunchOutput, error) {
		o, err := in.Predictor.Predict(ctx, launch)
		out = o
		return JudgmentLaunchOutput{}, err
	})

	result, errSummary := out.Result, out.ErrorSummary
	if invokeErr != nil {
		if errSummary == "" {
			errSummary = invokeErr.Error()
		}
		if result == "" {
			result = RunResultLaunchFailed
		}
	}
	switch result {
	case RunResultSucceeded, RunResultLaunchFailed, RunResultTimedOut, RunResultMalformed, RunResultErrored:
	default:
		result = RunResultErrored
	}
	if result == RunResultSucceeded {
		if out.Prediction == nil {
			result = RunResultMalformed
		} else if verr := validatePredictionOutput(out.Prediction, in.Declaration.Schema); verr != nil {
			result, errSummary = RunResultErrored, verr.Error()
		}
	}
	costMicros, source := predictionCost(result, out.Prediction, in.Declaration.Schema, maxMicros)

	recordCtx := context.WithoutCancel(ctx)
	err = s.db.Write(recordCtx, func(tx *sql.Tx) error {
		cur, err := loadRunByID(recordCtx, tx, runIDInt)
		if err != nil {
			return err
		}
		if cur == nil || cur.EndedAt != nil {
			// 起動の最中に回収（interrupted）された run は上書きしない。
			result = RunResultInterrupted
			return nil
		}
		_, err = updateRunEnd(recordCtx, tx, runIDInt, updateRunEndInput{
			EndedAt: s.currentTime(), Result: result, CostUSD: &costMicros, CostSource: source,
			Output: string(out.RawOutput), Error: errSummary,
		})
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	res := &predictRunResult{RunID: runID, Result: result}
	if result == RunResultSucceeded {
		res.Output = out.Prediction
	}
	return res, nil
}
