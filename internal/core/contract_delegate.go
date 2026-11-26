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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
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

// contractStateValues は ContractState の閉集合（valid と ContractStateValues の正本）。
var contractStateValues = []ContractState{ContractStateRunning, ContractStateWaiting, ContractStateSucceeded, ContractStateFailed, ContractStateCancelled}

func (s ContractState) valid() bool {
	for _, v := range contractStateValues {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal は、契約の run がこれ以上進まない状態か。
func (s ContractState) Terminal() bool {
	return s == ContractStateSucceeded || s == ContractStateFailed || s == ContractStateCancelled
}

// ContractRequestedAction は契約の requested_action（人または親に求める操作）。
type ContractRequestedAction struct {
	Kind    string // answer | approve | observe | review
	Decider string // human | parent | any
	Text    string
	// Options は求められた操作が示す選択肢（無ければ空）。
	Options []string
	// ResolvableBy は求められた操作を誰がどう解くか（任意。無ければ空）。answer は答えを resume で返せば解ける、
	// terminal は接続ツールの外の端末で人が解く必要があり resume では解けない。閉集合の外の値はそのまま持ち、
	// 扱いは resolvableByTerminal が決める。
	ResolvableBy string
}

// 契約の requested_action.resolvable_by の値。
const (
	contractResolvableByAnswer   = "answer"
	contractResolvableByTerminal = "terminal"
	// ContractResolvableByInvalid は invoker が、文字列でない値・空文字の resolvable_by を渡すための値（閉集合の外。terminal として扱う）。
	ContractResolvableByInvalid = "invalid"
)

// resolvableByTerminal は、求められた操作が resume では解けないものとして扱うべきか。
// 項目が無い（空）・answer は今までどおり答えを resume で返す。それ以外（terminal と閉集合の外の値）は
// 安全側に terminal として扱い、resume を送らず run を勝手に進めない。判定はこの値だけで行い、text は読まない。
func resolvableByTerminal(resolvableBy string) bool {
	return resolvableBy != "" && resolvableBy != contractResolvableByAnswer
}

// ContractArtifact は契約の artifacts の 1 要素。Kind は pr | branch | commit | other。
type ContractArtifact struct {
	Kind string
	Ref  string
	// Role は成果物の役割（任意。無ければ空。run_artifact.role に残す。例: integration）。
	Role string
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
	// Facts は出力の facts（名前から文字列への対応。無い・形が合わないときは nil）。
	Facts map[string]string
	// Flow は出力の flow（フローの名前。無い・文字列でないときは nil）。contract_run.flow に残す。
	Flow *string
	// Steps は出力の steps。nil は「無い」（steps が無い・形が合わない）で、ステップの行に触らない。
	// 空の非 nil は「在って 0 件」で、その委譲の実行のステップの行をすべて消す。
	Steps []ContractStep
}

// ContractStep は契約の出力の steps の 1 要素（形は invoker が確かめ済み）。API の steps と同じ欄に Seq を足したもの。
type ContractStep struct {
	Seq         int
	Step        string
	Kind        string
	Attempt     int
	State       string // running | finished | cancelled | interrupted
	Outcome     *string
	StartedAt   string
	EndedAt     *string
	CostUSD     *float64
	CostUnknown bool
	Model       *string
	ModelsUsed  []string
	Effort      *string // 任意の欄。契約の出力に無い・null は nil
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
	// Resume は判断待ちへの答えを返して続ける（標準出力は start・status と同じ形）。Cancel は run を中止する。
	Resume(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error)
	Cancel(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error)
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
		if !validContractRequestedKind(ra.Kind) {
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
	// WorkDir は契約のコマンドの作業ディレクトリ。空なら課題のリポジトリの作業ディレクトリ（contractRepoWorkDir。決められなければ ErrSlotUnavailable）。
	WorkDir string
	// Reconcile は成果物の PR の base と状態の取得（GET だけ）。release の作成に使う。nil なら release は扱わない。
	Reconcile UpstreamBranchSource
	// Judgment は判断点の起動の実行者。非 nil で、宣言の supervisor が真なら、start の直後に回答を待っていても保留にせず、
	// 次の状態の読みでスーパーバイザーへ回す（それ以外はオーナーの保留へ写す）。
	Judgment JudgmentInvoker
	// TaskID は起動するタスクの内部整数 ID。0 なら、起動できるタスクがちょうど 1 個の課題として、そのタスクを起動する。
	TaskID int64
	// SpecDraft は、起動するタスクを選ぶときに読み直した仕様の下書きの全文。下書きをリポジトリへ入れるタスクの文面の
	// ファイルの最後に足す（そのタスクで空なら、起動しない）。
	SpecDraft string
	// RunRecording は start の run をストアに記録する区間。非 nil なら、StartContractDelegation が区間に入り、run の記録を
	// ちょうど 1 回行い、成功したら確定を知らせてから区間を出る（nil なら区間なしで記録する）。
	// 同時委譲数の許可の数えは、ストアの run の数と、記録前の許可の数の和なので、記録の確定から許可の数の解放までの間に
	// 別のタスクの許可が数えると、同じ run を 2 回数えて断る。記録と許可の数の解放を、許可の数えと同時に起きない 1 つの区間にする。
	RunRecording runRecordingSection
}

func (in ContractStartInput) releaseSource() *contractReleaseSource {
	return &contractReleaseSource{Reconcile: in.Reconcile, Decl: in.ConnDecl, ToSupervisor: canRouteToSupervisor(in.Judgment, in.AgentDecl), Agent: in.AgentDecl}
}

func (in ContractPollInput) releaseSource() *contractReleaseSource {
	return &contractReleaseSource{Reconcile: in.Reconcile, Decl: in.ConnDecl, ToSupervisor: canRouteToSupervisor(in.Judgment, in.AgentDecl), Checks: in.Checks, Compare: in.Compare, Supervisor: in.AgentDecl != nil && in.AgentDecl.Supervisor, Agent: in.AgentDecl}
}

// ContractPollInput は Store.PollContractRuns の入力。
type ContractPollInput struct {
	// ChallengeID が非 nil ならその課題の契約の run だけを読む。nil なら終わっていないものすべて。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	ConnDecl    *ConnectorsDeclaration
	Client      ContractClient
	CycleID     string
	// Reconcile は成果物の PR の base と状態の取得（GET だけ）。release の作成と置き換えに使う。nil なら release は扱わない。
	Reconcile UpstreamBranchSource
	// Checks・Compare は、統合先を使う課題のタスクの実行へ「入れてよい」を返す前の確かめ（中身のチェックの取得・変わり方の比較）と、
	// 入った中身の確かめに使う上流の取得の口（GET だけ）。nil なら、その確かめができないので「入れてよい」を返さない。
	Checks  UpstreamCheckSource
	Compare UpstreamCompareSource
	// Git は、下書きづくりの実行が終わった後に、対象のリポジトリの元のクローンの変わっているパスを読む口。nil なら確かめられないので、
	// 下書きづくりは失敗にする。
	Git SlotGit
	// WorkDir が空なら課題のリポジトリの作業ディレクトリ（決められなければワークスペース）。下書きづくりの実行には使わない。
	WorkDir string
	// Judgment は判断点の起動の実行者。非 nil で、宣言の supervisor が真なら、回答を待つ契約の run（kind が answer で、ターミナルで解く待ちでない。
	// 答える者は問わない）をスーパーバイザーへ回す（それ以外はオーナーの保留へ写す）。Cycle は周の枠超過の共有（nil なら共有しない）。
	Judgment JudgmentInvoker
	Cycle    *JudgmentCycle
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
	// RequestedAction は state が waiting のときの求められた操作（無ければ nil）。求めに応じて保留へ写す（Held を参照）。
	RequestedAction *ContractRequestedAction
	// PRs は state が succeeded になったときに run の成果物へ取り込んだ PR の参照。
	PRs []string
	// Artifacts は読んだ状態の artifacts（waiting のレビュー待ちの成果物の取り込みに使う）。
	Artifacts []ContractArtifact
	// Held は、この呼び出しで課題を保留（人間対応待ち）にしたとき真。waiting の写し・resume の失敗・予算の中止の失敗・契約の run が failed・cancelled で
	// 終わった、または start の最中に flywheel の run が回収されたときで、自動では委譲し直さない。
	Held bool
	// Facts は読んだ状態の facts（無い・形が合わないときは nil）。
	Facts map[string]string
	// FactsCounted は、この読みで満たさなかった回数（contract_run.facts_unmet_count）を数えたとき真（状態の確認で、課題が
	// 着手中のレビュー待ちが中身の確認を通ったとき）。FactsUnmetCount は数えた後の回数。
	FactsCounted    bool
	FactsUnmetCount int
	// FactsPrevCount は数える前の回数（反映先宛ての成果物の数の確認で保留にしたとき、元に戻す）。
	FactsPrevCount int
	// PendingRespond は、このレビュー待ちが required_facts を満たさず、同じ契約の run へ返す respond（呼び出し元が resume で返す）。
	PendingRespond *contractAnswer
	// CostDeltaUSD は今回この run に記録した費用（累計の増分。何度読んでも同じ累計なら 0）。CostTotalUSD は累計。
	CostDeltaUSD float64
	CostTotalUSD float64
	Error        string
}

// contractRunRow は contract_run の 1 行。
type contractRunRow struct {
	// reqFacts は宣言の required_facts（DB の列ではない。呼び出し元が状態の確認の前に置く）。
	reqFacts      map[string][]string
	ID            int64
	ChallengeID   int64
	ConnectorID   string
	ExternalRunID string
	State         ContractState
	Summary       string
	CostMicros    int64
	MaxBudget     int64
	// RequestedKind・RequestedDecider・RequestedText は最後に読んだ求められた操作（無ければ空）。
	RequestedKind, RequestedDecider, RequestedText string
	// RequestedResolvableBy は最後に読んだ求められた操作の resolvable_by（無ければ空）。
	RequestedResolvableBy string
	// WorkDir は start を実行した作業ディレクトリ（0023 より前に始めた run は空）。
	WorkDir string
	// TaskID は結び付くタスクの内部整数 ID（0026。nil は NULL）。
	TaskID *int64
	// Kind は契約の実行の種類（task | reflect | spec）。OperationID は反映の依頼が結び付く反映の操作（task は nil）。
	Kind        string
	OperationID *int64
	// LandRef は「入れてよい」を返す前に書いた通す中身（NULL・空は、いまの承認ではまだ返していないこと）。
	// LandDelivered は、その「入れてよい」を委譲先が受け取ったと分かっているか。
	LandRef       string
	LandDelivered bool
	// StartRunID は、この契約の実行を始めた契約のコマンド（start）の flywheel の run の内部整数 ID。
	StartRunID int64
	// VerifiedRef は、タスクの検証（J5）が「満たした」とした中身（統合先を使う課題のタスクの実行。無ければ空）。
	VerifiedRef string
	// FixPendingRef・FixVerifiedRef・FixRejectedRef は、コンフリクトの解消の確かめ（S3c）の列（無ければ空）。意味と使い方は contract_fix_refs.go。
	FixPendingRef, FixVerifiedRef, FixRejectedRef string
	// holdSuccessEnd は DB の列ではない。真なら、succeeded を読んでも ended_at を入れない（統合先へ入ったかを上流で確かめるまで開けておく）。
	holdSuccessEnd bool
	// discard は DB の列ではない。真なら、この実行はもう要らないので取り消すだけで、成果物を取り込まず課題も保留にしない。
	discard bool
}

// contractKindOrTask は契約の実行の種類 kind（空は task）を返す。
func contractKindOrTask(kind string) string {
	if kind == "" {
		return contractRunKindTask
	}
	return kind
}

// 契約の実行の種類（contract_run.kind）。
const (
	contractRunKindTask    = "task"
	contractRunKindReflect = "reflect"
	// contractRunKindSpec は下書きづくりの実行。タスクも計画も持たない。
	contractRunKindSpec = "spec"
)

// contractReviewWaitingOpenTx は、課題のタスク taskID の、開いている（終わっていない）契約のレビュー待ちの実行があるかを、
// 呼び出し側のトランザクション（読み取り・書き込みのどちらも）の中で返す。レビュー待ちは、状態が waiting で、求められた操作の種類が review のもの。
// 終わっていない契約の実行は課題のタスクごとに高々 1 つ（idx_contract_run_active_challenge）なので、あればそのタスクの最後の契約の実行である。
// スーパーバイザーの選べる手（send_back を残すか）と、手 send_back の実行の時点の確かめが共有する。
func contractReviewWaitingOpenTx(ctx context.Context, tx *sql.Tx, challengeID, taskID int64) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND task_id = ? AND kind = ?
		AND ended_at IS NULL AND state = ? AND COALESCE(requested_kind, '') = ?`,
		challengeID, taskID, contractRunKindTask, string(ContractStateWaiting), contractRequestedKindReview).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

const contractRunSelect = `SELECT id, challenge_id, connector_id, external_run_id, state, summary, cost_usd, max_budget_usd,
	COALESCE(requested_kind, ''), COALESCE(requested_decider, ''), COALESCE(requested_text, ''), COALESCE(requested_resolvable_by, ''), COALESCE(work_dir, ''), task_id,
	kind, operation_id, COALESCE(land_ref, ''), land_delivered, start_run_id, COALESCE(verified_ref, ''),
	COALESCE(fix_pending_ref, ''), COALESCE(fix_verified_ref, ''), COALESCE(fix_rejected_ref, '') FROM contract_run`

func scanContractRun(row interface{ Scan(...any) error }) (*contractRunRow, error) {
	var r contractRunRow
	var state string
	var taskID, opID sql.NullInt64
	if err := row.Scan(&r.ID, &r.ChallengeID, &r.ConnectorID, &r.ExternalRunID, &state, &r.Summary, &r.CostMicros, &r.MaxBudget,
		&r.RequestedKind, &r.RequestedDecider, &r.RequestedText, &r.RequestedResolvableBy, &r.WorkDir, &taskID,
		&r.Kind, &opID, &r.LandRef, &r.LandDelivered, &r.StartRunID, &r.VerifiedRef, &r.FixPendingRef, &r.FixVerifiedRef, &r.FixRejectedRef); err != nil {
		return nil, err
	}
	if opID.Valid {
		v := opID.Int64
		r.OperationID = &v
	}
	if taskID.Valid {
		v := taskID.Int64
		r.TaskID = &v
	}
	r.State = ContractState(state)
	return &r, nil
}

// contractFill は契約のコマンドの引数の差し込みの値。値の無い差し込みは空の文字列で埋める。
type contractFill struct {
	ChallengeID string
	// Source は取り込み元（無ければ nil）。EmptySource が真のとき、nil の取り込み元の差し込みを空の文字列で埋める
	// （偽のときは ErrValidation）。
	Source      *sourceBinding
	EmptySource bool
	// RunID は status・resume・cancel の契約の実行 ID。
	RunID  string
	Answer contractAnswer
	// TaskFile・TaskID・UsesIntegration は start の差し込み。
	TaskFile, TaskID string
	UsesIntegration  bool
	// ApprovedRef は resume の {approved_ref}。「入れてよい」を返す反映の段だけが通す中身を入れる（それ以外は空）。
	ApprovedRef string
	// Flow は form: flow の start の、フローの名前（操作の flow）。cli の形では使わない。
	Flow string
	// ArtifactRef は reflect の {artifact_ref}（反映の操作の参照）。反映の依頼を始める反映の段だけが入れる（それ以外は空）。
	ArtifactRef string
	// Spec は下書きづくりの開始（contractCmdSpec）の値。ほかのコマンドでは nil。
	Spec *specContractFill
}

// fillContractArgs は契約のコマンドの引数の差し込みを、取り込み元が無いときに空の文字列で埋めない形で埋める
// （fillContractArgsWith の、{task_file}・{task_id}・{uses_integration} が空・EmptySource が偽の呼び出し）。
func fillContractArgs(args []string, challengeID string, sb *sourceBinding, externalRunID string, answer contractAnswer) ([]string, error) {
	return fillContractArgsWith(args, contractFill{ChallengeID: challengeID, Source: sb, RunID: externalRunID, Answer: answer})
}

// fillCommandArgs は接続ツール connector の宣言のコマンドの引数を埋める。commands.start に {task_file} を書いた宣言では、
// 取り込み元の無いタスクの取り込み元の差し込みを空の文字列で埋める。
func (s *Store) fillCommandArgs(connector *Connector, cmd contractCmd, challengeID string, sb *sourceBinding, externalRunID string, answer contractAnswer) ([]string, error) {
	return s.contractCommandArgs(connector, cmd, contractFill{ChallengeID: challengeID, Source: sb, EmptySource: startTakesTaskFile(connector), RunID: externalRunID, Answer: answer})
}

// fillRowCommandArgs は契約の実行 row のコマンドの引数を埋める。反映の依頼（上げる依頼を含む）は取り込み元を持たないので、
// 取り込み元の差し込みを空の文字列で埋める。それ以外は fillCommandArgs と同じ。
func (s *Store) fillRowCommandArgs(connector *Connector, row *contractRunRow, cmd contractCmd, challengeID string, sb *sourceBinding, answer contractAnswer) ([]string, error) {
	if row.Kind == contractRunKindReflect || row.Kind == contractRunKindSpec { // 取り込み元を読まない
		return s.contractCommandArgs(connector, cmd, contractFill{ChallengeID: challengeID, EmptySource: true, RunID: row.ExternalRunID, Answer: answer})
	}
	return s.fillCommandArgs(connector, cmd, challengeID, sb, row.ExternalRunID, answer)
}

// startTakesTaskFile は、接続ツールの commands.start が {task_file} を書いているか（取り込み元の無いタスクも起動する宣言）。
func startTakesTaskFile(c *Connector) bool {
	if c != nil && c.Form == ConnectorFormFlow {
		return true // flow の形の start は、いつも task_file を入力に渡す
	}
	if c == nil || c.Commands == nil {
		return false
	}
	for _, a := range c.Commands.Start {
		if strings.Contains(a, "{task_file}") {
			return true
		}
	}
	return false
}

// fillContractArgsWith は契約のコマンドの引数の差し込みを埋める。取り込み元の 3 つ（{issue_number}・{issue_url}・{external_key}）と
// {challenge_id}、status・resume・cancel の {run_id}、resume の {answer}・{choice}・{note}、start の {task_file}・{task_id}・
// {uses_integration}、resume の {approved_ref}（反映の段が「入れてよい」を返すときだけ値が入り、それ以外は空）。引数の要素の数は変えない。
func fillContractArgsWith(args []string, f contractFill) ([]string, error) {
	out := make([]string, len(args))
	usesIntegration := "false"
	if f.UsesIntegration {
		usesIntegration = "true"
	}
	for i, a := range args {
		// すべての差し込みを 1 回の置き換えにまとめる（差し込んだ値の中の波括弧を、他の差し込みとして読まない）。
		pairs := []string{"{challenge_id}", f.ChallengeID}
		if invocationNeedsSource(a) {
			switch {
			case f.Source != nil:
				_, number, ok := parseExternalKey(f.Source.ExternalKey)
				if !ok {
					return nil, fmt.Errorf("%w: malformed external_key %q", ErrValidation, f.Source.ExternalKey)
				}
				pairs = append(pairs, "{issue_number}", fmt.Sprintf("%d", number), "{issue_url}", f.Source.URL, "{external_key}", f.Source.ExternalKey)
			case f.EmptySource:
				pairs = append(pairs, "{issue_number}", "", "{issue_url}", "", "{external_key}", "")
			default:
				return nil, fmt.Errorf("%w: the operation needs the source issue but the challenge has none", ErrValidation)
			}
		}
		pairs = append(pairs, "{run_id}", f.RunID,
			"{choice}", f.Answer.Choice, "{note}", f.Answer.Note, "{answer}", f.Answer.text(),
			"{task_file}", f.TaskFile, "{task_id}", f.TaskID, "{uses_integration}", usesIntegration, "{approved_ref}", f.ApprovedRef,
			"{artifact_ref}", f.ArtifactRef)
		out[i] = strings.NewReplacer(pairs...).Replace(a)
	}
	return out, nil
}

// 契約の review の待ちへ flywheel が返す答えの選択肢の値（契約の汎用の値。接続ツールは自分の選択肢へ写す）。
const (
	// contractChoiceLand は、検証と承認を通った中身を宛先へ入れてよいことを知らせる答え。反映の段だけが、通す中身（{approved_ref}）を付けて返す。
	contractChoiceLand = "land"
	// contractChoiceRespond は、完了の検証（J5）の差し戻しの理由を添えて、同じ run で直させる答え。
	contractChoiceRespond = "respond"
)

// contractAnswer は resume へ渡す答え。Choice は選択肢の値、Note は添え書き（自由な文）。
// オーナーの保留への答え（分けて受け取る仕組みは未実装）は文全体を Choice として渡し、Note は空。
type contractAnswer struct {
	Choice, Note string
}

// text は {answer} に差し込む文。添え書きがあれば選択肢の値の次の行に続ける。
func (a contractAnswer) text() string {
	if a.Note == "" {
		return a.Choice
	}
	return a.Choice + "\n" + a.Note
}

// checkPlannedContractOperation は起動するタスクの操作 operation を宣言の connector から引き、
// J2 と同じ checkOperationLaunchable で検査する。計画が無い・構造化した出力が無い・操作が
// connector の宣言に無いときも ErrValidation（起動しない）。
func checkPlannedContractOperation(plan approvedPlan, connector *Connector, hasSource bool, operation string) error {
	if !plan.HasSpec {
		return fmt.Errorf("%w: the challenge has no approved plan with a structured spec", ErrValidation)
	}
	if !json.Valid([]byte(plan.Spec)) {
		return fmt.Errorf("%w: the approved plan spec is malformed", ErrValidation)
	}
	for i := range connector.Operations {
		if connector.Operations[i].ID == operation {
			return checkOperationLaunchable(connector, &connector.Operations[i], hasSource)
		}
	}
	return fmt.Errorf("%w: operation %q is not declared for connector %q", ErrValidation, operation, connector.ID)
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
		if !c.UsesContractCommands() {
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

// contractRepoWorkDir は契約のコマンド（start・status・resume・cancel）の作業ディレクトリを決める。
// explicit（呼び出し元が渡した WorkDir）が非空ならそれを使う。空なら、課題の承認済みの計画のリポジトリ（repos[]）から
// repoIdleWorkDir で決める（worktree は元のクローン、clone は状態が idle の作業用クローンのうち paths の順で最初のもの）。
// 決められないとき（承認済みの計画が無い・宣言と合わない・idle の作業用クローンが無い）は ok=false。
func (s *Store) contractRepoWorkDir(ctx context.Context, agent *AgentDeclaration, conn *ConnectorsDeclaration, cid int64, explicit, taskRepo string) (string, bool, error) {
	if explicit != "" {
		return explicit, true, nil
	}
	if taskRepo != "" {
		// 起動するタスクの対象リポジトリ（タスクの行のもの）。
		repo, _ := conn.findConnectorRepo(taskRepo)
		if repo == nil {
			return "", false, nil
		}
		return s.repoIdleWorkDir(ctx, *repo)
	}
	var plan approvedPlan
	var planOK bool
	var binding *sourceBinding
	var doneCovers map[string]bool
	err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		if plan, planOK, err = loadApprovedPlanAnySpec(ctx, tx, cid); err != nil || !planOK {
			return err
		}
		if doneCovers, err = loadDoneTaskCovers(ctx, tx, cid); err != nil {
			return err
		}
		binding, err = loadSourceBindingByChallengeID(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return "", false, err
	}
	if !planOK || conn == nil {
		return "", false, nil
	}
	validated, vok := validateJ2Output([]byte(plan.Spec), j2ValidationContext{Agent: agent, Conn: conn, HasSource: binding != nil, SkipFlowReason: true, RecheckCriteria: true, DoneCovers: doneCovers})
	if !vok {
		return "", false, nil
	}
	task, ok := validated.soleTask()
	if !ok {
		return "", false, nil
	}
	repo, _ := conn.findConnectorRepo(task.Repo)
	if repo == nil {
		return "", false, nil
	}
	return s.repoIdleWorkDir(ctx, *repo)
}

// existingContractWorkDir は既存の契約の run へのコマンド（status・resume・cancel）の作業ディレクトリ。決め直さず、
// start で記録した作業ディレクトリ（contract_run.work_dir）を使う。記録の無い run（記録を足す前に始めたもの）は
// ワークスペース（contractWorkDir の既定）で実行する。呼び出し元が WorkDir を明示したときはそれを使う（下書きづくりの実行は、
// いつも記録した作業ディレクトリ）。
func existingContractWorkDir(row *contractRunRow, explicit string) string {
	if explicit != "" && row.Kind != contractRunKindSpec {
		return explicit
	}
	return row.WorkDir
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
	var plan approvedPlan
	var tasks []Task
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if ch == nil {
			return ErrNotFound
		}
		if ch.Status != StatusInProgress {
			// タスクを止める保留だけが開いている課題は、保留の無いタスクだけ起動できる。
			if in.TaskID <= 0 {
				return errDelegationNotEligible
			}
			if may, err := taskMayProceed(ctx, tx, cid, in.TaskID); err != nil {
				return err
			} else if !may {
				return errDelegationNotEligible
			}
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
			plan = p
		}
		tasks, err = loadTasksToLaunch(ctx, tx, cid, in.TaskID)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	// 起動の前に、J2 と同じ関数で承認済みの計画の操作を検査する（J2 の後に宣言が変わっても、検査していない引数で起動しない）。
	// 起動するタスクはちょうど 1 個（in.TaskID が 0 なら、起動できるタスクがちょうど 1 個の課題）。対象のリポジトリと操作は
	// タスクの行のもの。
	if len(tasks) != 1 {
		return nil, fmt.Errorf("%w: the challenge does not have exactly one launchable task", ErrValidation)
	}
	task := tasks[0]
	taskID := taskIDPtr(&task)
	// 承認したフロー（planned_flow）があるのに、いまのタスクのリポジトリの委譲先が flow の形でないときは、承認していない形で
	// 走らせない。契約のコマンドを起動せず、オーナーへの保留にする。
	if task.PlannedFlow != "" && connector.Form != ConnectorFormFlow {
		return nil, s.holdPlannedFlowUnrunnable(ctx, in.ChallengeID, task, connector)
	}
	if err := checkPlannedContractOperation(plan, connector, binding != nil, task.Operation); err != nil {
		return nil, err
	}
	var taskSource *sourceBinding
	if err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		taskSource, err = loadTaskSourceBinding(ctx, tx, cid, taskID)
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, classifyReadWriteErr(err)
	}
	// タスクの文面と {uses_integration}。起動の実行の ID を知る前に、差し込みを埋められることだけを確かめる。
	var taskText string
	var usesIntegration bool
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		// 文面を使うのは、commands.start に {task_file} を書いた宣言だけ（書いていない宣言は、文面の組み立てで止めない）。
		if startTakesTaskFile(connector) {
			doc, err := loadTaskDocument(ctx, tx, cid, plan, task)
			if err != nil {
				return err
			}
			// 一覧の壊れた計画は、上の文面の組み立てが断っている。
			list, err := loadPlanAcceptanceCriteria(plan.Spec)
			if err != nil {
				return err
			}
			if landsSpecDraft(list, task) {
				if in.SpecDraft == "" {
					return fmt.Errorf("%w: the task %s lands the spec draft but the draft text is not given", ErrValidation, task.ID)
				}
				doc = appendSpecDraftToTaskDocument(doc, in.SpecDraft)
			}
			taskText = doc
		}
		u, err := challengeUsesIntegration(ctx, tx, cid)
		if u != nil {
			usesIntegration = *u
		}
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, classifyReadWriteErr(err)
	}
	startFill := contractFill{
		ChallengeID: in.ChallengeID, Source: taskSource, EmptySource: startTakesTaskFile(connector),
		TaskFile: "", TaskID: task.ID, UsesIntegration: usesIntegration, Flow: taskStartFlow(connector, task),
	}
	if _, err := s.contractCommandArgs(connector, contractCmdStart, startFill); err != nil {
		return nil, err
	}
	workDir, ok, err := s.contractRepoWorkDir(ctx, in.AgentDecl, in.ConnDecl, cid, in.WorkDir, task.Repo)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSlotUnavailable
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
	runIDInt, err := s.recordContractStartRun(in.RunRecording, func() (int64, error) {
		return s.beginContractRunForTask(ctx, cid, cycleIDInt, planVersion, sessionID, maxMicros, true, taskID, nil)
	})
	if err != nil {
		return nil, err
	}
	// タスクの文面を、起動の実行の記録の置き場へ書き、その絶対パスを {task_file} に渡す（書けなければ起動せず、
	// 起動できなかった run として記録する）。ファイルは消さない。
	runDir := runDirPath(s.workspace, formatRunID(runIDInt))
	var taskFile string
	var writeErr error
	if startTakesTaskFile(connector) {
		taskFile, writeErr = writeTaskFile(runDir, taskText)
	}
	startFill.TaskFile = taskFile
	command, err := s.contractCommandArgs(connector, contractCmdStart, startFill)
	if err != nil {
		return nil, err
	}
	call := func(ctx context.Context, li ContractLaunchInput) (ContractLaunchOutput, error) {
		if writeErr != nil {
			return ContractLaunchOutput{Result: RunResultLaunchFailed, ErrorSummary: fmt.Sprintf("write task file: %v", writeErr)}, nil
		}
		return in.Client.Start(ctx, li)
	}
	rep, err := s.finishContractCall(ctx, contractCall{
		ChallengeID: in.ChallengeID, ChallengeIDInt: cid, RunIDInt: runIDInt, Connector: connector.ID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: s.contractWorkDir(workDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
			Command: command, TimeoutSec: in.AgentDecl.TimeoutSec.Delegate,
		},
		Invoke: call, MaxMicros: maxMicros, TaskIDInt: taskID, HoldSuccessEnd: usesIntegration, Agent: in.AgentDecl,
	})
	if err != nil {
		return nil, err
	}
	if err := s.afterContractRead(ctx, rep, connector, in.releaseSource()); err != nil {
		return nil, err
	}
	return rep, nil
}

// beginContractRun は beginContractRunForTask を、タスクに結び付けずに呼ぶ（テストの足場が使う。本体は、タスクの ID
// 〔過去の行では nil〕を渡して beginContractRunForTask を直に呼ぶ）。
func (s *Store) beginContractRun(ctx context.Context, cid int64, cycleID, planVersion *int64, sessionID string, maxMicros int64, isStart bool) (int64, error) {
	return s.beginContractRunForTask(ctx, cid, cycleID, planVersion, sessionID, maxMicros, isStart, nil, nil)
}

// beginContractRunForTask は契約のコマンド 1 回ぶんの run を記録する。start のときは周の上限を評価し、そのタスク（タスクが
// 無ければ課題）に終わっていない契約の run・run があれば ErrRunInProgress にする。
//
// taskID が非 nil（start のときは起動するタスク、status・resume・cancel のときは契約の run が結び付くタスク）なら、
// run の行（kind=contract）の task_id と repo（タスクの対象リポジトリ）へ記録する。start のときは、pending のタスクを
// in_progress にする（run の記録と同じトランザクション。すでに in_progress のタスクでは変えない）。
//
// operationID が非 nil なら、反映の操作のために呼んだ run として印を付ける（run.operation_id。反映の段・完了から戻す前の読み・
// 片付けだけが渡す。印の付いた run は、課題の最後の委譲の run・最後の契約のコマンドを読む所から外れる）。
func (s *Store) beginContractRunForTask(ctx context.Context, cid int64, cycleID, planVersion *int64, sessionID string, maxMicros int64, isStart bool, taskID, operationID *int64) (int64, error) {
	return s.beginContractRunMarked(ctx, cid, cycleID, planVersion, sessionID, maxMicros, isStart, taskID, operationID, false, budgetBucketImpl)
}

// beginContractRunMarked は beginContractRunForTask に、反映の依頼の印（run.reflect）を足したもの。reflect が真なら、反映の操作が無い
// 反映の依頼（上げる依頼）の run にも印を付ける（課題の使った額・最後の委譲の実行の読みから外すため）。bucket は run の予算の枠。
func (s *Store) beginContractRunMarked(ctx context.Context, cid int64, cycleID, planVersion *int64, sessionID string, maxMicros int64, isStart bool, taskID, operationID *int64, reflect bool, bucket budgetBucket) (int64, error) {
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
				if taskID == nil {
					return errDelegationNotEligible
				}
				if may, err := taskMayProceed(ctx, tx, cid, *taskID); err != nil {
					return err
				} else if !may {
					return errDelegationNotEligible
				}
			}
			// 終わっていない契約の run のほか、保留に写し切れていない回収・失敗・中止も実行中として数える。起動するタスクが
			// 決まっているときは、そのタスクの単位で見る（同じ課題の別のタスクの実行は妨げない。タスクに結び付かない実行との
			// 重なりは insertRun が止める）。
			var active bool
			var err error
			if taskID != nil {
				active, err = taskHasActiveRun(ctx, tx, cid, *taskID)
				if err == nil && !active {
					active, err = taskLessHasActiveRun(ctx, tx, cid)
				}
				// レビュー待ちの契約の実行は「実行中」に数えないが、終わっていない契約の実行が残るタスクを、もう一度 start しない
				// （外部の実行を二重に作らず、記録が一意の索引に当たって外部の実行だけが残るのを防ぐ）。
				if err == nil && !active {
					active, err = taskHasOpenContractRun(ctx, tx, cid, *taskID)
				}
			} else {
				active, err = challengeHasActiveRun(ctx, tx, cid)
			}
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
		// run の行へ書く対象リポジトリは、タスクの行のもの。タスクの行が無いときは、start は止め、既にある契約の run への
		// コマンド（読み直し・再開・中止）は、タスクに結び付けずに続ける（記録のために run を止めない）。
		var taskRepo string
		runTask := taskID
		if taskID != nil {
			err := tx.QueryRowContext(ctx, `SELECT repo FROM task WHERE id = ?`, *taskID).Scan(&taskRepo)
			switch {
			case errors.Is(err, sql.ErrNoRows) && isStart:
				return ErrNotFound
			case errors.Is(err, sql.ErrNoRows):
				runTask = nil
			case err != nil:
				return err
			}
		}
		row, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID: cycleID, Kind: runKindContract, ChallengeVersion: int64(cur.Version), PlanVersion: planVersion,
			SessionID: sessionID, PID: int64(os.Getpid()), Host: host, HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: maxMicros, BudgetBucket: bucket, TaskID: runTask, Repo: taskRepo, OperationID: operationID, Reflect: reflect,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("core: malformed run id %q", row.ID)
		}
		runIDInt = n
		if isStart && taskID != nil {
			return s.startTaskTx(ctx, tx, *taskID, &n)
		}
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
	// TaskIDInt は contract_run を新しく作るときに書く task_id（nil は NULL。既存の行の task_id は変えない）。
	TaskIDInt *int64
	// Kind・OperationIDInt は contract_run を新しく作るときに書く kind と operation_id（反映の依頼の start だけが指定する。
	// 空の Kind は task、nil の OperationIDInt は NULL。既存の行は変えない）。
	Kind           string
	OperationIDInt *int64
	// FactsRequired は宣言の required_facts（無ければ nil）。CountFacts は、この呼び出しで満たさなかった回数を数えるとき真
	// （状態の確認だけ。resume の応答は数えない）。
	FactsRequired map[string][]string
	CountFacts    bool
	// StopNote は、中止の理由（予算の超過）。契約が cancelled・failed を返して保留にするときの問いの先頭に付く。
	StopNote string
	// ReleaseStage は、反映の段が呼ぶ契約のコマンド（完了の課題の実行）のとき真。成果物を課題の成果物の記録（run_artifact）へ取り込まず、
	// 課題を保留にしない（契約の実行の記録だけを読んだ値で更新する）。
	ReleaseStage bool
	// Agent は宣言の agent（保留を作るときに宣言の supervisor を見るため。nil は宣言の既定と同じ扱い）。
	Agent *AgentDeclaration
	// HoldEnd は、終わりを読んでも contract_run の ended_at を入れないとき真（反映の段の status・resume。結果を書く書き込みが入れる）。
	HoldEnd bool
	// HoldSuccessEnd は、succeeded だけは ended_at を入れないとき真（failed・cancelled は、「入れてよい」を返し済みの実行〔landedStop〕以外は終わった記録にする）。
	HoldSuccessEnd bool
}

// isSpec は、下書きづくりの実行の呼び出し（開始と、既にある実行へのコマンド）か。
func (c contractCall) isSpec() bool {
	return c.Kind == contractRunKindSpec || (c.Existing != nil && c.Existing.Kind == contractRunKindSpec)
}

// landedStop は、状態 st が失敗・中止で、「入れてよい」を返し済み（land_ref がある）の統合先を使う課題のタスクの実行か。
// こうした実行は、統合先へ入ったかを上流で確かめるまで、終わった記録にせず、通す中身もタスクも変えない。
func (c contractCall) landedStop(st ContractState) bool {
	return c.HoldSuccessEnd && (st == ContractStateFailed || st == ContractStateCancelled) && c.Existing != nil && c.Existing.LandRef != ""
}

// keepsOpenOn は、終わりの状態 st を読んでも、契約の実行（contract_run）を終わった記録（ended_at）にしないか。
func (c contractCall) keepsOpenOn(st ContractState) bool {
	return c.HoldEnd || (c.HoldSuccessEnd && st == ContractStateSucceeded) || c.landedStop(st)
}

// finishContractCall はコマンドを起動し、結果を run と contract_run へ 1 つのトランザクションで記録する。
func (s *Store) finishContractCall(ctx context.Context, c contractCall) (*ContractRunReport, error) {
	// form: flow のコマンドの --delegated-run は、run の行を作った後のここで、その run の ID にする。
	c.Launch.Command = withDelegatedRun(c.Launch.Command, formatRunID(c.RunIDInt))
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
		report.Artifacts = status.Artifacts
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
				// 反映の段が読んだ終わりを、結果を書くまで開けていた実行（HoldEnd）は、その場で終わる呼び出し（完了から戻す前の読み・片付け）が閉じる。
				if !c.keepsOpenOn(latest.State) {
					if _, err := tx.ExecContext(recordCtx, `UPDATE contract_run SET ended_at = ?, fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL WHERE id = ? AND ended_at IS NULL`,
						formatTimestamp(s.currentTime()), latest.ID); err != nil {
						return err
					}
				}
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
	if c.isSpec() {
		// 下書きづくりの実行は、タスクの契約の実行の保留を作らない。開始の失敗だけを、下書きづくりの保留にする。
		if c.Existing == nil {
			held, herr := s.holdFailedSpecDraftStart(ctx, c, status, reclaimed, result, errSummary)
			if herr != nil {
				return nil, herr
			}
			report.Held = held
		}
		return report, nil
	}
	if question := contractHoldQuestion(c, status, out.Status, reclaimed, result, errSummary); question != "" && !c.ReleaseStage &&
		(status == nil || !c.landedStop(status.State)) {
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
//   - start が回収以外で失敗した（時間切れ・不正な出力・起動の失敗・その他のエラー）: 契約の run は記録されないが、
//     コマンドが外部の run を切り離して始めた後に失敗したかもしれない。回収と同じく保留にし、失敗の種類を問いに書く
//     （自動では委譲し直さない）。
//   - 契約が failed・cancelled を返した（recorded は記録に使った状態。別のプロセスが先に終えていれば nil）。
//     存在しない run の状態を読んでも failed が返ることがあり、状態の置き場の設定違いと本当の失敗を
//     見分けられないため、自動で失敗にはしない。
func contractHoldQuestion(c contractCall, recorded, observed *ContractStatus, reclaimed bool, result RunResult, errSummary string) string {
	ext := ""
	if observed != nil {
		ext = observed.RunID
	}
	if reclaimed {
		if c.Existing != nil {
			return "" // 状態の確認の最中の回収は、契約の run がそのまま残るので次の周で読み直す
		}
		return contractReclaimedQuestion(ext, c.Connector)
	}
	if c.Existing == nil && result != RunResultSucceeded {
		return contractStartFailedQuestion(result, errSummary, ext, c.Connector)
	}
	if recorded == nil {
		return ""
	}
	q := contractStopQuestion(recorded.State, recorded.RunID, recorded.Summary)
	if q != "" && c.StopNote != "" {
		q = c.StopNote + "\n" + q
	}
	return q
}

// contractStopQuestion は契約が failed・cancelled を返したときの問い（それ以外の state は空）。
func contractStopQuestion(state ContractState, runID, summary string) string {
	return contractStopQuestionDuringHold(state, runID, summary, false)
}

// contractStopQuestionDuringHold は contractStopQuestion に、duringHold（課題が保留の間に契約の run が終わっていた）のとき
// その旨を添える。答えたのに何も起きないように見えないよう、答えの後の周で失敗として写すこの問いで、保留の間に終わったことを伝える。
func contractStopQuestionDuringHold(state ContractState, runID, summary string, duringHold bool) string {
	q := contractStopQuestionBody(state, runID, summary)
	if q != "" && duringHold {
		q += "\nこの run は、課題が保留の間に（回答する前に）終わっていた。先の保留への回答は run には届いておらず、" +
			"回答は保留の記録に残っている。新しい run は始めていない。"
	}
	return q
}

func contractStopQuestionBody(state ContractState, runID, summary string) string {
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

// contractStartFailedQuestion は start が回収以外で失敗したときの問い。result は失敗の種類（run の結果）、
// errSummary はその詳細。externalRunID・connector は分かっていれば渡す（空なら書かない）。
func contractStartFailedQuestion(result RunResult, errSummary, externalRunID, connector string) string {
	q := fmt.Sprintf("接続ツールの run を始めるコマンドが失敗した（失敗の種類: %s", result)
	if errSummary != "" {
		q += "。詳細: " + errSummary
	}
	q += "）。コマンドが接続ツールの側で run を始めた後に失敗した場合、flywheel が追っていない run が残っているかもしれない。"
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

// holdForContract は、契約の run の結果に基づいて保留を立てる。立てるのは、課題が着手中のときと、人間対応待ちの課題に
// タスクを止める保留として重ねられるときだけ（holdContractWithStop）。それ以外は何もしない（held は偽）。
// 契約の run の記録とは別のトランザクションで行う。
func (s *Store) holdForContract(ctx context.Context, c contractCall, question string) (bool, error) {
	if c.StopNote != "" {
		// 中止の理由（予算の超過・課題の取り下げ）を添えた保留は、止まり方を渡さない（スーパーバイザー待ちにしない）。
		return s.holdContractWithReason(ctx, formatRunID(c.RunIDInt), c.ChallengeID, question, holdReasonContractStopped)
	}
	return s.holdReclaimedContractStart(ctx, formatRunID(c.RunIDInt), c.ChallengeID, question, c.Agent)
}

// holdReclaimedContractStart は runID を原因の run として保留を立てる。立てるのは、課題が着手中のときと、人間対応待ちの課題に
// タスクを止める保留として重ねられるときだけ（それ以外は偽）。
//
// 契約の実行の終わり・回収の保留で、止まり方は delegate_stopped（agent は宣言の supervisor を見るために渡す。nil は宣言の既定と同じ扱い）。
func (s *Store) holdReclaimedContractStart(ctx context.Context, runID, challengeID, question string, agent *AgentDeclaration) (bool, error) {
	return s.holdContractWithStop(ctx, runID, challengeID, question, holdReasonContractStopped, stopHoldOptions(StopKindDelegateStopped, agent))
}

// holdContractWithReason は holdReclaimedContractStart と同じ保留を、理由 reason で、止まり方を渡さずに立てる。
func (s *Store) holdContractWithReason(ctx context.Context, runID, challengeID, question string, reason holdReason) (bool, error) {
	return s.holdContractWithStop(ctx, runID, challengeID, question, reason, HoldOptionsInput{})
}

// holdContractChallengeWide は holdContractWithReason の、課題を止める保留として立てる版（上げる依頼の保留。止める範囲を、
// タスクの結び付きから導かない）。
func (s *Store) holdContractChallengeWide(ctx context.Context, runID, challengeID, question string, reason holdReason) (bool, error) {
	return s.holdContractWithStop(ctx, runID, challengeID, question, reason, HoldOptionsInput{ChallengeWide: true})
}

// holdContractWithStop は holdContractWithReason の、止まり方（opts.StopKind）を渡す版。
func (s *Store) holdContractWithStop(ctx context.Context, runID, challengeID, question string, reason holdReason, opts HoldOptionsInput) (bool, error) {
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), runID, challengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress && tc.current.Status != StatusAwaitingHuman {
				return errReconcileNotEligible
			}
			// 保留は、止まった契約の run が結び付くタスクに結び付ける（同じ課題のほかのタスクの run では、そのタスクではない）。
			// run がタスクに結び付かないとき（start の失敗では契約の run がまだ無い）は、起動できるタスクがちょうど 1 個なら
			// そのタスクに結び付ける。
			var runTask sql.NullInt64
			if n, ok := parseRunID(runID); ok {
				if err := tc.tx.QueryRowContext(ctx, `SELECT task_id FROM run WHERE id = ?`, n).Scan(&runTask); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			if runTask.Valid {
				id := runTask.Int64
				tc.taskID = &id
			} else if tasks, err := loadLaunchableTasks(ctx, tc.tx, tc.id); err != nil {
				return err
			} else if len(tasks) == 1 {
				tc.taskID = taskIDPtr(&tasks[0])
			}
			scoped, err := taskStopOptions(ctx, tc, opts)
			if err != nil {
				return err
			}
			if reason == holdReasonReflectFailed || reason == holdReasonReflectUnavailable {
				scoped.Scope = "" // 上げる依頼の保留は、課題を止める保留
			}
			if tc.current.Status == StatusAwaitingHuman && scoped.Scope != HoldScopeTask {
				return errReconcileNotEligible // 人間対応待ちに重ねられるのは、タスクを止める保留だけ
			}
			_, err = s.insertHoldWithOccurrenceOptions(ctx, tc, question, reason, "", scoped)
			return err
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
	var rk, rd, rt, rb any
	if ra := st.RequestedAction; ra != nil {
		rk, rd, rt = ra.Kind, ra.Decider, ra.Text
		if ra.ResolvableBy != "" {
			rb = ra.ResolvableBy
		}
	}
	var endedAt any
	if st.State.Terminal() && !c.keepsOpenOn(st.State) {
		endedAt = now
	}
	report.Facts = st.Facts
	if err := s.writeContractRunRecord(ctx, tx, c, st, rk, rd, rt, rb, totalMicros, now, endedAt, report); err != nil {
		return err
	}
	// 下書きづくりの実行は、状態を写すだけで、タスク・成果物・課題の遷移に触れない。始まったら、同じ書き込みで記録へ写す。
	if c.isSpec() {
		if c.Existing == nil {
			return s.markSpecDraftRunning(ctx, tx, c.ChallengeIDInt)
		}
		return nil
	}
	// 「入れてよい」を返し済みの実行の失敗・取り消しは、統合先へ入ったかを上流で確かめるまで、通す中身もタスクも変えない
	// （確かめの結果で、済み・保留・着手中へ戻すのどれかを 1 つの書き込みで決める。afterContractRead が扱う）。
	if (st.State == ContractStateFailed || st.State == ContractStateCancelled) && !c.landedStop(st.State) {
		// 統合先を使う課題のタスクの実行が失敗・取り消しで終わったら、検証した中身と通す中身は無効にする（次の委譲の後に検証し直す）。
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = NULL, fix_pending_ref = NULL, land_ref = NULL, land_delivered = 0
			WHERE challenge_id = ? AND external_run_id = ? AND kind = 'task' AND (verified_ref IS NOT NULL OR land_ref IS NOT NULL OR fix_pending_ref IS NOT NULL)
			AND EXISTS (SELECT 1 FROM challenge WHERE id = ? AND uses_integration = 1)`, c.ChallengeIDInt, st.RunID, c.ChallengeIDInt); err != nil {
			return err
		}
		// 検証中のタスクは着手中へ戻す（回答の後に、次の委譲の段が委譲し直せる。検証中のままだと、起動・検証・進めようが無い保留の
		// どれにも当たらず止まる）。タスクの状態の変更は、読んだ記録と同じトランザクションで行う。
		if err := s.reopenVerifyingTaskOfStoppedRun(ctx, tx, c, st.RunID); err != nil {
			return err
		}
	}
	if c.landedStop(st.State) {
		// 統合先へ入ったかの確かめが読めなかったときに、次の確認が記録から確かめ直せるよう、成果物を取り込んでおく。
		_, err := importContractArtifacts(ctx, tx, c.RunIDInt, st.Artifacts, s.currentTime())
		return err
	}
	if st.State != ContractStateSucceeded {
		return nil
	}
	return s.afterContractSucceeded(ctx, tx, c, st, report)
}

// reopenVerifyingTaskOfStoppedRun は、統合先を使う課題のタスクの契約の実行（externalRunID）が失敗・取り消しで終わったとき、
// そのタスクが検証中なら着手中へ戻す。反映の段が読む実行・上げる依頼・タスクに結び付かない実行では何もしない。
func (s *Store) reopenVerifyingTaskOfStoppedRun(ctx context.Context, tx *sql.Tx, c contractCall, externalRunID string) error {
	if c.ReleaseStage {
		return nil
	}
	var taskID int64
	err := tx.QueryRowContext(ctx, `SELECT t.id FROM contract_run k JOIN task t ON t.id = k.task_id JOIN challenge ch ON ch.id = k.challenge_id
		WHERE k.challenge_id = ? AND k.external_run_id = ? AND k.kind = 'task' AND t.status = 'verifying' AND ch.uses_integration = 1`,
		c.ChallengeIDInt, externalRunID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rec, err := s.newInvokerRecorder(tx, &c.RunIDInt)
	if err != nil {
		return err
	}
	_, err = setTaskStatus(ctx, tx, rec, taskID, TaskStatusInProgress)
	return err
}

// writeContractRunRecord は読めた状態で contract_run の行を作る・更新する。あわせて、出力の facts・待ちの options・
// レビュー待ちの中身の識別を、読むたびに置き換えて残す（記録するだけで、扱いは変えない）。
func (s *Store) writeContractRunRecord(ctx context.Context, tx *sql.Tx, c contractCall, st *ContractStatus, rk, rd, rt, rb any, totalMicros int64, now string, endedAt any, report *ContractRunReport) error {
	var rowID int64
	if c.Existing == nil {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary,
				requested_kind, requested_decider, requested_text, requested_resolvable_by, cost_usd, max_budget_usd, started_at, observed_at, ended_at, work_dir, task_id, kind, operation_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ChallengeIDInt, c.Connector, st.RunID, c.RunIDInt, string(st.State), st.Summary, rk, rd, rt, rb,
			totalMicros, c.MaxMicros, now, now, endedAt, c.Launch.WorkDir, int64PtrColumn(c.TaskIDInt),
			contractKindOrTask(c.Kind), int64PtrColumn(c.OperationIDInt))
		if err != nil {
			return err
		}
		if rowID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else {
		rowID = c.Existing.ID
		if _, err := tx.ExecContext(ctx,
			`UPDATE contract_run SET state = ?, summary = ?, requested_kind = ?, requested_decider = ?, requested_text = ?,
				requested_resolvable_by = ?, cost_usd = ?, observed_at = ?, ended_at = ?,
				wait_hold_id = CASE WHEN ? = 'waiting' AND requested_kind IS ? AND requested_decider IS ? AND requested_text IS ? AND requested_resolvable_by IS ? THEN wait_hold_id ELSE NULL END WHERE id = ?`,
			string(st.State), st.Summary, rk, rd, rt, rb, totalMicros, now, endedAt, string(st.State), rk, rd, rt, rb, c.Existing.ID); err != nil {
			return err
		}
		if endedAt != nil {
			// 契約の実行が終わったら、確かめ待ちの中身は要らない（確かめの対象は終わっていない実行だけ）。
			if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL WHERE id = ?`, c.Existing.ID); err != nil {
				return err
			}
		}
	}
	facts, err := encodeContractFacts(st.Facts)
	if err != nil {
		return err
	}
	var opts any
	if ra := st.RequestedAction; ra != nil && len(ra.Options) > 0 {
		b, err := json.Marshal(ra.Options)
		if err != nil {
			return err
		}
		opts = string(b)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE contract_run SET facts = ?, requested_options = ?, content_ref = ? WHERE id = ?`,
		facts, opts, nullableTextColumn(reviewContentRef(st.Artifacts)), rowID)
	if err != nil {
		return err
	}
	if err := writeContractFlowAndSteps(ctx, tx, rowID, st); err != nil {
		return err
	}
	return countUnmetFacts(ctx, tx, c, st, rowID, report)
}

// writeContractFlowAndSteps は contract_run.flow と、委譲の実行のステップの行（contract_run_step）を出力と同じ中身にする。
// 委譲の実行の ID と seq を鍵に足すか上書きし、出力に無い seq の行は消す。steps が無い（nil）ときはステップの行に触らない。
func writeContractFlowAndSteps(ctx context.Context, tx *sql.Tx, rowID int64, st *ContractStatus) error {
	var flow any
	if st.Flow != nil {
		flow = *st.Flow
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET flow = ? WHERE id = ?`, flow, rowID); err != nil {
		return err
	}
	if st.Steps == nil {
		return nil
	}
	if len(st.Steps) == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM contract_run_step WHERE contract_run_id = ?`, rowID)
		return err
	}
	seqs := make([]any, 0, len(st.Steps)+1)
	seqs = append(seqs, rowID)
	marks := ""
	for _, sp := range st.Steps {
		models, err := json.Marshal(append([]string{}, sp.ModelsUsed...))
		if err != nil {
			return err
		}
		var cost any
		if sp.CostUSD != nil {
			cost = usdToMicros(*sp.CostUSD)
		}
		unknown := 0
		if sp.CostUnknown {
			unknown = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used, effort)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (contract_run_id, seq) DO UPDATE SET step = excluded.step, kind = excluded.kind, attempt = excluded.attempt,
				state = excluded.state, outcome = excluded.outcome, started_at = excluded.started_at, ended_at = excluded.ended_at,
				cost_usd = excluded.cost_usd, cost_unknown = excluded.cost_unknown, model = excluded.model, models_used = excluded.models_used, effort = excluded.effort`,
			rowID, sp.Seq, sp.Step, sp.Kind, sp.Attempt, sp.State, stringPtrColumn(sp.Outcome), sp.StartedAt, stringPtrColumn(sp.EndedAt),
			cost, unknown, stringPtrColumn(sp.Model), string(models), stringPtrColumn(sp.Effort)); err != nil {
			return err
		}
		seqs = append(seqs, sp.Seq)
		marks += ", ?"
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM contract_run_step WHERE contract_run_id = ? AND seq NOT IN (`+strings.TrimPrefix(marks, ", ")+`)`, seqs...)
	return err
}

// encodeContractFacts は facts を JSON の文字列にする（nil は NULL）。
func encodeContractFacts(f map[string]string) (any, error) {
	if f == nil {
		return nil, nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// reviewContentRef はレビュー待ちの中身の識別を返す。成果物のうち kind が commit で role の無い要素の ref が
// ちょうど 1 つのときだけ。role が付いた commit（integration など）は数えない。0 件・2 件以上は空。
func reviewContentRef(arts []ContractArtifact) string {
	ref, n := reviewContentRefs(arts)
	if n != 1 {
		return ""
	}
	return ref
}

// reviewContentRefs は中身の識別の候補（kind が commit で role の無い成果物）の数と、最後に見つけた ref を返す。
func reviewContentRefs(arts []ContractArtifact) (ref string, n int) {
	for _, a := range arts {
		if a.Kind == "commit" && a.Role == "" {
			ref = a.Ref
			n++
		}
	}
	return ref, n
}

// afterContractSucceeded は succeeded を読んだときの記録（保留の間に読んだ印と成果物の取り込み）。
func (s *Store) afterContractSucceeded(ctx context.Context, tx *sql.Tx, c contractCall, st *ContractStatus, report *ContractRunReport) error {
	if c.ReleaseStage {
		return nil // 反映の段が読んだ成果物は、課題の成果物の記録へ取り込まない（完了の検証が読む直前の委譲の実行の成果物を変えない）
	}
	// 課題が保留（awaiting_human）の間に succeeded を読んだ印。答えの後の周で、start も resume も呼ばずに検証中へ進める。
	// 統合先を使う課題には付けない（SubmitHeldContractSuccesses が扱わず、印が残ると実行中と数え続けて止まるため。終わった実行のタスクは委譲の段が委譲し直す）。
	if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 1 WHERE challenge_id = ? AND external_run_id = ?
		AND EXISTS (SELECT 1 FROM challenge WHERE id = ? AND status = 'awaiting_human' AND COALESCE(uses_integration, 0) = 0)`, c.ChallengeIDInt, st.RunID, c.ChallengeIDInt); err != nil {
		return err
	}
	prs, err := importContractArtifacts(ctx, tx, c.RunIDInt, st.Artifacts, s.currentTime())
	report.PRs = append(report.PRs, prs...)
	return err
}

// importContractArtifacts は契約の状態の artifacts のうち取り込む種類（pr・branch・commit）を、run（runInt）の成果物として
// 記録する（成果物の取り込みはここ 1 か所）。取り込んだ PR の参照を返す。
func importContractArtifacts(ctx context.Context, tx *sql.Tx, runInt int64, arts []ContractArtifact, verifiedAt time.Time) ([]string, error) {
	var prs []string
	for _, a := range arts {
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
		if _, err := insertRunArtifact(ctx, tx, runInt, insertRunArtifactInput{Kind: kind, Ref: a.Ref, Role: a.Role, VerifiedAt: &verifiedAt}); err != nil {
			return nil, err
		}
		if kind == artifactKindPR {
			prs = append(prs, a.Ref)
		}
	}
	return prs, nil
}

// PollContractRuns は終わっていない契約の run（contract_run）の状態を、宣言の commands.status で読んで記録する。
// 読むたびに run を 1 つ記録し、費用は契約の累計との差分だけを記録する（何度読んでも二重に数えない）。
// 結果は contract_run の古い順。1 件の読みが失敗しても（結果が succeeded でなくても）残りを読む。
func (s *Store) PollContractRuns(ctx context.Context, in ContractPollInput) ([]ContractRunReport, error) {
	if in.Client == nil || in.AgentDecl == nil || in.ConnDecl == nil {
		return nil, ErrValidation
	}
	var only *int64
	if in.ChallengeID != nil {
		n, ok := parseChallengeID(*in.ChallengeID)
		if !ok {
			return nil, ErrValidation
		}
		only = &n
	}
	// 保留にし損ねた課題の掃引が失敗しても、その失敗を記録して状態の読みを続ける（1 件の失敗で読み全体を止めない）。
	var firstErr error
	if _, err := s.holdUnrecordedContractStops(ctx, in.AgentDecl); err != nil {
		firstErr = err
	}
	// 保留の間に成功していた課題（オーナーが答えて着手中へ戻ったもの）を、start も resume も呼ばずに検証中へ進める。
	if _, err := s.SubmitHeldContractSuccesses(ctx, only); err != nil {
		firstErr = firstErrOr(firstErr, err)
	}
	var rows []*contractRunRow
	bindings := map[*contractRunRow]*sourceBinding{}
	planVersions := map[int64]*int64{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		// 読むのはタスクの実行と、着手中・検証中・保留中の課題の上げる依頼（kind が reflect。保留中も状態・費用を読み続ける。受け取りは保留が解けてから）で、課題が完了でないものだけ（完了の課題の実行と
		// 反映の依頼は反映の段が読む）と、取り下げでない課題の下書きづくりの実行（kind が spec。分類済・人間対応待ちでも読む）。
		q := contractRunSelect + ` WHERE ended_at IS NULL AND challenge_id NOT IN (SELECT id FROM challenge WHERE status = 'done')
			AND (kind = 'task' OR (kind = 'spec' AND challenge_id NOT IN (SELECT id FROM challenge WHERE status = 'withdrawn')) OR (kind = 'reflect' AND (challenge_id IN (SELECT id FROM challenge WHERE status IN ('in_progress', 'verifying', 'awaiting_human'))
				OR operation_id IN (SELECT id FROM operation WHERE state = 'closed' AND closed_reason IN ('superseded', 'challenge_rejected')))))`
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
			if bindings[r], err = loadTaskSourceBinding(ctx, tx, r.ChallengeID, r.TaskID); err != nil {
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
	for _, row := range rows {
		// 同じタスク（タスクに結び付かない実行どうしを含む）に、終わっていない実行の記録（タスクの検証の実行など）が開いている間は、
		// 何も呼ばずにこの 1 件を飛ばす（次の機会にやり直す）。同じ課題の別のタスクの実行は飛ばさない。
		busy, err := s.sameTaskRunOpen(ctx, row)
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		if busy {
			continue
		}
		connector, err := findCLIConnector(in.ConnDecl, row.ConnectorID)
		var rep *ContractRunReport
		if err == nil {
			// 要らなくなった実行（反映の操作を閉じた上げる依頼・取りやめにしたタスクの実行・作り直した計画より前の上げる依頼）は、
			// 状態を読まずに取り消す（保留中の課題でも取り消す）。
			if why, cerr := s.obsoleteContractRun(ctx, row); cerr != nil {
				err = cerr
			} else if why != "" {
				rep, err = s.cancelObsoleteContractRun(ctx, in, row, connector, bindings[row], planVersions[row.ChallengeID], cycleIDInt, why)
			} else {
				rep, err = s.pollOne(ctx, in, row, connector, bindings[row], planVersions[row.ChallengeID], cycleIDInt)
			}
			if err == nil && rep == nil {
				continue // 検証中のレビュー待ちは読まない（pendingReviewReply）
			}
		}
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			if !errors.Is(err, ErrStoreError) {
				// 読めなかった（宣言の接続ツールが無い・差し込みを埋められない等）ことを、報告として見えるようにする。
				reports = append(reports, ContractRunReport{ChallengeID: formatChallengeID(row.ChallengeID), ExternalRunID: row.ExternalRunID,
					Result: RunResultErrored, State: row.State, Summary: row.Summary, Error: err.Error()})
			}
			continue
		}
		reports = append(reports, *rep)
	}
	return reports, firstErr
}

// sameTaskRunOpen は、契約の実行 row と同じタスク（row がタスクに結び付かなければ、課題のタスクに結び付かない実行）に、
// 終わっていない実行の記録（run）があるか（insertRun が止める重なりと同じ条件）。
func (s *Store) sameTaskRunOpen(ctx context.Context, row *contractRunRow) (bool, error) {
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		// insertRun が止める重なりと同じ条件にする（タスクに結び付く行は、同じタスクとタスクに結び付かない実行。タスクに結び付かない行は、
		// 課題のどの終わっていない実行とも）。
		q := `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND result IS NULL`
		args := []any{row.ChallengeID}
		if row.TaskID != nil {
			q += ` AND (task_id = ? OR task_id IS NULL)`
			args = append(args, *row.TaskID)
		}
		return tx.QueryRowContext(ctx, q, args...).Scan(&n)
	})
	return n > 0, classifyReadWriteErr(err)
}

// reclaimedContractStartWhere は、回収された（interrupted）か回収以外で失敗した契約の start の run で、まだ保留に
// 写していないものを選ぶ条件（run を r、課題を c と別名にした副問い合わせの本体）。start の run は、契約の run（contract_run）を
// 作る前に回収されると contract_run を持たない。下書きづくりの開始（予算の枠 spec）は選ばない（下書きづくりの経路が扱う）。status の run との区別は「その課題の、先に始まった契約の run が
// その run の開始時点で終わっていなかった」ことで行う。終わりと同じ時刻（ミリ秒）の run は start の run として扱う:
// 契約の run を終わらせるのは成功した契約のコマンドの run（start・status・resume・cancel）で、成功以外の run は
// 終わらせない。この条件は成功以外の run だけを選ぶから。run のタスクを進めてよくなければ選ばない（述語 taskMayProceedSQL。
// 選ぶのは、課題が着手中か、タスクを止める保留だけが開いた人間対応待ちで、そのタスクに開いている保留が無いとき。
// 課題を止める保留・そのタスクの保留が開いている・取り下げ・完了は選ばない）。
var reclaimedContractStartWhere = `r.kind = 'contract' AND r.budget_bucket <> 'spec' AND r.operation_id IS NULL AND r.result IS NOT NULL AND r.result <> 'succeeded' AND ` + taskMayProceedSQL("c", "r.task_id") + `
	AND NOT EXISTS (SELECT 1 FROM contract_run k WHERE k.start_run_id = r.id)
	AND NOT EXISTS (SELECT 1 FROM contract_run k WHERE k.challenge_id = r.challenge_id AND COALESCE(k.task_id, 0) = COALESCE(r.task_id, 0) AND k.start_run_id < r.id
		AND (k.ended_at IS NULL OR k.ended_at > r.started_at))
	AND NOT EXISTS (SELECT 1 FROM hold h WHERE h.run_id = r.id)`

// unheldContractStopWhere は、契約の run（k）が failed・cancelled で終わったのに、その後に保留を作れていない
// （記録と保留が別のトランザクションなので、間でプロセスが止まると起こる）課題を選ぶ条件。
// 後の保留は、そのタスクに結び付くもの（か、タスクに結び付かないもの）だけを数える（ほかのタスクの保留は、このタスクの作り損ねを隠さない）。
// 開いたまま（ended_at が NULL）の行は選ばない（状態の確認が終わった記録にした後で拾う。NULL との比較は成り立たず、毎回選んでしまう）。
// 取りやめにしたタスクの実行は、flywheel が取り消した（計画の作り直し・取り下げ）ので、保留にし損ねたものと数えない。
var unheldContractStopWhere = `k.kind = 'task' AND k.state IN ('failed', 'cancelled') AND ` + taskMayProceedSQL("c", "k.task_id") + `
	AND k.ended_at IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM hold h WHERE h.challenge_id = k.challenge_id AND h.raised_at >= k.ended_at
		AND (h.task_id IS NULL OR k.task_id IS NULL OR h.task_id = k.task_id))
	AND NOT EXISTS (SELECT 1 FROM task t WHERE t.id = k.task_id AND t.status IN ('cancelled', 'done'))`

// HoldUnrecordedContractStops は、保留にし損ねた課題を保留にする。次の 2 つ（どちらも課題が着手中のときだけ）。
//   - 契約の start の最中に flywheel の run が回収された（呼んだプロセスが止まって別のプロセスが回収した場合など）のに、
//     まだ保留にしていない。外部の run が残っているかもしれないことを問いに書く。
//   - 契約の run が failed・cancelled で終わったのに、保留を作れていない。
//
// 回収は run を interrupted で閉じるだけなので、PollContractRuns の先頭と、周から呼ぶ側が呼ぶ。
// 保留にした課題の数を返す。
// 宣言を受け取らないので、止まり方の保留は宣言の既定（supervisor 真）で作る。サイクルの経路は PollContractRuns（宣言を渡す）を使う。
func (s *Store) HoldUnrecordedContractStops(ctx context.Context) (int, error) {
	return s.holdUnrecordedContractStops(ctx, nil)
}

// holdUnrecordedContractStops は HoldUnrecordedContractStops の、宣言の agent（保留の止まり方と宣言の supervisor。nil は宣言の既定と同じ扱い）を渡す版。
func (s *Store) holdUnrecordedContractStops(ctx context.Context, agent *AgentDeclaration) (int, error) {
	type target struct {
		runID, challengeID int64
		question           string
	}
	var targets []target
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.id, r.challenge_id, r.result, COALESCE(r.error, '') FROM run r JOIN challenge c ON c.id = r.challenge_id
			WHERE `+reclaimedContractStartWhere+` ORDER BY r.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t target
			var result, errSummary string
			if err := rows.Scan(&t.runID, &t.challengeID, &result, &errSummary); err != nil {
				_ = rows.Close()
				return err
			}
			if RunResult(result) == RunResultInterrupted {
				t.question = contractReclaimedQuestion("", "")
			} else {
				t.question = contractStartFailedQuestion(RunResult(result), errSummary, "", "")
			}
			targets = append(targets, t)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		rows, err = tx.QueryContext(ctx, `SELECT k.start_run_id, k.challenge_id, k.state, k.external_run_id, k.summary,
				EXISTS (SELECT 1 FROM hold h2 WHERE h2.challenge_id = k.challenge_id AND h2.raised_at < k.ended_at
					AND (h2.answered_at IS NULL OR h2.answered_at >= k.ended_at)
					AND (h2.closed_at IS NULL OR h2.closed_at >= k.ended_at))
			FROM contract_run k JOIN challenge c ON c.id = k.challenge_id WHERE `+unheldContractStopWhere+` ORDER BY k.id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t target
			var state, ext, summary string
			var duringHold bool
			if err := rows.Scan(&t.runID, &t.challengeID, &state, &ext, &summary, &duringHold); err != nil {
				return err
			}
			t.question = contractStopQuestionDuringHold(ContractState(state), ext, summary, duringHold)
			targets = append(targets, t)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	// 1 件の保留が失敗しても残りを保留にする（最初の失敗を返す）。
	held := 0
	var firstErr error
	for _, t := range targets {
		ok, err := s.holdReclaimedContractStart(ctx, formatRunID(t.runID), formatChallengeID(t.challengeID), t.question, agent)
		if err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		if ok {
			held++
		}
	}
	return held, firstErr
}

func firstErrOr(cur, next error) error {
	if cur != nil {
		return cur
	}
	return next
}

// runRecordingSection は run の記録と許可の数の解放を、許可の数えと同時に起きない 1 つの区間にする（実装は admission）。
// 区間の中で行う記録は StartContractDelegation 自身がちょうど 1 回だけ行う。
type runRecordingSection interface {
	Lock()
	Unlock()
	// MarkInsertedLocked は、区間の中で run の記録が確定したことを知らせる（許可の数を解放する）。
	MarkInsertedLocked()
}

// recordContractStartRun は sec の区間（nil なら区間なし）の中で insert を 1 回呼び、成功したら確定を知らせる。
func (s *Store) recordContractStartRun(sec runRecordingSection, insert func() (int64, error)) (int64, error) {
	if sec == nil {
		return insert()
	}
	sec.Lock()
	defer sec.Unlock()
	id, err := insert()
	if err == nil {
		sec.MarkInsertedLocked()
	}
	return id, err
}

// operationFlow は接続ツールの操作 operation の flow（form: flow の操作が実行するフローの名前）を返す。無ければ空。
func operationFlow(c *Connector, operation string) string {
	for i := range c.Operations {
		if c.Operations[i].ID == operation {
			return c.Operations[i].Flow
		}
	}
	return ""
}

// taskStartFlow は委譲を始めるときに、始めるコマンドの最後の引数にするフローの名前を返す。計画の承認で記録した planned_flow があれば
// それを使い（承認の後に宣言の操作の flow を書き換えても変えない）、無い（NULL。計画を作るときにフローを選ぶ前に承認したタスク）なら、
// そのときの宣言の操作の flow を使う。
func taskStartFlow(c *Connector, task Task) string {
	if task.PlannedFlow != "" {
		return task.PlannedFlow
	}
	return operationFlow(c, task.Operation)
}

// holdPlannedFlowUnrunnable は、承認したフロー（task.planned_flow）で走れない（いまのタスクのリポジトリの委譲先が flow の形でない）
// ときの保留を立て、起動しないことを表す ErrValidation の誤りを返す。保留は既存の作り（理由なしの人間への保留。タスクを止める
// 保留として重ねられるときはタスクに結び付ける）で、新しい保留の種類は作らない。課題が着手中でも人間対応待ちでもなければ、保留は立てない。
func (s *Store) holdPlannedFlowUnrunnable(ctx context.Context, challengeID string, task Task, c *Connector) error {
	question := fmt.Sprintf("タスク %s は、計画の承認でフロー %q で進めると決めたが、リポジトリ %q の委譲先 %q はいま flow の形ではないので、承認したフローで走れない。"+
		"契約のコマンドは起動していない。宣言を flow の形へ戻すか、計画を作り直すか。",
		task.ID, task.PlannedFlow, task.Repo, c.ID)
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	taskInt := taskIntID(&task)
	_, herr := s.runTransition(context.WithoutCancel(ctx), runTransitionParams{
		actor: actor, channel: ChannelInvoker, verification: VerificationNone, id: challengeID, op: OpHold, preceding: NoStatus,
	}, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusInProgress && tc.current.Status != StatusAwaitingHuman {
			return errReconcileNotEligible
		}
		if taskInt > 0 {
			tc.taskID = &taskInt
		}
		scoped, err := taskStopOptions(ctx, tc, HoldOptionsInput{})
		if err != nil {
			return err
		}
		if tc.current.Status == StatusAwaitingHuman && scoped.Scope != HoldScopeTask {
			return errReconcileNotEligible // 人間対応待ちに重ねられるのは、タスクを止める保留だけ
		}
		_, err = s.insertHoldWithOccurrenceOptions(ctx, tc, question, holdReasonNone, "", scoped)
		return err
	})
	if herr != nil && !errors.Is(herr, ErrInvalidTransition) && !errors.Is(herr, ErrTerminalState) && !errors.Is(herr, errReconcileNotEligible) {
		return herr
	}
	return fmt.Errorf("%w: task %s has planned flow %q but the connector %q is not a flow form; the contract command was not started", ErrValidation, task.ID, task.PlannedFlow, c.ID)
}
