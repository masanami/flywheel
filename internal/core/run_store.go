// このファイルは #78（親要件チケット #77 §クリティカル設計決定 1・
// docs/features/m3-invoker-delegation.md）が足すマイグレーション 0004 の
// run・cycle・lock 表に対する、非公開の行レベルの読み書きを持つ。
//
// ここに置くのは #78 の完了条件（マイグレーションと、AC-168 の排他を検証する
// ための最小の読み書き）だけである。run 記録の公開 API 本体（invoker からの
// 呼び出し・起動と記録の不可分性）・heartbeat の巡回・interrupted への回収・
// 予算評価のロジックは、後続チケット（#79・#80 等）が別に作る。すべて
// 非公開（internal/cli・internal/invoker からは呼べない）。
//
// 閉集合（kind・judgment・result・cost_source・budget_bucket・cycle の
// result）は、0001・0002 の流儀どおりスキーマに CHECK 制約を付けず、この
// ファイルで検証する。

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

var (
	// errActiveRunExists は、課題に既に終了していない run があることを表す
	// （AC-168。部分一意索引 idx_run_active_challenge の違反と、タスクに結び付く run と結び付かない run の同時の未終了の確かめを翻訳する）。
	errActiveRunExists = errors.New("core: challenge already has an active run")

	// errActiveSlotRunExists は、スロットに既に終了していない run があることを
	// 表す（AC-280。部分一意索引 idx_run_active_slot の違反を翻訳する）。
	errActiveSlotRunExists = errors.New("core: slot already has an active run")
)

// --- ID の表示形 ---

var runIDPattern = regexp.MustCompile(`^R-[1-9][0-9]*$`)

func formatRunID(id int64) string { return fmt.Sprintf("R-%d", id) }

// parseRunID は s を run の内部整数 ID として解釈する。形式不正は ok=false。
func parseRunID(s string) (int64, bool) {
	if !runIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "R-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

var cycleIDPattern = regexp.MustCompile(`^Y-[1-9][0-9]*$`)

func formatCycleID(id int64) string { return fmt.Sprintf("Y-%d", id) }

// parseCycleID は s を cycle の内部整数 ID として解釈する。形式不正は ok=false。
func parseCycleID(s string) (int64, bool) {
	if !cycleIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "Y-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// --- 閉集合 ---

// runKind は run.kind（判断の呼び出しか委譲の起動か）。
type runKind string

const (
	runKindJudgment runKind = "judgment"
	runKindDelegate runKind = "delegate"
	// runKindContract は form: cli の接続ツールの契約のコマンド（start・status）1 回の実行。委譲の run と
	// 同じく課題・セッションに結び付くが、委譲の履歴の規則（連続失敗の数え・--resume の対象選び・
	// delegation_failed の発生）には入れない。セッション ID は run の一意のために採る偽の値。
	runKindContract runKind = "contract"
	// runKindPredict はコンフリクトの予測（flywheel の predict）の実行。新しく作る経路は無く、
	// 過去の行を読む（費用の集計・runs の表示）ためと、終わっていない行を終わらせる更新のために残す。
	runKindPredict runKind = "predict"
)

func (k runKind) valid() bool {
	switch k {
	case runKindJudgment, runKindDelegate, runKindContract, runKindPredict:
		return true
	}
	return false
}

// judgmentPoint は run.judgment（判断点 J1〜J6・J8・SV。J7 は無い。委譲は "" = NULL）。
type judgmentPoint string

const (
	judgmentJ1 judgmentPoint = "J1"
	judgmentJ2 judgmentPoint = "J2"
	judgmentJ3 judgmentPoint = "J3"
	judgmentJ4 judgmentPoint = "J4"
	judgmentJ5 judgmentPoint = "J5"
	judgmentJ6 judgmentPoint = "J6"
	judgmentJ8 judgmentPoint = "J8"
	// judgmentSV はスーパーバイザーの判断点（想定外の止まりへの対処。区切り S4b-2）。課題を持つ。
	judgmentSV judgmentPoint = "SV"
	// judgmentJ9 は 2 つの課題を同時に進めてよいかの判断（J7 は無い）。新しく呼ぶ経路は無く、
	// 過去の run を読むためだけに残す。課題を持たない（J8 と同じ）。
	judgmentJ9 judgmentPoint = "J9"
)

// hasNoChallenge は j が課題を持たない判断（J8・J9）かを返す。
func (j judgmentPoint) hasNoChallenge() bool { return j == judgmentJ8 || j == judgmentJ9 }

// valid は j が J1〜J6・J8 のいずれかであるかを返す（"" には false。空許容は
// 呼び出し側が kind に応じて判定する）。
func (j judgmentPoint) valid() bool {
	switch j {
	case judgmentJ1, judgmentJ2, judgmentJ3, judgmentJ4, judgmentJ5, judgmentJ6, judgmentJ8, judgmentSV, judgmentJ9:
		return true
	}
	return false
}

// runResult は run.result（§結果の判別の 7 値＋interrupted。終了していない
// run は "" = NULL）。
type runResult string

const (
	runResultLaunchFailed    runResult = "launch_failed"
	runResultTimedOut        runResult = "timed_out"
	runResultMalformed       runResult = "malformed"
	runResultBudgetExhausted runResult = "budget_exhausted"
	runResultErrored         runResult = "errored"
	runResultInvalidOutput   runResult = "invalid_output"
	runResultSucceeded       runResult = "succeeded"
	runResultInterrupted     runResult = "interrupted"
)

func (r runResult) valid() bool {
	switch r {
	case runResultLaunchFailed, runResultTimedOut, runResultMalformed, runResultBudgetExhausted,
		runResultErrored, runResultInvalidOutput, runResultSucceeded, runResultInterrupted:
		return true
	}
	return false
}

// costSource は run.cost_source（費用の出所）。
type costSource string

const (
	costSourceReported costSource = "reported"
	costSourceDelta    costSource = "delta"
	costSourceUnknown  costSource = "unknown"
)

func (c costSource) valid() bool {
	switch c {
	case costSourceReported, costSourceDelta, costSourceUnknown:
		return true
	}
	return false
}

// budgetBucket は run.budget_bucket（予算の枠）。
type budgetBucket string

const (
	budgetBucketJudgment budgetBucket = "judgment"
	budgetBucketImpl     budgetBucket = "impl"
	budgetBucketReview   budgetBucket = "review"
	budgetBucketPredict  budgetBucket = "predict"
)

func (b budgetBucket) valid() bool {
	switch b {
	case budgetBucketJudgment, budgetBucketImpl, budgetBucketReview, budgetBucketPredict:
		return true
	}
	return false
}

// decider は run.decider（その run の意思決定の主体）。
type decider string

const (
	deciderHuman  decider = "human"
	deciderParent decider = "parent"
	deciderChild  decider = "child"
)

func (d decider) valid() bool {
	switch d {
	case deciderHuman, deciderParent, deciderChild:
		return true
	}
	return false
}

// cycleResult は cycle.result（周の終了理由。終了していない周は "" = NULL）。
type cycleResult string

const (
	cycleResultCompleted   cycleResult = "completed"
	cycleResultAborted     cycleResult = "aborted"
	cycleResultInterrupted cycleResult = "interrupted"
)

func (c cycleResult) valid() bool {
	switch c {
	case cycleResultCompleted, cycleResultAborted, cycleResultInterrupted:
		return true
	}
	return false
}

// --- run ---

// runRow は run 表の 1 行（§クリティカル設計決定 1 の採用案の列挙どおり）。
// ID・CycleID・ResumedFromRunID は表示形（"R-<n>"・"Y-<n>"）。金額は USD の
// 100 万分の 1 を単位とする整数（【仮定】）。ポインタ型は NULL 可の列
// （nil = NULL）。Judgment・Result・CostSource は "" が NULL を表す
// （source_binding.go と同じ規約）。
type runRow struct {
	ID                   string
	CycleID              *string
	Kind                 runKind
	Judgment             judgmentPoint
	ChallengeID          string
	ChallengeVersion     int64
	PlanVersion          *int64
	SessionID            string
	SessionIDMismatch    bool
	ResumedFromRunID     *string
	PID                  int64
	Host                 string
	HeartbeatAt          time.Time
	StartedAt            time.Time
	EndedAt              *time.Time
	Result               runResult
	RateLimited          bool
	MaxBudgetUSD         int64
	BudgetBucket         budgetBucket
	CostUSD              *int64
	CostSource           costSource
	ReportedTotalCostUSD *int64
	Output               string
	Error                string
	// 0005 で足した列。SlotID は表示形（"SL-<n>"）。Decider は "" が NULL、
	// DeciderRow・Repo は nil / "" が NULL。Repo は委譲の run・契約のコマンドの run（どちらも結び付くタスクの repo）と過去の predict の run が持つ。
	// predict の run は ChallengeID・SessionID が "" 、ChallengeVersion が 0（NULL）になる。
	SlotID     *string
	Decider    decider
	DeciderRow *int64
	Repo       string
	// FlywheelVersion は run を作った flywheel の版（0011。"" が NULL）。
	FlywheelVersion string
	// TaskID は run が結び付くタスクの表示形（"T-<n>"。0026。nil が NULL＝課題の単位の実行と過去の行）。
	TaskID *string
}

const runSelectColumns = `SELECT id, cycle_id, kind, judgment, challenge_id, challenge_version, plan_version,
	session_id, session_id_mismatch, resumed_from_run_id, pid, host, heartbeat_at, started_at, ended_at,
	result, rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source, reported_total_cost_usd,
	output, error, slot_id, decider, decider_row, repo, flywheel_version, task_id FROM run`

// scanRunRow は 1 行分の run を Scan して runRow へ変換する。
func scanRunRow(row interface{ Scan(dest ...any) error }) (*runRow, error) {
	var (
		id, pid                                         int64
		challengeID, challengeVersion                   sql.NullInt64
		cycleID, resumedFromRunID, planVersion, costUSD sql.NullInt64
		reportedTotalCostUSD                            sql.NullInt64
		slotID, deciderRow, taskID                      sql.NullInt64
		kind, judgment, sessionID, host, result         string
		costSourceStr, deciderStr, repo                 string
		flywheelVersion                                 string
		budgetBucketStr, output, errStr                 sql.NullString
		heartbeatAtStr, startedAtStr                    string
		endedAtStr                                      sql.NullString
		sessionIDMismatch, rateLimited                  bool
		maxBudgetUSD                                    int64
	)
	if err := row.Scan(
		&id, &cycleID, &kind, &nullString{&judgment}, &challengeID, &challengeVersion, &planVersion,
		&nullString{&sessionID}, &sessionIDMismatch, &resumedFromRunID, &pid, &host, &heartbeatAtStr, &startedAtStr, &endedAtStr,
		&nullString{&result}, &rateLimited, &maxBudgetUSD, &budgetBucketStr, &costUSD, &nullString{&costSourceStr}, &reportedTotalCostUSD,
		&output, &errStr, &slotID, &nullString{&deciderStr}, &deciderRow, &nullString{&repo}, &nullString{&flywheelVersion}, &taskID,
	); err != nil {
		return nil, err
	}

	heartbeatAt, err := parseTimestamp(heartbeatAtStr)
	if err != nil {
		return nil, err
	}
	startedAt, err := parseTimestamp(startedAtStr)
	if err != nil {
		return nil, err
	}
	var endedAt *time.Time
	if endedAtStr.Valid {
		t, err := parseTimestamp(endedAtStr.String)
		if err != nil {
			return nil, err
		}
		endedAt = &t
	}

	r := &runRow{
		ID:                formatRunID(id),
		Kind:              runKind(kind),
		Judgment:          judgmentPoint(judgment),
		ChallengeVersion:  challengeVersion.Int64,
		SessionID:         sessionID,
		SessionIDMismatch: sessionIDMismatch,
		PID:               pid,
		Host:              host,
		HeartbeatAt:       heartbeatAt,
		StartedAt:         startedAt,
		EndedAt:           endedAt,
		Result:            runResult(result),
		RateLimited:       rateLimited,
		MaxBudgetUSD:      maxBudgetUSD,
		BudgetBucket:      budgetBucket(budgetBucketStr.String),
		CostSource:        costSource(costSourceStr),
		Output:            output.String,
		Error:             errStr.String,
		Decider:           decider(deciderStr),
		Repo:              repo,
		FlywheelVersion:   flywheelVersion,
	}
	if challengeID.Valid {
		r.ChallengeID = formatChallengeID(challengeID.Int64)
	}
	if slotID.Valid {
		v := formatSlotID(slotID.Int64)
		r.SlotID = &v
	}
	if taskID.Valid {
		v := formatTaskID(taskID.Int64)
		r.TaskID = &v
	}
	if deciderRow.Valid {
		v := deciderRow.Int64
		r.DeciderRow = &v
	}
	if cycleID.Valid {
		v := formatCycleID(cycleID.Int64)
		r.CycleID = &v
	}
	if planVersion.Valid {
		v := planVersion.Int64
		r.PlanVersion = &v
	}
	if resumedFromRunID.Valid {
		v := formatRunID(resumedFromRunID.Int64)
		r.ResumedFromRunID = &v
	}
	if costUSD.Valid {
		v := costUSD.Int64
		r.CostUSD = &v
	}
	if reportedTotalCostUSD.Valid {
		v := reportedTotalCostUSD.Int64
		r.ReportedTotalCostUSD = &v
	}
	return r, nil
}

// nullString は sql.NullString を経由して *string 相当の列（"" が NULL を
// 表す非公開の内部センチネル）を読む Scanner。
type nullString struct{ dest *string }

func (n *nullString) Scan(src any) error {
	var ns sql.NullString
	if err := ns.Scan(src); err != nil {
		return err
	}
	*n.dest = ns.String
	return nil
}

// insertRunInput は insertRun の入力。ChallengeID は呼び出し側が別引数
// （challengeID int64）で渡す（sourceBinding.go の insertSourceBinding と
// 同じ形）。ended_at・result・cost 系・output・error は NULL のまま挿入する
// （終了時にしか決まらない列。updateRunEnd が後で埋める）。
type insertRunInput struct {
	// Reflect は、反映の操作を持たない反映の依頼（上げる依頼）の run にも run.reflect の印を付けるとき真（反映の操作を持つ run は常に付く）。
	Reflect          bool
	CycleID          *int64
	Kind             runKind
	Judgment         judgmentPoint
	ChallengeVersion int64
	PlanVersion      *int64
	SessionID        string
	ResumedFromRunID *int64
	PID              int64
	Host             string
	HeartbeatAt      time.Time
	StartedAt        time.Time
	MaxBudgetUSD     int64
	BudgetBucket     budgetBucket
	// 0005 で足した入力。SlotID は slot の内部整数 ID。Decider と DeciderRow は
	// 両方指定するか両方省略する（DeciderRow は 1〜5）。
	SlotID     *int64
	Decider    decider
	DeciderRow *int64
	// TaskID は run が結び付くタスクの内部整数 ID（0026。nil は NULL）。
	TaskID *int64
	// Repo は run が対象にするリポジトリの名前（"" は NULL）。委譲の run と契約のコマンドの run は、結び付くタスクの repo を書く。
	Repo string
	// OperationID は、反映の操作のために呼んだ契約のコマンドの run の印（反映の操作の内部整数 ID。0028。nil は委譲の段の run）。
	OperationID *int64
	// TaskLess が真のとき、タスクに結び付かない run（TaskID が nil）の挿入は、タスクに結び付かない終わっていない run とだけ重ならない
	// （別のタスクの run とは重ならない。J2 の「足す」の形だけが使う）。
	TaskLess bool
}

func (in insertRunInput) valid() bool {
	if !in.Kind.valid() || !in.BudgetBucket.valid() {
		return false
	}
	// 新しく作る run には、過去の記録を読むためだけに残した値（predict・J9）を受け付けない。
	if in.Kind == runKindPredict || in.Judgment == judgmentJ9 || in.BudgetBucket == budgetBucketPredict {
		return false
	}
	switch in.Kind {
	case runKindJudgment:
		if !in.Judgment.valid() {
			return false
		}
	case runKindDelegate, runKindContract:
		if in.Judgment != "" {
			return false
		}
	}
	if in.Kind == runKindJudgment && in.Judgment.hasNoChallenge() {
		// J8 は課題を持たない判断（課題・計画・再開元を持たない）。セッションは持つ。
		if in.ChallengeVersion != 0 || in.PlanVersion != nil || in.ResumedFromRunID != nil || in.SessionID == "" {
			return false
		}
	} else if in.ChallengeVersion <= 0 || in.SessionID == "" {
		return false
	}
	if (in.Decider == "") != (in.DeciderRow == nil) {
		return false
	}
	if in.Decider != "" && (!in.Decider.valid() || *in.DeciderRow < 1 || *in.DeciderRow > 5) {
		return false
	}
	if in.TaskID != nil && *in.TaskID <= 0 {
		return false
	}
	if in.OperationID != nil && *in.OperationID <= 0 {
		return false
	}
	if in.SlotID != nil && *in.SlotID <= 0 {
		return false
	}
	if in.PID <= 0 || in.Host == "" {
		return false
	}
	if in.HeartbeatAt.IsZero() || in.StartedAt.IsZero() {
		return false
	}
	if in.MaxBudgetUSD <= 0 {
		return false
	}
	return true
}

// insertRun は challengeID の課題に新しい run を作る。呼び出し側は課題が同じ
// トランザクションの中に存在することを確かめておく（存在しなければ外部キー
// 違反の生のエラーが返る）。
//
//   - 入力が閉集合の外・必須値の欠落なら ErrValidation
//   - 課題に既に終了していない run があれば errActiveRunExists
//     （部分一意索引 idx_run_active_challenge〔課題とタスクの組ごとに 1 本〕の違反と、タスク付きの run と課題の単位の run の
//     同時の未終了を翻訳する。AC-168）
//
// J8 の run は課題に結び付かないため challengeID は 0 を渡す（NULL で
// 挿入する）。それ以外の run に 0 以下を渡すと ErrValidation。
//
// slot_id を指定し、そのスロットに終了していない run が既にあれば
// errActiveSlotRunExists（部分一意索引 idx_run_active_slot。AC-280）。
func insertRun(ctx context.Context, tx *sql.Tx, challengeID int64, in insertRunInput) (*runRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	noChallenge := in.Kind == runKindJudgment && in.Judgment.hasNoChallenge()
	if noChallenge != (challengeID == 0) || challengeID < 0 {
		return nil, ErrValidation
	}
	var challengeIDCol, challengeVersionCol, sessionIDCol any
	if !noChallenge {
		challengeIDCol, challengeVersionCol, sessionIDCol = challengeID, in.ChallengeVersion, in.SessionID
	} else if in.Kind == runKindJudgment {
		sessionIDCol = in.SessionID
	}

	if !noChallenge {
		// 一意の索引は課題とタスクの組ごと（版 29）なので、タスクに結び付く run とタスクに結び付かない run（課題の単位）が
		// 同時に終わっていない状態は索引では止まらない。見直し前と同じに、同じ書き込みの中でここで止める。
		q := `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND result IS NULL`
		// タスクに結び付く run は、タスクに結び付かない終わっていない run とも重ならない（逆向きは TaskLess が開ける。
		// 「足す」の形の run が回収されずに残ると、そのあいだ他のタスクの run は起動できない）。
		if in.TaskID != nil || in.TaskLess {
			q += ` AND task_id IS NULL`
		}
		var n int
		if err := tx.QueryRowContext(ctx, q, challengeID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, errActiveRunExists
		}
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO run (cycle_id, kind, judgment, challenge_id, challenge_version, plan_version,
			session_id, session_id_mismatch, resumed_from_run_id, pid, host, heartbeat_at, started_at,
			ended_at, result, rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source,
			reported_total_cost_usd, output, error, slot_id, decider, decider_row, repo, flywheel_version, task_id, operation_id, reflect)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?, NULL, NULL, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64PtrColumn(in.CycleID), string(in.Kind), nullableTextColumn(string(in.Judgment)), challengeIDCol,
		challengeVersionCol, int64PtrColumn(in.PlanVersion), sessionIDCol, int64PtrColumn(in.ResumedFromRunID),
		in.PID, in.Host, formatTimestamp(in.HeartbeatAt), formatTimestamp(in.StartedAt),
		in.MaxBudgetUSD, string(in.BudgetBucket),
		int64PtrColumn(in.SlotID), nullableTextColumn(string(in.Decider)), int64PtrColumn(in.DeciderRow),
		nullableTextColumn(in.Repo), nullableTextColumn(flywheelVersion()), int64PtrColumn(in.TaskID), int64PtrColumn(in.OperationID),
		boolToInt(in.OperationID != nil || in.Reflect),
	)
	if store.IsUniqueViolation(err) {
		// 部分一意索引のどちらに当たったかは、スロットを指定していて、その
		// スロットに終了していない run があるかで判別する。
		if in.SlotID != nil {
			var n int
			if qerr := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM run WHERE slot_id = ? AND result IS NULL`, *in.SlotID).Scan(&n); qerr == nil && n > 0 {
				return nil, errActiveSlotRunExists
			}
		}
		return nil, errActiveRunExists
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return loadRunByID(ctx, tx, id)
}

// loadRunByID は id の run を tx から読む。無ければ (nil, nil)。
func loadRunByID(ctx context.Context, tx *sql.Tx, id int64) (*runRow, error) {
	row := tx.QueryRowContext(ctx, runSelectColumns+" WHERE id = ?", id)
	r, err := scanRunRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// loadActiveRunByChallengeID は challengeID の課題の終了していない run
// （result IS NULL）を tx から読む。無ければ (nil, nil)。
func loadActiveRunByChallengeID(ctx context.Context, tx *sql.Tx, challengeID int64) (*runRow, error) {
	row := tx.QueryRowContext(ctx, runSelectColumns+" WHERE challenge_id = ? AND result IS NULL", challengeID)
	r, err := scanRunRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// loadActiveRunByChallengeTask は、課題 challengeID のタスク taskID の終わっていない run か、タスクに結び付かない
// 終わっていない run（task_id が空）を返す。無ければ nil。別のタスクの run は見ない。
func loadActiveRunByChallengeTask(ctx context.Context, tx *sql.Tx, challengeID, taskID int64) (*runRow, error) {
	row := tx.QueryRowContext(ctx, runSelectColumns+" WHERE challenge_id = ? AND result IS NULL AND (task_id IS NULL OR task_id = ?) LIMIT 1", challengeID, taskID)
	r, err := scanRunRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// updateRunEndInput は updateRunEnd の入力。Result は必須（run を終了させる
// 呼び出しであり、閉集合の値でなければならない）。CostUSD・
// ReportedTotalCostUSD は nil なら NULL のまま。CostSource・Output・Error は
// "" が NULL を表す。
type updateRunEndInput struct {
	EndedAt              time.Time
	Result               runResult
	RateLimited          bool
	CostUSD              *int64
	CostSource           costSource
	ReportedTotalCostUSD *int64
	Output               string
	Error                string
}

func (in updateRunEndInput) valid() bool {
	if in.EndedAt.IsZero() || !in.Result.valid() {
		return false
	}
	if in.CostSource != "" && !in.CostSource.valid() {
		return false
	}
	return true
}

// updateRunEnd は id の run の終了列（ended_at・result・rate_limited・
// cost_usd・cost_source・reported_total_cost_usd・output・error）を更新する。
// run が無ければ (nil, nil)。入力が不正なら ErrValidation。
func updateRunEnd(ctx context.Context, tx *sql.Tx, id int64, in updateRunEndInput) (*runRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM run WHERE id = ?`, id).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if runKind(kind) == runKindPredict {
		// 過去の predict の行に許す更新は、終わっていない行をサイクルの後片付けが中断にするものだけ。
		// cost_source は reported か unknown（delta は使わない）。
		if in.Result != runResultInterrupted || in.CostSource == costSourceDelta {
			return nil, ErrValidation
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE run SET ended_at = ?, result = ?, rate_limited = ?, cost_usd = ?, cost_source = ?,
			reported_total_cost_usd = ?, output = ?, error = ? WHERE id = ?`,
		formatTimestamp(in.EndedAt), string(in.Result), in.RateLimited, int64PtrColumn(in.CostUSD),
		nullableTextColumn(string(in.CostSource)), int64PtrColumn(in.ReportedTotalCostUSD),
		nullableTextColumn(in.Output), nullableTextColumn(in.Error), id,
	)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	return loadRunByID(ctx, tx, id)
}

// updateRunHeartbeat は id の run の heartbeat_at を更新する。run が無ければ
// (nil, nil)。
func updateRunHeartbeat(ctx context.Context, tx *sql.Tx, id int64, at time.Time) (*runRow, error) {
	if at.IsZero() {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx, `UPDATE run SET heartbeat_at = ? WHERE id = ?`, formatTimestamp(at), id)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	return loadRunByID(ctx, tx, id)
}

// --- cycle ---

// cycleRow は cycle 表の 1 行。ID は表示形（"Y-<n>"）。BudgetUSD・SpentUSD は
// USD の 100 万分の 1 を単位とする整数。Result は "" が NULL（終了していない
// 周）を表す。
type cycleRow struct {
	// EndReason は終わった理由（終わっていない周・過去の行は空）。Rounds は通した回りの数。
	EndReason string
	Rounds    int
	ID        string
	Trigger   string
	StartedAt time.Time
	EndedAt   *time.Time
	Result    cycleResult
	BudgetUSD int64
	SpentUSD  int64
}

const cycleSelectColumns = `SELECT id, trigger, started_at, ended_at, result, budget_usd, spent_usd, end_reason, rounds FROM cycle`

func scanCycleRow(row interface{ Scan(dest ...any) error }) (*cycleRow, error) {
	var (
		id                    int64
		trigger, startedAtStr string
		endedAtStr, result    sql.NullString
		endReason             sql.NullString
		budgetUSD, spentUSD   int64
		rounds                int64
	)
	if err := row.Scan(&id, &trigger, &startedAtStr, &endedAtStr, &result, &budgetUSD, &spentUSD, &endReason, &rounds); err != nil {
		return nil, err
	}
	startedAt, err := parseTimestamp(startedAtStr)
	if err != nil {
		return nil, err
	}
	c := &cycleRow{
		ID:        formatCycleID(id),
		Trigger:   trigger,
		StartedAt: startedAt,
		Result:    cycleResult(result.String),
		BudgetUSD: budgetUSD,
		SpentUSD:  spentUSD,
		EndReason: endReason.String,
		Rounds:    int(rounds),
	}
	if endedAtStr.Valid {
		t, err := parseTimestamp(endedAtStr.String)
		if err != nil {
			return nil, err
		}
		c.EndedAt = &t
	}
	return c, nil
}

// insertCycleInput は insertCycle の入力。
type insertCycleInput struct {
	Trigger   string
	StartedAt time.Time
	BudgetUSD int64
}

func (in insertCycleInput) valid() bool {
	return in.Trigger != "" && !in.StartedAt.IsZero() && in.BudgetUSD > 0
}

// insertCycle は新しい周を started_at・budget_usd とともに記録する
// （spent_usd は 0 で始まる。ended_at・result は NULL）。
func insertCycle(ctx context.Context, tx *sql.Tx, in insertCycleInput) (*cycleRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO cycle (trigger, started_at, ended_at, result, budget_usd, spent_usd) VALUES (?, ?, NULL, NULL, ?, 0)`,
		in.Trigger, formatTimestamp(in.StartedAt), in.BudgetUSD,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return loadCycleByID(ctx, tx, id)
}

// loadCycleByID は id の周を tx から読む。無ければ (nil, nil)。
func loadCycleByID(ctx context.Context, tx *sql.Tx, id int64) (*cycleRow, error) {
	row := tx.QueryRowContext(ctx, cycleSelectColumns+" WHERE id = ?", id)
	c, err := scanCycleRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// updateCycleEndInput は updateCycleEnd の入力。Result は必須（周を終了させる
// 呼び出し）。
type updateCycleEndInput struct {
	EndedAt time.Time
	Result  cycleResult
	// EndReason が空でなければ、終わった理由（end_reason）も書く。空なら列に触れない。
	EndReason CycleEndReason
}

func (in updateCycleEndInput) valid() bool {
	return !in.EndedAt.IsZero() && in.Result.valid()
}

// updateCycleEnd は id の周の ended_at・result を更新する。周が無ければ
// (nil, nil)。
func updateCycleEnd(ctx context.Context, tx *sql.Tx, id int64, in updateCycleEndInput) (*cycleRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	q, args := `UPDATE cycle SET ended_at = ?, result = ? WHERE id = ?`, []any{formatTimestamp(in.EndedAt), string(in.Result), id}
	if in.EndReason != "" {
		q, args = `UPDATE cycle SET ended_at = ?, result = ?, end_reason = ? WHERE id = ?`, []any{formatTimestamp(in.EndedAt), string(in.Result), string(in.EndReason), id}
	}
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	return loadCycleByID(ctx, tx, id)
}

// --- lock ---

// lockRow は lock 表の 1 行。Holder は表示形（"Y-<n>"）。
type lockRow struct {
	Name        string
	Holder      string
	PID         int64
	Host        string
	AcquiredAt  time.Time
	HeartbeatAt time.Time
}

const lockSelectColumns = `SELECT name, holder, pid, host, acquired_at, heartbeat_at FROM lock`

func scanLockRow(row interface{ Scan(dest ...any) error }) (*lockRow, error) {
	var (
		name, host                  string
		holder, pid                 int64
		acquiredAtStr, heartbeatStr string
	)
	if err := row.Scan(&name, &holder, &pid, &host, &acquiredAtStr, &heartbeatStr); err != nil {
		return nil, err
	}
	acquiredAt, err := parseTimestamp(acquiredAtStr)
	if err != nil {
		return nil, err
	}
	heartbeatAt, err := parseTimestamp(heartbeatStr)
	if err != nil {
		return nil, err
	}
	return &lockRow{
		Name:        name,
		Holder:      formatCycleID(holder),
		PID:         pid,
		Host:        host,
		AcquiredAt:  acquiredAt,
		HeartbeatAt: heartbeatAt,
	}, nil
}

// insertLockInput は insertLock の入力。Holder は周（cycle）の内部整数 ID。
type insertLockInput struct {
	Name        string
	Holder      int64
	PID         int64
	Host        string
	AcquiredAt  time.Time
	HeartbeatAt time.Time
}

func (in insertLockInput) valid() bool {
	return in.Name != "" && in.Holder > 0 && in.PID > 0 && in.Host != "" &&
		!in.AcquiredAt.IsZero() && !in.HeartbeatAt.IsZero()
}

// insertLock は name のロックを作る。既に name のロックがあれば、
// 主キー制約の違反を翻訳せずそのまま返す（呼び出し側〔#79・#80〕がロックの
// 保持・再取得の規則を実装する。#78 は行の作成・取得・heartbeat・削除だけを
// 持つ）。
func insertLock(ctx context.Context, tx *sql.Tx, in insertLockInput) (*lockRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO lock (name, holder, pid, host, acquired_at, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.Holder, in.PID, in.Host, formatTimestamp(in.AcquiredAt), formatTimestamp(in.HeartbeatAt),
	)
	if err != nil {
		return nil, err
	}
	return loadLockByName(ctx, tx, in.Name)
}

// loadLockByName は name のロックを tx から読む。無ければ (nil, nil)。
func loadLockByName(ctx context.Context, tx *sql.Tx, name string) (*lockRow, error) {
	row := tx.QueryRowContext(ctx, lockSelectColumns+" WHERE name = ?", name)
	l, err := scanLockRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// updateLockHeartbeat は name のロックの heartbeat_at を更新する。ロックが
// 無ければ (nil, nil)。
func updateLockHeartbeat(ctx context.Context, tx *sql.Tx, name string, at time.Time) (*lockRow, error) {
	if at.IsZero() {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx, `UPDATE lock SET heartbeat_at = ? WHERE name = ?`, formatTimestamp(at), name)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	return loadLockByName(ctx, tx, name)
}

// deleteLock は name のロックを削除する。無かった場合もエラーにしない
// （削除は冪等）。
func deleteLock(ctx context.Context, tx *sql.Tx, name string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM lock WHERE name = ?`, name)
	return err
}

// int64PtrColumn は p（nil 可のポインタ）を SQL のバインド値（NULL または
// 値）へ変換する。nullableTextColumn（source_binding.go）の整数版。
func int64PtrColumn(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
