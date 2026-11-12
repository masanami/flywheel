package core

// このファイルは `form: cli` の接続ツールの委譲の前半（docs/features/m3-invoker-delegation.md
// §委譲の流れ（`form: cli`）。architecture.md §10 の汎用の契約）を持つ:
//
//   - 開始（StartContractDelegation）: 宣言の commands.start を呼んで契約の実行 ID を受け取り、
//     contract_run に保存して、その周の委譲を終える。run の終わりまで待たない。state が running でも失敗にしない。
//   - 状態の確認（PollContractRuns）: 次の周以降に commands.status を呼び、state を running | waiting |
//     succeeded | failed | cancelled に振り分けて記録する。succeeded のときは artifacts を run の成果物へ取り込む。
//
// 再開・中止・判断待ちの保留への写し・予算の上限での中止は作らない（後半）。判断待ち（waiting）は状態と
// 求められた操作を記録して次の周へ回すだけである。プロセスの起動と出力の正規化は ContractClient
// （internal/invoker が実装）が持ち、ここは規則（状態の振り分け・費用の重複排除・記録）だけを持つ。
// 接続ツール固有の名前（コマンド名・ゲート名）は持たず、起動の形は宣言から取る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// ContractState は契約（architecture.md §10）の汎用の状態。
type ContractState string

// 契約の状態の閉集合。
const (
	ContractStateRunning   ContractState = "running"
	ContractStateWaiting   ContractState = "waiting"
	ContractStateSucceeded ContractState = "succeeded"
	ContractStateFailed    ContractState = "failed"
	ContractStateCancelled ContractState = "cancelled"
)

func (s ContractState) valid() bool {
	switch s {
	case ContractStateRunning, ContractStateWaiting, ContractStateSucceeded, ContractStateFailed, ContractStateCancelled:
		return true
	}
	return false
}

// Terminal は、契約の run がこれ以上進まない状態か。
func (s ContractState) Terminal() bool {
	return s == ContractStateSucceeded || s == ContractStateFailed || s == ContractStateCancelled
}

// ContractRequestedAction は契約の requested_action（人または親に求める操作）。
type ContractRequestedAction struct {
	Kind    string // answer | approve | observe
	Decider string // human | parent | any
	Text    string
}

// ContractArtifact は契約の artifacts の 1 要素。Kind は pr | branch | commit | other。
type ContractArtifact struct {
	Kind string
	Ref  string
}

// ContractStatus は契約の標準出力 JSON（契約の版 1）を正規化した形。invoker が JSON から写し、
// 閉集合・必須の検査は core（validateContractStatus）が行う。
type ContractStatus struct {
	ContractVersion int
	RunID           string
	State           ContractState
	Summary         string
	RequestedAction *ContractRequestedAction
	Artifacts       []ContractArtifact
	// CostUSD は孫を含む累計（null・無しは nil）。
	CostUSD *float64
}

// ContractLaunchInput は ContractClient へ渡す、契約のコマンド 1 回ぶんの情報。差し込みは core が
// 埋め済みで、Command はそのまま起動する引数の配列（シェルを介さない）。
type ContractLaunchInput struct {
	Workspace  string
	WorkDir    string
	RunDir     string
	Command    []string
	TimeoutSec int
}

// ContractLaunchOutput はコマンド 1 回の結果。Result は launch_failed | timed_out | malformed | errored |
// succeeded のどれか（終了コードが 0 でなければ errored。0 でも JSON が読めなければ malformed）。
// Status は標準出力が JSON として読めたときだけ非 nil。
type ContractLaunchOutput struct {
	Result       RunResult
	Status       *ContractStatus
	RawOutput    []byte
	ErrorSummary string
}

// ContractClient は core が定義する、契約のコマンドの起動の IF（internal/invoker.Launcher が実装する）。
type ContractClient interface {
	Start(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error)
	Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error)
}

// contractVersion は扱う契約の版（宣言の contract_version と同じ。1 だけ）。
const contractVersion = 1

// validateContractStatus は st が契約の版 1 の形か検査する。wantRunID が空でなければ run_id が一致すること
// も求める。返り値は不備の理由（正しければ空）。
func validateContractStatus(st *ContractStatus, wantRunID string) string {
	switch {
	case st == nil:
		return "no status"
	case st.ContractVersion != contractVersion:
		return fmt.Sprintf("contract_version must be %d, got %d", contractVersion, st.ContractVersion)
	case st.RunID == "":
		return "run_id is empty"
	case wantRunID != "" && st.RunID != wantRunID:
		return fmt.Sprintf("run_id %q does not match %q", st.RunID, wantRunID)
	case !st.State.valid():
		return fmt.Sprintf("unknown state %q", st.State)
	}
	if ra := st.RequestedAction; ra != nil {
		switch ra.Kind {
		case "answer", "approve", "observe":
		default:
			return fmt.Sprintf("unknown requested_action.kind %q", ra.Kind)
		}
		switch ra.Decider {
		case "human", "parent", "any":
		default:
			return fmt.Sprintf("unknown requested_action.decider %q", ra.Decider)
		}
	}
	for _, a := range st.Artifacts {
		switch a.Kind {
		case "pr", "branch", "commit", "other":
		default:
			return fmt.Sprintf("unknown artifacts.kind %q", a.Kind)
		}
		if a.Ref == "" {
			return "artifacts.ref is empty"
		}
	}
	if st.CostUSD != nil && (*st.CostUSD < 0 || math.IsNaN(*st.CostUSD) || math.IsInf(*st.CostUSD, 0)) {
		return "cost_usd is not a non-negative number"
	}
	return ""
}

// ContractStartInput は Store.StartContractDelegation の入力。
type ContractStartInput struct {
	// ChallengeID は委譲する課題（"C-<n>"）。着手中であること。
	ChallengeID string
	// ConnectorID は宣言の接続ツール（form: cli）の ID。
	ConnectorID string
	AgentDecl   *AgentDeclaration
	ConnDecl    *ConnectorsDeclaration
	Client      ContractClient
	// CycleID はこの操作が属する周（BeginCycle が返した ID）。空なら周に属さない。
	CycleID string
	// MaxBudgetUSD は run に記録する上限額（周の上限の評価と、状態を読む run の上限に使う）。
	MaxBudgetUSD float64
	// WorkDir は契約のコマンドの作業ディレクトリ（空ならワークスペース）。
	WorkDir string
}

// ContractPollInput は Store.PollContractRuns の入力。
type ContractPollInput struct {
	// ChallengeID が非 nil ならその課題の契約の run だけを読む。nil なら終わっていないものすべて。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	ConnDecl    *ConnectorsDeclaration
	Client      ContractClient
	CycleID     string
	WorkDir     string
}

// ContractRunReport は start・status の 1 回の結果。
type ContractRunReport struct {
	ChallengeID string
	// RunID はこの呼び出しの flywheel の run（"R-<n>"）。ExternalRunID は契約の実行 ID（run を始められなかったときは空）。
	RunID         string
	ExternalRunID string
	// Result はコマンド 1 回の結果（succeeded でなければ契約の状態は更新されていない）。
	Result  RunResult
	State   ContractState
	Summary string
	// RequestedAction は state が waiting のときの求められた操作（無ければ nil）。保留へは写さない。
	RequestedAction *ContractRequestedAction
	// PRs は state が succeeded になったときに run の成果物へ取り込んだ PR の参照。
	PRs []string
	// Held は、この呼び出しで課題を保留（人間対応待ち）にしたとき真。契約の run が failed・cancelled で
	// 終わった、または start の最中に flywheel の run が回収されたときで、自動では委譲し直さない。
	Held bool
	// CostDeltaUSD は今回この run に記録した費用（累計の増分。何度読んでも同じ累計なら 0）。CostTotalUSD は累計。
	CostDeltaUSD float64
	CostTotalUSD float64
	Error        string
}

// contractRunRow は contract_run の 1 行。
type contractRunRow struct {
	ID            int64
	ChallengeID   int64
	ConnectorID   string
	ExternalRunID string
	State         ContractState
	Summary       string
	CostMicros    int64
	MaxBudget     int64
}

const contractRunSelect = `SELECT id, challenge_id, connector_id, external_run_id, state, summary, cost_usd, max_budget_usd FROM contract_run`

func scanContractRun(row interface{ Scan(...any) error }) (*contractRunRow, error) {
	var r contractRunRow
	var state string
	if err := row.Scan(&r.ID, &r.ChallengeID, &r.ConnectorID, &r.ExternalRunID, &state, &r.Summary, &r.CostMicros, &r.MaxBudget); err != nil {
		return nil, err
	}
	r.State = ContractState(state)
	return &r, nil
}

// fillContractArgs は契約のコマンドの引数の差し込みを埋める。閉集合は invocation と同じ 4 つ
// （{issue_number}・{issue_url}・{external_key}・{challenge_id}）と、status・resume・cancel の {run_id}
// （start が返した契約の実行 ID）。引数の要素の数は変えない。
func fillContractArgs(args []string, challengeID string, sb *sourceBinding, externalRunID string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := fillInvocation(a, challengeID, sb)
		if err != nil {
			return nil, err
		}
		out[i] = strings.ReplaceAll(v, "{run_id}", externalRunID)
	}
	return out, nil
}

func findCLIConnector(d *ConnectorsDeclaration, id string) (*Connector, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: connectors declaration is required", ErrValidation)
	}
	for i := range d.Connectors {
		c := &d.Connectors[i]
		if c.ID != id {
			continue
		}
		if c.Form != ConnectorFormCLI || c.Commands == nil {
			return nil, fmt.Errorf("%w: connector %q is not a cli connector", ErrValidation, id)
		}
		return c, nil
	}
	return nil, fmt.Errorf("%w: unknown connector %q", ErrValidation, id)
}

func (s *Store) contractWorkDir(workDir string) string {
	if workDir != "" {
		return workDir
	}
	return s.workspace
}

// StartContractDelegation は課題 1 件の委譲を契約の開始で始める。実行 ID を contract_run に保存し、
// 契約の run の終わりを待たずに返す（再開・中止はしない）。コマンドが成功（JSON を出力）しなかったときは
// 契約の run を作らず、その結果を run に記録する。
func (s *Store) StartContractDelegation(ctx context.Context, in ContractStartInput) (*ContractRunReport, error) {
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return nil, ErrValidation
	}
	if in.Client == nil || in.AgentDecl == nil || !validBudgetAmount(in.MaxBudgetUSD) {
		return nil, ErrValidation
	}
	connector, err := findCLIConnector(in.ConnDecl, in.ConnectorID)
	if err != nil {
		return nil, err
	}
	var binding *sourceBinding
	var planVersion *int64
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if ch == nil {
			return ErrNotFound
		}
		if ch.Status != StatusInProgress {
			return errDelegationNotEligible
		}
		if binding, err = loadSourceBindingByChallengeID(ctx, tx, cid); err != nil {
			return err
		}
		p, found, err := loadApprovedPlanAnySpec(ctx, tx, cid)
		if err != nil {
			return err
		}
		if found {
			v := int64(p.Version)
			planVersion = &v
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	command, err := fillContractArgs(connector.Commands.Start, in.ChallengeID, binding, "")
	if err != nil {
		return nil, err
	}
	maxMicros := usdToMicros(in.MaxBudgetUSD)
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	var cycleIDInt *int64
	if n, ok := parseCycleID(in.CycleID); ok {
		cycleIDInt = &n
	}
	runIDInt, err := s.beginContractRun(ctx, cid, cycleIDInt, planVersion, sessionID, maxMicros, true)
	if err != nil {
		return nil, err
	}
	call := func(ctx context.Context, li ContractLaunchInput) (ContractLaunchOutput, error) {
		return in.Client.Start(ctx, li)
	}
	return s.finishContractCall(ctx, contractCall{
		ChallengeID: in.ChallengeID, ChallengeIDInt: cid, RunIDInt: runIDInt, Connector: connector.ID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: s.contractWorkDir(in.WorkDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
			Command: command, TimeoutSec: in.AgentDecl.TimeoutSec.Delegate,
		},
		Invoke: call, MaxMicros: maxMicros,
	})
}

// statusRunMaxBudgetMicros は状態の確認（status）の run の評価額（run の記録が正の値を要求する最小）。契約の費用は
// 累計の差分として読むたびに記録するので、status の run が回収されても契約の予算の全額を課さない（二重に数えない）。
const statusRunMaxBudgetMicros int64 = 1

// beginContractRun は契約のコマンド 1 回ぶんの run を記録する。start のときは周の上限を評価し、課題に
// 終わっていない契約の run・run があれば ErrRunInProgress にする。
func (s *Store) beginContractRun(ctx context.Context, cid int64, cycleID, planVersion *int64, sessionID string, maxMicros int64, isStart bool) (int64, error) {
	host, _ := os.Hostname()
	now := s.currentTime()
	var runIDInt int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		cur, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if cur == nil {
			return ErrNotFound
		}
		if isStart {
			if cur.Status != StatusInProgress {
				return errDelegationNotEligible
			}
			// 終わっていない契約の run のほか、保留に写し切れていない回収・失敗・中止も実行中として数える。
			active, err := challengeHasActiveRun(ctx, tx, cid)
			if err != nil {
				return err
			}
			if active {
				return errActiveRunExists
			}
			if cycleID != nil {
				if err := evalCycleBudgetTx(ctx, tx, *cycleID, maxMicros); err != nil {
					return err
				}
			}
		}
		row, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID: cycleID, Kind: runKindContract, ChallengeVersion: int64(cur.Version), PlanVersion: planVersion,
			SessionID: sessionID, PID: int64(os.Getpid()), Host: host, HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: maxMicros, BudgetBucket: budgetBucketImpl,
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
	switch {
	case errors.Is(err, errActiveRunExists):
		return 0, ErrRunInProgress
	}
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	return runIDInt, nil
}

// contractCall は 1 回のコマンドの起動と記録の材料。Existing が非 nil なら状態の確認（既存の契約の run）。
type contractCall struct {
	ChallengeID    string
	ChallengeIDInt int64
	RunIDInt       int64
	Connector      string
	Launch         ContractLaunchInput
	Invoke         func(context.Context, ContractLaunchInput) (ContractLaunchOutput, error)
	MaxMicros      int64
	Existing       *contractRunRow
}

// finishContractCall はコマンドを起動し、結果を run と contract_run へ 1 つのトランザクションで記録する。
func (s *Store) finishContractCall(ctx context.Context, c contractCall) (*ContractRunReport, error) {
	var out ContractLaunchOutput
	_, invokeErr := s.invokeFnWithHeartbeat(ctx, c.RunIDInt, func(ctx context.Context) (JudgmentLaunchOutput, error) {
		o, err := c.Invoke(ctx, c.Launch)
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
	if !result.valid() {
		result = RunResultErrored
	}
	wantID := ""
	if c.Existing != nil {
		wantID = c.Existing.ExternalRunID
	}
	status := out.Status
	if result == RunResultSucceeded {
		if reason := validateContractStatus(status, wantID); reason != "" {
			result = RunResultInvalidOutput
			if errSummary == "" {
				errSummary = "invalid contract output: " + reason
			}
		}
	}
	if result != RunResultSucceeded {
		status = nil
	}

	// 費用の差分は書き込みのトランザクションの中で、その時点の累計を読み直して決める（別のプロセスが先に
	// 同じ契約の run を読んで記録していても、二重に数えない）。
	var newTotal, delta int64
	var reportedPtr *int64
	var costSource CostSource
	costFrom := func(prev int64) {
		newTotal = prev
		reportedPtr, costSource = nil, ""
		if status != nil && status.CostUSD != nil {
			if m := usdToMicros(*status.CostUSD); m > newTotal {
				newTotal = m
			}
			reportedPtr, costSource = &newTotal, CostSourceDelta
		}
		delta = newTotal - prev
	}
	if c.Existing != nil {
		costFrom(c.Existing.CostMicros)
	} else {
		costFrom(0)
	}

	report := &ContractRunReport{
		ChallengeID: c.ChallengeID, RunID: formatRunID(c.RunIDInt), Result: result, Error: errSummary,
	}
	if c.Existing != nil {
		report.ExternalRunID, report.State, report.Summary = c.Existing.ExternalRunID, c.Existing.State, c.Existing.Summary
	}
	if status != nil {
		report.ExternalRunID, report.State, report.Summary = status.RunID, status.State, status.Summary
		report.RequestedAction = status.RequestedAction
	}

	// コマンドを起動した以上、記録は ctx の取り消しに関わらず行う。
	recordCtx := context.WithoutCancel(ctx)
	var reclaimed bool
	err := s.db.Write(recordCtx, func(tx *sql.Tx) error {
		cur, err := loadRunByID(recordCtx, tx, c.RunIDInt)
		if err != nil {
			return err
		}
		if cur == nil || cur.EndedAt != nil {
			// 起動の最中に回収された run は上書きしない。
			reclaimed = true
			return nil
		}
		if c.Existing != nil {
			latest, err := scanContractRun(tx.QueryRowContext(recordCtx, contractRunSelect+` WHERE id = ?`, c.Existing.ID))
			if err != nil {
				return err
			}
			c.Existing = latest
			costFrom(latest.CostMicros)
			if latest.State.Terminal() {
				// 別のプロセスが先に終えた契約の run は更新しない（成果物の二重の取り込み・再び開くことを防ぐ）。
				status = nil
				costFrom(latest.CostMicros)
			}
		}
		now := s.currentTime()
		if _, err := updateRunEnd(recordCtx, tx, c.RunIDInt, updateRunEndInput{
			EndedAt: now, Result: result, CostUSD: &delta, CostSource: costSource, ReportedTotalCostUSD: reportedPtr,
			Output: string(out.RawOutput), Error: errSummary,
		}); err != nil {
			return err
		}
		if status == nil {
			return nil
		}
		return s.recordContractStatus(recordCtx, tx, c, status, newTotal, report)
	})
	if err != nil {
		// 契約の run の記録に失敗しても、flywheel の run を開いたまま残さない。
		_ = s.db.Write(recordCtx, func(tx *sql.Tx) error {
			cur, lerr := loadRunByID(recordCtx, tx, c.RunIDInt)
			if lerr != nil || cur == nil || cur.EndedAt != nil {
				return lerr
			}
			_, uerr := updateRunEnd(recordCtx, tx, c.RunIDInt, updateRunEndInput{
				EndedAt: s.currentTime(), Result: RunResultErrored, Error: "contract record failed: " + err.Error(),
			})
			return uerr
		})
	}
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	report.CostDeltaUSD, report.CostTotalUSD = microsToUSD(delta), microsToUSD(newTotal)
	if status == nil && c.Existing != nil {
		report.State, report.Summary = c.Existing.State, c.Existing.Summary
	}
	if question := contractHoldQuestion(c, status, out.Status, reclaimed); question != "" {
		held, herr := s.holdForContract(ctx, c, question)
		if herr != nil {
			return nil, herr
		}
		report.Held = held
	}
	return report, nil
}

// contractHoldQuestion は、この呼び出しの結果が課題を保留にすべきものなら、オーナーへの問いを返す（無ければ空）。
//   - start の最中に flywheel の run が回収された（reclaimed）: 契約の run は記録されず、追われていない
//     外部の run が残っているかもしれない。
//   - 契約が failed・cancelled を返した（recorded は記録に使った状態。別のプロセスが先に終えていれば nil）。
//     存在しない run の状態を読んでも failed が返ることがあり、状態の置き場の設定違いと本当の失敗を
//     見分けられないため、自動で失敗にはしない。
func contractHoldQuestion(c contractCall, recorded, observed *ContractStatus, reclaimed bool) string {
	if reclaimed {
		if c.Existing != nil {
			return "" // 状態の確認の最中の回収は、契約の run がそのまま残るので次の周で読み直す
		}
		ext := ""
		if observed != nil {
			ext = observed.RunID
		}
		return contractReclaimedQuestion(ext, c.Connector)
	}
	if recorded == nil {
		return ""
	}
	return contractStopQuestion(recorded.State, recorded.RunID, recorded.Summary)
}

// contractStopQuestion は契約が failed・cancelled を返したときの問い（それ以外の state は空）。
func contractStopQuestion(state ContractState, runID, summary string) string {
	switch state {
	case ContractStateFailed:
		return fmt.Sprintf("接続ツールの run（%s）が failed を返した。接続ツールの要約: %s\n"+
			"存在しない run の状態を読んでも failed が返ることがあり、状態の置き場の設定の違いと本当の失敗を見分けられないため、"+
			"自動では失敗にも再委譲にもしない。要約と設定を確かめて回答する。", runID, summary)
	case ContractStateCancelled:
		return fmt.Sprintf("接続ツールの run（%s）が中止された（cancelled）。接続ツールの要約: %s\n"+
			"自動では委譲し直さない。必要なら回答して再開する。", runID, summary)
	}
	return ""
}

// contractReclaimedQuestion は start の最中の回収の問い。externalRunID・connector は分かっていれば渡す（空なら書かない）。
func contractReclaimedQuestion(externalRunID, connector string) string {
	q := "接続ツールの run を始めている最中に、flywheel の run が回収された（プロセスの停止など）。" +
		"接続ツールの側で run が始まっていて、flywheel が追っていない run が残っているかもしれない。"
	if externalRunID != "" {
		q += fmt.Sprintf("接続ツールが返した実行 ID: %s。", externalRunID)
	} else {
		q += "実行 ID は受け取れていない。"
	}
	if connector != "" {
		q += fmt.Sprintf("接続先: %s。", connector)
	}
	return q + "接続ツール側の run が残っていないかを確かめ、残っていれば止めてから回答する。自動では委譲し直さない。"
}

// holdForContract は課題が着手中なら、契約の run の結果に基づいて課題を保留にする。課題が着手中でなくなって
// いれば何もしない（held は偽）。契約の run の記録とは別のトランザクションで行う。
func (s *Store) holdForContract(ctx context.Context, c contractCall, question string) (bool, error) {
	return s.holdReclaimedContractStart(ctx, formatRunID(c.RunIDInt), c.ChallengeID, question)
}

// holdReclaimedContractStart は runID を原因の run として、課題が着手中なら保留にする（着手中でなければ偽）。
func (s *Store) holdReclaimedContractStart(ctx context.Context, runID, challengeID, question string) (bool, error) {
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), runID, challengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			return s.insertHoldWithOccurrence(ctx, tc, question, holdReasonContractStopped, "")
		})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// recordContractStatus は読めた状態を contract_run へ反映する（start なら行を作る）。state が succeeded のときは
// artifacts の pr・branch・commit をこの run の成果物へ取り込む（1 回だけ。終わった契約の run は読まないため）。
func (s *Store) recordContractStatus(ctx context.Context, tx *sql.Tx, c contractCall, st *ContractStatus, totalMicros int64, report *ContractRunReport) error {
	now := formatTimestamp(s.currentTime())
	var rk, rd, rt any
	if ra := st.RequestedAction; ra != nil {
		rk, rd, rt = ra.Kind, ra.Decider, ra.Text
	}
	var endedAt any
	if st.State.Terminal() {
		endedAt = now
	}
	if c.Existing == nil {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary,
				requested_kind, requested_decider, requested_text, cost_usd, max_budget_usd, started_at, observed_at, ended_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ChallengeIDInt, c.Connector, st.RunID, c.RunIDInt, string(st.State), st.Summary, rk, rd, rt,
			totalMicros, c.MaxMicros, now, now, endedAt)
		if err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE contract_run SET state = ?, summary = ?, requested_kind = ?, requested_decider = ?, requested_text = ?,
				cost_usd = ?, observed_at = ?, ended_at = ? WHERE id = ?`,
			string(st.State), st.Summary, rk, rd, rt, totalMicros, now, endedAt, c.Existing.ID); err != nil {
			return err
		}
	}
	if st.State != ContractStateSucceeded {
		return nil
	}
	verifiedAt := s.currentTime()
	for _, a := range st.Artifacts {
		var kind artifactKind
		switch a.Kind {
		case "pr":
			kind = artifactKindPR
		case "branch":
			kind = artifactKindBranch
		case "commit":
			kind = artifactKindCommit
		default:
			continue
		}
		if _, err := insertRunArtifact(ctx, tx, c.RunIDInt, insertRunArtifactInput{Kind: kind, Ref: a.Ref, VerifiedAt: &verifiedAt}); err != nil {
			return err
		}
		if kind == artifactKindPR {
			report.PRs = append(report.PRs, a.Ref)
		}
	}
	return nil
}

// PollContractRuns は終わっていない契約の run（contract_run）の状態を、宣言の commands.status で読んで記録する。
// 読むたびに run を 1 つ記録し、費用は契約の累計との差分だけを記録する（何度読んでも二重に数えない）。
// 結果は contract_run の古い順。1 件の読みが失敗しても（結果が succeeded でなくても）残りを読む。
func (s *Store) PollContractRuns(ctx context.Context, in ContractPollInput) ([]ContractRunReport, error) {
	if in.Client == nil || in.AgentDecl == nil || in.ConnDecl == nil {
		return nil, ErrValidation
	}
	if _, err := s.HoldUnrecordedContractStops(ctx); err != nil {
		return nil, err
	}
	var only *int64
	if in.ChallengeID != nil {
		n, ok := parseChallengeID(*in.ChallengeID)
		if !ok {
			return nil, ErrValidation
		}
		only = &n
	}
	var rows []*contractRunRow
	bindings := map[int64]*sourceBinding{}
	planVersions := map[int64]*int64{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		q := contractRunSelect + ` WHERE ended_at IS NULL`
		args := []any{}
		if only != nil {
			q += ` AND challenge_id = ?`
			args = append(args, *only)
		}
		rs, err := tx.QueryContext(ctx, q+` ORDER BY id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			r, err := scanContractRun(rs)
			if err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		for _, r := range rows {
			if bindings[r.ChallengeID], err = loadSourceBindingByChallengeID(ctx, tx, r.ChallengeID); err != nil {
				return err
			}
			if p, found, err := loadApprovedPlanAnySpec(ctx, tx, r.ChallengeID); err != nil {
				return err
			} else if found {
				v := int64(p.Version)
				planVersions[r.ChallengeID] = &v
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	var cycleIDInt *int64
	if n, ok := parseCycleID(in.CycleID); ok {
		cycleIDInt = &n
	}
	var reports []ContractRunReport
	var firstErr error
	for _, row := range rows {
		connector, err := findCLIConnector(in.ConnDecl, row.ConnectorID)
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		challengeID := formatChallengeID(row.ChallengeID)
		command, err := fillContractArgs(connector.Commands.Status, challengeID, bindings[row.ChallengeID], row.ExternalRunID)
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		sessionID, err := newLowercaseUUIDv4()
		if err != nil {
			firstErr = firstErrOr(firstErr, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err))
			continue
		}

		runIDInt, err := s.beginContractRun(ctx, row.ChallengeID, cycleIDInt, planVersions[row.ChallengeID], sessionID, statusRunMaxBudgetMicros, false)
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		rep, err := s.finishContractCall(ctx, contractCall{
			ChallengeID: challengeID, ChallengeIDInt: row.ChallengeID, RunIDInt: runIDInt, Connector: row.ConnectorID,
			Launch: ContractLaunchInput{
				Workspace: s.workspace, WorkDir: s.contractWorkDir(in.WorkDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
				Command: command, TimeoutSec: in.AgentDecl.TimeoutSec.Delegate,
			},
			Invoke: func(ctx context.Context, li ContractLaunchInput) (ContractLaunchOutput, error) {
				return in.Client.Status(ctx, li)
			},
			MaxMicros: row.MaxBudget, Existing: row,
		})
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		reports = append(reports, *rep)
	}
	return reports, firstErr
}

// reclaimedContractStartWhere は、回収された（interrupted）契約の start の run で、まだ保留に写していないものを
// 選ぶ条件（run を r、課題を c と別名にした副問い合わせの本体）。start の run は、契約の run（contract_run）を
// 作る前に回収されると contract_run を持たない。status の run との区別は「その課題の、先に始まった契約の run が
// その run の開始時点で終わっていなかった」ことで行う。課題が着手中でなくなっていれば（保留・取り下げ・完了）選ばない。
const reclaimedContractStartWhere = `r.kind = 'contract' AND r.result = 'interrupted' AND c.status = 'in_progress'
	AND NOT EXISTS (SELECT 1 FROM contract_run k WHERE k.start_run_id = r.id)
	AND NOT EXISTS (SELECT 1 FROM contract_run k WHERE k.challenge_id = r.challenge_id AND k.start_run_id < r.id
		AND (k.ended_at IS NULL OR k.ended_at >= r.started_at))
	AND NOT EXISTS (SELECT 1 FROM hold h WHERE h.run_id = r.id)`

// unheldContractStopWhere は、契約の run（k）が failed・cancelled で終わったのに、その後に保留を作れていない
// （記録と保留が別のトランザクションなので、間でプロセスが止まると起こる）課題を選ぶ条件。
const unheldContractStopWhere = `k.state IN ('failed', 'cancelled') AND c.status = 'in_progress'
	AND NOT EXISTS (SELECT 1 FROM hold h WHERE h.challenge_id = k.challenge_id AND h.raised_at >= k.ended_at)`

// HoldUnrecordedContractStops は、保留にし損ねた課題を保留にする。次の 2 つ（どちらも課題が着手中のときだけ）。
//   - 契約の start の最中に flywheel の run が回収された（呼んだプロセスが止まって別のプロセスが回収した場合など）のに、
//     まだ保留にしていない。外部の run が残っているかもしれないことを問いに書く。
//   - 契約の run が failed・cancelled で終わったのに、保留を作れていない。
//
// 回収は run を interrupted で閉じるだけなので、PollContractRuns の先頭と、周から呼ぶ側が呼ぶ。
// 保留にした課題の数を返す。
func (s *Store) HoldUnrecordedContractStops(ctx context.Context) (int, error) {
	type target struct {
		runID, challengeID int64
		question           string
	}
	var targets []target
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.id, r.challenge_id FROM run r JOIN challenge c ON c.id = r.challenge_id
			WHERE `+reclaimedContractStartWhere+` ORDER BY r.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			t := target{question: contractReclaimedQuestion("", "")}
			if err := rows.Scan(&t.runID, &t.challengeID); err != nil {
				_ = rows.Close()
				return err
			}
			targets = append(targets, t)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		rows, err = tx.QueryContext(ctx, `SELECT k.start_run_id, k.challenge_id, k.state, k.external_run_id, k.summary
			FROM contract_run k JOIN challenge c ON c.id = k.challenge_id WHERE `+unheldContractStopWhere+` ORDER BY k.id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t target
			var state, ext, summary string
			if err := rows.Scan(&t.runID, &t.challengeID, &state, &ext, &summary); err != nil {
				return err
			}
			t.question = contractStopQuestion(ContractState(state), ext, summary)
			targets = append(targets, t)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	held := 0
	for _, t := range targets {
		ok, err := s.holdReclaimedContractStart(ctx, formatRunID(t.runID), formatChallengeID(t.challengeID), t.question)
		if err != nil {
			return held, err
		}
		if ok {
			held++
		}
	}
	return held, nil
}

func firstErrOr(cur, next error) error {
	if cur != nil {
		return cur
	}
	return next
}
