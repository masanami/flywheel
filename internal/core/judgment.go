package core

// このファイルは #81（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §invoker の共通の規則・§結果の判別・§費用の記録・§アーキテクチャ決定
// 「1 つの run の流れ ①〜③」）が足す、判断の呼び出し 1 回の実行本体
// （Store.RunJudgment）を持つ。
//
// 「規則は core、入出力は invoker」（§機能全体の設計）に従い、このファイルは
// invoker の IF（JudgmentInvoker）と、その IF を使って①起動前の記録→②起動して
// 待つ（子を待つ間は書き込みロックを持たない。ingest.go の①〜③と同じ形）→
// ③結果と費用を記録する、という決定的な手順だけを持つ。子プロセスの起動・
// 結果の判別・費用の抽出は internal/invoker（規則を持たない）に置く。
//
// J1〜J5 それぞれの入力の組み立て・出力の閉集合の検査・課題の状態への写像
// （分類・計画への反映等）は #84・#85 の範囲であり、ここには無い。このファイルは
// 判断点に依らない汎用の「run を 1 回実行して記録する」層だけを提供する。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"syscall"
	"time"
)

// --- 公開エイリアス（run_store.go〈#78〉の閉集合をそのまま公開する） ---
//
// run_store.go は #78 の時点で core 内部（非公開）の型として閉集合を定義した。
// #81 は invoker・呼び出し元（将来の #84・#85・CLI）がこれらの値を扱えるよう、
// 型を複製せず公開エイリアスで再輸出する（二重管理を避ける）。

// RunResult は run.result の閉集合（§結果の判別の7値＋interrupted）。
type RunResult = runResult

// RunResult の8値。
const (
	RunResultLaunchFailed    RunResult = runResultLaunchFailed
	RunResultTimedOut        RunResult = runResultTimedOut
	RunResultMalformed       RunResult = runResultMalformed
	RunResultBudgetExhausted RunResult = runResultBudgetExhausted
	RunResultErrored         RunResult = runResultErrored
	RunResultInvalidOutput   RunResult = runResultInvalidOutput
	RunResultSucceeded       RunResult = runResultSucceeded
	RunResultInterrupted     RunResult = runResultInterrupted
)

// runResultValues は RunResult の閉集合（§結果の判別の 7 値＋interrupted。
// 仕様の列挙の順）。
var runResultValues = []RunResult{
	RunResultLaunchFailed,
	RunResultTimedOut,
	RunResultMalformed,
	RunResultBudgetExhausted,
	RunResultErrored,
	RunResultInvalidOutput,
	RunResultSucceeded,
	RunResultInterrupted,
}

// RunResultValues は RunResult の閉集合の写しを返す（CLI のテストが仕様の列挙と
// 双方向に照合するため。IngestOutcomeValues と同じ形）。
func RunResultValues() []RunResult {
	return append([]RunResult(nil), runResultValues...)
}

// CostSource は run.cost_source の閉集合（費用の出所）。
type CostSource = costSource

// CostSource の3値。
const (
	CostSourceReported CostSource = costSourceReported
	CostSourceDelta    CostSource = costSourceDelta
	CostSourceUnknown  CostSource = costSourceUnknown
)

// JudgmentPoint は判断点 J1〜J5 の閉集合。
type JudgmentPoint = judgmentPoint

// JudgmentPoint の5値。
const (
	JudgmentJ1 JudgmentPoint = judgmentJ1
	JudgmentJ2 JudgmentPoint = judgmentJ2
	JudgmentJ3 JudgmentPoint = judgmentJ3
	JudgmentJ4 JudgmentPoint = judgmentJ4
	JudgmentJ5 JudgmentPoint = judgmentJ5
)

// RunKind は run.kind（判断の呼び出しか委譲の起動か）の閉集合。
type RunKind = runKind

// RunKind の2値。
const (
	RunKindJudgment RunKind = runKindJudgment
	RunKindDelegate RunKind = runKindDelegate
)

// ErrRunInProgress は課題に既に終了していない run があることを表す
// （AC「同じ課題に対する2つのclassify --auto <C-ID>を並行して実行すると、runは
// 1つしか作られず、もう一方はrun_in_progressで終わる」。run_store.go の
// errActiveRunExists〈部分一意索引の違反の翻訳〉をそのまま公開する）。
var ErrRunInProgress = errActiveRunExists

// --- invoker の IF（規則は core、入出力は invoker） ---

// JudgmentLaunchInput は Store.RunJudgment が JudgmentInvoker へ渡す、
// 判断の呼び出し1回ぶんの起動情報（②の入力）。ストアのハンドルは含まない
// （internal/invoker はストアを import しない＝import の向き）。
type JudgmentLaunchInput struct {
	// SessionID は、この run が実際に使うセッションのID（小文字UUID）。
	// 新規セッションならRunJudgmentが①で採番した値、--resumeなら再開元の
	// runのsession_idそのもの（core が一意に決め、invoker には常にこの
	// 1つの値だけを渡す。self-review 指摘 round1: 以前はSessionIDと
	// ResumeSessionIDの2値を渡し、どちらを実際に使うかの判断をinvoker側に
	// 残していたため、core が記録するsession_idと実際にclaudeへ渡す
	// session_idが食い違いうる構造だった）。
	SessionID string
	// IsResume は、この run が既存のセッションの再開（--resume）であるかを
	// 表す（S1 では呼び出し元は使わないが、§結果の判別と費用「同じ
	// session_idを--resumeしたrunの費用は…」のACをcoreのテストで検証
	// できるよう、このチケットで実装しておく）。invoker はこの値を使って
	// 起動の引数の形（--session-id と --resume のどちらを使うか）を
	// 決めてよい（実際の引数の形は【仮定】。args.goを参照）。
	IsResume bool
	// Judgment は判断点（J1〜J5）。invoker（#84 以降の Sections を使う経路）が
	// この値から埋め込みの指示文（Instructions）を選ぶために使う。
	Judgment JudgmentPoint
	// Workspace は判断の呼び出しの作業ディレクトリ。
	Workspace string
	// RunDir は標準出力・標準エラー・渡した入力の保存先
	// （.flywheel/runs/<run の ID>/ の絶対パス）。
	RunDir string
	// OutputSchema は --json-schema に渡す、この run の出力スキーマ。
	OutputSchema []byte
	// MaxBudgetUSD は --max-budget-usd に渡す上限額（USD）。
	MaxBudgetUSD float64
	// TimeoutSec は1回の起動に置く時間の上限（秒）。
	TimeoutSec int
	// Stdin は標準入力にそのまま渡すバイト列（指示文＋区切りの行で囲んだ
	// データの区画。Sections が空のときだけ使う互換の経路。組み立ては
	// 呼び出し元〈#81 の時点のテスト等〉の責務で、invoker はその中身を
	// 解釈しない）。
	Stdin []byte
	// Sections は、標準入力に埋め込む外部由来の文字列のデータの区画の一覧
	// （#84。§機能全体の設計「規則は core、入出力は invoker」の決定: 標準入力の
	// 組み立て（instructions の埋め込み読み込み・区切りの行での囲い）は
	// internal/invoker.Launcher.InvokeJudgment が in.Judgment から
	// invoker.Instructions を引き、invoker.BuildStdin(instructions,
	// sections) で行う。core はここへ「判断点の種類」と「データの区画の
	// 一覧」を渡すだけで、指示文の中身・区切りの形式を知らない〈import の
	// 向きを保つ〉）。非空なら invoker はこちらを優先し、Stdin フィールドは
	// 無視する（互換: Sections が空のときだけ Stdin をそのまま使う）。
	Sections []JudgmentDataSection
}

// JudgmentDataSection は標準入力へ埋め込む、外部由来の文字列 1 件（ラベルと
// 本文）。internal/invoker.DataSection と同じ形だが、core はストアも
// invoker も import しないため独立した型として持つ（invoker 側がこの値を
// invoker.DataSection へ写す）。
type JudgmentDataSection struct {
	Label   string
	Content string
}

// JudgmentLaunchOutput は JudgmentInvoker が返す、判断の呼び出し1回の結果
// （②の出力。③で core が記録する）。Cost は「生の値」（返り値の
// total_cost_usd、無ければ nil）のままで、費用の出所ごとの最終額
// （reported/delta/unknown）は RunJudgment が直前の run の費用と突き合わせて
// 決める（invoker はストアを持たず、直前の run の費用を知らない）。
type JudgmentLaunchOutput struct {
	// Result は§結果の判別の表（invoker が上から評価して決める）。
	Result RunResult
	// SessionIDReturned は返り値の session_id（得られなければ空文字列）。
	SessionIDReturned string
	// RateLimited は§枠超過の先頭一致の判定結果。
	RateLimited bool
	// ReportedTotalCostUSD は返り値の total_cost_usd（無ければ nil）。
	ReportedTotalCostUSD *float64
	// StructuredOutput は structured_output の生の JSON（無ければ nil）。
	StructuredOutput []byte
	// ErrorSummary は run.error に記録する要約（無ければ空文字列）。
	ErrorSummary string
}

// JudgmentInvoker は core が定義する、判断の呼び出し1回の実行の IF
// （internal/invoker.Launcher が実装する。§機能全体の設計「規則は core、
// 入出力はinvoker」）。core のテストはこの IF の偽の実装（メモリ上の
// 固定の結果を返す実装）で検証する。
type JudgmentInvoker interface {
	// Available は起動できるか（PATH に claude があるか）を確かめる。
	// RunJudgment は①の書き込みトランザクションより前にこれを呼び、
	// エラーならそのまま返して何も記録・変更しない（§invoker の共通の規則
	// 「PATHにclaudeが無いときは、何も起動・変更せずにinvoker_unavailableで
	// 終わる」）。呼び出し元（CLI 層）はこのエラーを errors.Is で判別して
	// invoker_unavailable へ写す（写像自体は #84・#85 の範囲）。
	Available(ctx context.Context) error

	InvokeJudgment(ctx context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error)
}

// --- 汎用の run 実行（RunJudgment） ---

// defaultHeartbeatInterval は待つ間に heartbeat を更新する間隔の既定値
// （§invoker の共通の規則「60秒ごとに run の生存の記録〈heartbeat〉を
// 更新する」）。
const defaultHeartbeatInterval = 60 * time.Second

// defaultStaleAfter は heartbeat がこれより古く、プロセスが生きていなければ
// interrupted で閉じる閾値（§invoker の共通の規則「300秒より古く…」）。
const defaultStaleAfter = 300 * time.Second

// usdMicrosPerUnit は §クリティカル設計決定 1「金額はUSDの100万分の1を単位と
// する整数で持つ」の単位。
const usdMicrosPerUnit = 1_000_000

func usdToMicros(usd float64) int64 {
	return int64(math.Round(usd * usdMicrosPerUnit))
}

func microsToUSD(micros int64) float64 {
	return float64(micros) / usdMicrosPerUnit
}

// RunJudgmentInput は Store.RunJudgment の入力。
type RunJudgmentInput struct {
	// ChallengeID は対象の課題（"C-<n>"）。
	ChallengeID string
	// Judgment は判断点（J1〜J5）。
	Judgment JudgmentPoint
	// MaxBudgetUSD はこの run に渡す上限額（USD。0以下は ErrValidation）。
	MaxBudgetUSD float64
	// TimeoutSec は1回の起動の時間の上限（秒）。
	TimeoutSec int
	// Stdin は判断の呼び出しの標準入力（Sections が空のときだけ使う互換の経路。
	// JudgmentLaunchInput.Stdin と同じ規則）。
	Stdin []byte
	// Sections は標準入力に埋め込むデータの区画の一覧（#84。非空なら invoker が
	// これを優先する。JudgmentLaunchInput.Sections と同じ規則）。
	Sections []JudgmentDataSection
	// OutputSchema はこの判断点の出力スキーマ。
	OutputSchema []byte
	// Invoker は判断の呼び出しの実行者（internal/invoker.Launcher、または
	// テストの偽の実装）。nil は ErrValidation。
	Invoker JudgmentInvoker
	// ResumeFromRunID が非nilなら、その run の session_id へ --resume する
	// （§失敗・差し戻しの上限・§J5検証。S1の呼び出し元はまだこれを使わない）。
	ResumeFromRunID *string
	// CycleID が非nilなら、この run を周（cycle）へ紐づけ、起動の前に
	// §予算ガードの評価式（既消費額＋予約額＋評価額 ＞ 周の上限額）を①の
	// トランザクションの中で検査する（#83）。真なら run を作らず課題も
	// 変えずに ErrBudgetExceeded を返す（等号は起動してよい）。周が存在
	// しない・既に終了していれば ErrValidation。nil なら（#83 より前の
	// 呼び出し元と同じ）予算の評価を行わず、run の cycle_id は NULL のまま
	// 記録する。
	CycleID *string
}

// RunJudgmentResult は Store.RunJudgment の出力。
type RunJudgmentResult struct {
	RunID             string
	ChallengeID       string
	Result            RunResult
	RateLimited       bool
	CostUSD           float64
	CostSource        CostSource
	StructuredOutput  []byte
	SessionID         string
	SessionIDMismatch bool
}

// RunJudgment は判断の呼び出し1回を実行して記録する（§アーキテクチャ決定
// 「1つのrunの流れ」）:
//
//	① 書き込みトランザクションで、対象の課題を読み直し、session_id を採番して
//	   run を記録する（起動と記録を不可分にする。同じ課題に終了していない run
//	   があれば ErrRunInProgress）
//	② トランザクションの外で in.Invoker を呼んで待つ。待つ間、
//	   defaultHeartbeatInterval ごとに短い書き込みトランザクションで
//	   heartbeat を更新する（子を待つ間は書き込みロックを保持しない）
//	③ 書き込みトランザクションで、run の結果・費用を記録する
//
// 課題の状態への写像（J1〜J5の意味づけ）はここでは行わない（#84・#85の範囲）。
func (s *Store) RunJudgment(ctx context.Context, in RunJudgmentInput) (*RunJudgmentResult, error) {
	if in.Invoker == nil || in.MaxBudgetUSD <= 0 {
		return nil, ErrValidation
	}
	if !in.Judgment.valid() {
		return nil, ErrValidation
	}
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return nil, ErrNotFound
	}

	// invoker が起動できるか（PATHにclaudeがあるか）を、何も記録・変更する前に
	// 確かめる（AC「PATHにclaudeが無い環境でclassify --autoを実行すると、
	// 終了コード2・invoker_unavailableで終わり、ストアが変わらない」）。
	if err := in.Invoker.Available(ctx); err != nil {
		return nil, err
	}

	// 次にストアを開いたコマンドが中断した run を回収する、という規則を
	// RunJudgment の入口でも適用する（stale な「終了していない run」に阻まれて
	// 新しい起動ができなくなる事態を避ける）。
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	pid := int64(os.Getpid())
	now := s.currentTime()
	maxBudgetMicros := usdToMicros(in.MaxBudgetUSD)

	// self-review 指摘（round1, code-reviewer/design-reviewer 双方が
	// CONFIRMED）: 以前はここで常に新しい UUID を生成して run.session_id に
	// 記録していたが、--resume する run は実際には resumeSessionID を
	// claude へ渡す（invoker/args.go）ため、記録した session_id と実際に
	// 使った値が食い違い、claude が resumeSessionID をそのまま返すたびに
	// session_id_mismatch が誤って立っていた。resume するときは
	// resumeSessionID をそのまま session_id として記録する（新規に採番
	// しない）。
	var sessionID string
	var isResume bool
	var resumeFromRunIDInt *int64
	var prevReportedMicros *int64
	var cycleIDInt *int64
	var runIDInt int64

	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}

		if in.CycleID != nil {
			// 終了していない run を持つ課題は、予算の評価より先に
			// ErrRunInProgress にする（予算の不足と取り違えない）。
			active, err := loadActiveRunByChallengeID(ctx, tx, cid)
			if err != nil {
				return err
			}
			if active != nil {
				return errActiveRunExists
			}
			cyid, ok := parseCycleID(*in.CycleID)
			if !ok {
				return ErrValidation
			}
			// self-review: 整数（USDの100万分の1）で評価し、丸め誤差を避ける
			// （§クリティカル設計決定 1）。「＞」だけを拒否し、等号は起動してよい
			// （§予算ガード「起動の前に…評価し、真なら起動しない」の元の現行の
			// 評価式）。評価式は evalCycleBudgetTx（事前検査の checkCycleBudget と
			// 共有）。
			if err := evalCycleBudgetTx(ctx, tx, cyid, maxBudgetMicros); err != nil {
				return err
			}
			cycleIDInt = &cyid
		}

		if in.ResumeFromRunID != nil {
			rid, ok := parseRunID(*in.ResumeFromRunID)
			if !ok {
				return ErrValidation
			}
			prev, err := loadRunByID(ctx, tx, rid)
			if err != nil {
				return err
			}
			// self-review 指摘（round1, code-reviewer PLAUSIBLE）: 別の課題の
			// run を再開先に指定できてしまうと、無関係な run の session_id・
			// 費用が紛れ込む。同じ課題の run であることを確かめる。
			if prev == nil || prev.SessionID == "" || prev.ChallengeID != formatChallengeID(cid) {
				return ErrValidation
			}
			isResume = true
			sessionID = prev.SessionID
			resumeFromRunIDInt = &rid
			if prev.ReportedTotalCostUSD != nil {
				v := *prev.ReportedTotalCostUSD
				prevReportedMicros = &v
			}
		} else {
			sid, err := newLowercaseUUIDv4()
			if err != nil {
				return fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
			}
			sessionID = sid
		}

		row, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID:          cycleIDInt,
			Kind:             runKindJudgment,
			Judgment:         in.Judgment,
			ChallengeVersion: int64(ch.Version),
			SessionID:        sessionID,
			ResumedFromRunID: resumeFromRunIDInt,
			PID:              pid,
			Host:             host,
			HeartbeatAt:      now,
			StartedAt:        now,
			MaxBudgetUSD:     maxBudgetMicros,
			BudgetBucket:     budgetBucketJudgment,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("core: RunJudgment: malformed run id %q", row.ID)
		}
		runIDInt = n
		return nil
	})
	if err != nil {
		if errors.Is(err, errActiveRunExists) {
			return nil, ErrRunInProgress
		}
		return nil, classifyReadWriteErr(err)
	}

	runIDDisplay := formatRunID(runIDInt)

	launchIn := JudgmentLaunchInput{
		SessionID:    sessionID,
		IsResume:     isResume,
		Judgment:     in.Judgment,
		Workspace:    s.workspace,
		RunDir:       runDirPath(s.workspace, runIDDisplay),
		OutputSchema: in.OutputSchema,
		MaxBudgetUSD: in.MaxBudgetUSD,
		TimeoutSec:   in.TimeoutSec,
		Stdin:        in.Stdin,
		Sections:     in.Sections,
	}

	output, invokeErr := s.invokeWithHeartbeat(ctx, runIDInt, launchIn, in.Invoker)

	result := output.Result
	errSummary := output.ErrorSummary
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

	sessionIDReturned := output.SessionIDReturned
	mismatch := sessionIDReturned != "" && sessionIDReturned != sessionID
	finalSessionID := sessionID
	if mismatch {
		finalSessionID = sessionIDReturned
	}

	costMicros, costSource := computeRunCost(result, output.ReportedTotalCostUSD, isResume, prevReportedMicros, maxBudgetMicros)

	var reportedPtr *int64
	if output.ReportedTotalCostUSD != nil {
		v := usdToMicros(*output.ReportedTotalCostUSD)
		reportedPtr = &v
	}

	updateInput := updateRunEndInput{
		EndedAt:              s.currentTime(),
		Result:               result,
		RateLimited:          output.RateLimited,
		CostUSD:              &costMicros,
		CostSource:           costSource,
		ReportedTotalCostUSD: reportedPtr,
		Output:               string(output.StructuredOutput),
		Error:                errSummary,
	}

	// self-review 指摘（round1, code-reviewer PLAUSIBLE）: ③（結果の記録）は
	// 呼び出し元の ctx が取り消されていても必ず実行する。②で子プロセスを
	// 起動・終了させた（費用が発生しうる）以上、その結果の記録を ctx の
	// 取り消しで欠落させると、run が終了しないまま残り
	// （ErrRunInProgress）、300秒のinterrupted回収を待つまで課題が塞がれる。
	// context.WithoutCancel で「取り消し・締め切りを引き継がない」子
	// context にして、記録だけは常に完了させる。
	recordCtx := context.WithoutCancel(ctx)
	err = s.db.Write(recordCtx, func(tx *sql.Tx) error {
		if mismatch {
			if _, err := tx.ExecContext(recordCtx,
				`UPDATE run SET session_id = ?, session_id_mismatch = 1 WHERE id = ?`,
				sessionIDReturned, runIDInt,
			); err != nil {
				return err
			}
		}
		_, err := updateRunEnd(recordCtx, tx, runIDInt, updateInput)
		return err
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}

	return &RunJudgmentResult{
		RunID:             runIDDisplay,
		ChallengeID:       formatChallengeID(cid),
		Result:            result,
		RateLimited:       output.RateLimited,
		CostUSD:           microsToUSD(costMicros),
		CostSource:        costSource,
		StructuredOutput:  output.StructuredOutput,
		SessionID:         finalSessionID,
		SessionIDMismatch: mismatch,
	}, nil
}

// runDirPath は runID の run の保存先の絶対パス（.flywheel/runs/<runID>/）を
// 返す（§invoker の共通の規則「ワークスペースの.flywheel/runs/<runの ID>/に
// 保存する」）。ディレクトリの作成自体は internal/invoker が行う（RunDir は
// ただの計算済みパス）。
func runDirPath(workspace, runID string) string {
	return workspace + string(os.PathSeparator) + flywheelDirName + string(os.PathSeparator) + "runs" + string(os.PathSeparator) + runID
}

// invokeWithHeartbeat は invoker.InvokeJudgment を待つ間、
// defaultHeartbeatInterval（テストは s.heartbeatInterval で差し替え可能）
// ごとに短い書き込みトランザクションで heartbeat を更新する。子プロセスの
// 実行そのものは別 goroutine で行い、この関数は「子を待つ間、書き込み
// ロックを保持しない」（AC）ことを保証する: 保持するのは heartbeat の
// 更新中だけで、その間は極めて短い。
func (s *Store) invokeWithHeartbeat(ctx context.Context, runIDInt int64, in JudgmentLaunchInput, invoker JudgmentInvoker) (JudgmentLaunchOutput, error) {
	return s.invokeFnWithHeartbeat(ctx, runIDInt, func(ctx context.Context) (JudgmentLaunchOutput, error) {
		return invoker.InvokeJudgment(ctx, in)
	})
}

// invokeFnWithHeartbeat は invoke（子の起動）を別 goroutine で実行して待つ間、
// heartbeat を更新する（判断の呼び出しと委譲の起動が共有する）。
func (s *Store) invokeFnWithHeartbeat(ctx context.Context, runIDInt int64, invoke func(context.Context) (JudgmentLaunchOutput, error)) (JudgmentLaunchOutput, error) {
	interval := s.heartbeatInterval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}

	type invokeResult struct {
		out JudgmentLaunchOutput
		err error
	}
	done := make(chan invokeResult, 1)
	go func() {
		out, err := invoke(ctx)
		done <- invokeResult{out: out, err: err}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			return r.out, r.err
		case <-ticker.C:
			hbAt := s.currentTime()
			// heartbeat の更新の失敗は起動そのものを止めない（ベストエフォート。
			// 次に flywheel コマンドがストアを開いたときの interrupted への回収が
			// 最終的な安全網になる）。
			_ = s.db.Write(ctx, func(tx *sql.Tx) error {
				_, err := updateRunHeartbeat(ctx, tx, runIDInt, hbAt)
				return err
			})
		}
	}
}

// computeRunCost は§費用の記録の規則を実装する:
//
//   - launch_failed: 費用は0。Claudeを呼んでいないため出所は記録しない（""）
//   - timed_out・malformed・interrupted: 渡した上限額を消費したとみなし、出所は
//     unknown（fail-closed。結果のJSONを得られない）
//   - 返り値にtotal_cost_usdが無い: 渡した上限額、出所はunknown
//   - --resumeの再開（isResume）で、直前のrunの報告（prevReportedMicros）が
//     あれば、差分を費用とし出所はdelta。差が負になれば渡した上限額、出所は
//     unknown
//   - isResumeなのにprevReportedMicrosが無い（直前のrunの報告が取れな
//     かった）ときも、渡した上限額・出所unknown（fail-closed。self-review
//     指摘round1 CONFIRMED: 以前はこのケースをprevReportedMicros==nilの
//     分岐と区別せず「新しいセッション」と同じ扱いにしてしまい、返り値の
//     累計をそのままreportedとして記録していた＝§費用の記録の
//     「直前のrunの報告が無い…ときは…unknownにする」に反していた）
//   - それ以外（isResumeでない＝新しいセッション）は返り値のtotal_cost_usd
//     をそのまま費用とし、出所はreported
func computeRunCost(result RunResult, reportedUSD *float64, isResume bool, prevReportedMicros *int64, maxBudgetMicros int64) (costMicros int64, source CostSource) {
	if result == RunResultLaunchFailed {
		return 0, ""
	}
	switch result {
	case RunResultTimedOut, RunResultMalformed, RunResultInterrupted:
		return maxBudgetMicros, CostSourceUnknown
	}
	if reportedUSD == nil {
		return maxBudgetMicros, CostSourceUnknown
	}
	reportedMicros := usdToMicros(*reportedUSD)
	if !isResume {
		return reportedMicros, CostSourceReported
	}
	if prevReportedMicros == nil {
		return maxBudgetMicros, CostSourceUnknown
	}
	delta := reportedMicros - *prevReportedMicros
	if delta < 0 {
		return maxBudgetMicros, CostSourceUnknown
	}
	return delta, CostSourceDelta
}

// --- 中断した run の回収（interrupted への回収） ---

// ReapInterruptedRuns は、終了していない run のうち heartbeat が
// defaultStaleAfter（テストは s.staleAfter で差し替え可能）より古く、
// 記録したプロセスがそのホストで生きていないものを interrupted で閉じる
// （§invoker の共通の規則）。ListRuns（flywheel runs）・RunJudgment の入口が
// 呼ぶ。
func (s *Store) ReapInterruptedRuns(ctx context.Context) error {
	threshold := s.staleAfter
	if threshold <= 0 {
		threshold = defaultStaleAfter
	}
	now := s.currentTime()

	type candidate struct {
		id           int64
		pid          int64
		host         string
		maxBudgetUSD int64
	}
	var candidates []candidate
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, pid, host, heartbeat_at, max_budget_usd FROM run WHERE result IS NULL`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, pid, maxBudgetUSD int64
			var host, hbStr string
			if err := rows.Scan(&id, &pid, &host, &hbStr, &maxBudgetUSD); err != nil {
				return err
			}
			hb, err := parseTimestamp(hbStr)
			if err != nil {
				return err
			}
			if now.Sub(hb) <= threshold {
				continue
			}
			if isRunProcessAlive(host, pid) {
				continue
			}
			candidates = append(candidates, candidate{id: id, pid: pid, host: host, maxBudgetUSD: maxBudgetUSD})
		}
		return rows.Err()
	})
	if err != nil {
		return classifyReadWriteErr(err)
	}
	if len(candidates) == 0 {
		return nil
	}

	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, c := range candidates {
			row, err := loadRunByID(ctx, tx, c.id)
			if err != nil {
				return err
			}
			if row == nil || row.EndedAt != nil {
				continue // 冪等: 別の呼び出しが既に閉じていた
			}
			cost := c.maxBudgetUSD
			if _, err := updateRunEnd(ctx, tx, c.id, updateRunEndInput{
				EndedAt:     s.currentTime(),
				Result:      RunResultInterrupted,
				RateLimited: false,
				CostUSD:     &cost,
				CostSource:  CostSourceUnknown,
			}); err != nil {
				return err
			}
			// 中断した委譲の子がまだ作業ツリーで動いているかもしれないので、スロットを
			// idle に戻さず needs_attention にする（人が確かめて `slot clear` で戻す）。
			if row.SlotID != nil {
				if sid, ok := parseSlotID(*row.SlotID); ok {
					if cur, err := loadSlotByID(ctx, tx, sid); err != nil {
						return err
					} else if cur != nil && cur.State == slotStateBusy {
						if _, err := updateSlotState(ctx, tx, sid, slotStateNeedsAttention, nil,
							"the delegation run was interrupted; its child may still be working in this tree"); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
	return classifyReadWriteErr(err)
}

// isRunProcessAlive は run を記録したプロセス（host・pid）が生きているかを
// 判定する。host がこのプロセスのホスト名と異なる場合は確かめようがないため
// 安全側（生きている扱い＝回収しない）に倒す【仮定】: M3 S1 は単一ホストでの
// 運用を前提とし（分散スロットはS2以降）、別ホストの run を誤って
// interrupted にする方が実害が大きいと判断した。
func isRunProcessAlive(runHost string, pid int64) bool {
	currentHost, err := os.Hostname()
	if err != nil || runHost != currentHost {
		return true
	}
	if pid <= 0 {
		return false
	}
	err = syscall.Kill(int(pid), 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}

// newLowercaseUUIDv4 は RFC 4122 の version 4 の UUID を小文字表記で生成する
// （§invoker の共通の規則「session_id〈UUIDの小文字表記〉を採番し」）。
func newLowercaseUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.ToLower(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
}
