package core

// このファイルは委譲の段が使うタスクの口を持つ: 起動できるタスクの判定、委譲の起動・結果によるタスクの
// 状態の変化、J3 の入力の区画「タスク」。タスクの行の読み書きは task.go の口を使う。
// 課題の状態と遷移は変えない。委譲の履歴・再開・やり直しの数え・予算の枠は、課題と計画の版の単位のまま。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// labelTaskSection は J3 の入力の区画「タスク」の見出し。
const labelTaskSection = "タスク"

// isLaunchableTaskStatus は、状態が起動できるタスクのもの（pending・in_progress）か。
func isLaunchableTaskStatus(s TaskStatus) bool {
	return s == TaskStatusPending || s == TaskStatusInProgress
}

// loadLaunchableTasks は課題 cid の起動できるタスク（task の行のうち状態が pending か in_progress のもの。
// 計画の版は問わない）を ID の昇順で返す。構造化した出力ではなく行で数える。
func loadLaunchableTasks(ctx context.Context, tx *sql.Tx, cid int64) ([]Task, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(all))
	for _, t := range all {
		if isLaunchableTaskStatus(t.Status) {
			out = append(out, t)
		}
	}
	return out, nil
}

// loadTasksToLaunch は課題 cid の起動するタスクの行を返す。taskID が 0 なら起動できるタスク（loadLaunchableTasks）、0 でなければ
// そのタスク 1 個（課題のタスクで、状態が起動できるもの〔pending・in_progress〕でなければ ErrValidation）。
func loadTasksToLaunch(ctx context.Context, tx *sql.Tx, cid, taskID int64) ([]Task, error) {
	if taskID == 0 {
		return loadLaunchableTasks(ctx, tx, cid)
	}
	t, err := loadTask(ctx, tx, taskID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: task %s does not exist", ErrValidation, formatTaskID(taskID))
		}
		return nil, err
	}
	if t.ChallengeID != formatChallengeID(cid) || !isLaunchableTaskStatus(t.Status) {
		return nil, fmt.Errorf("%w: task %s is not launchable", ErrValidation, t.ID)
	}
	return []Task{*t}, nil
}

// launchSelection は課題 1 件について、委譲の段が起動するタスクと、起動しなかった理由。
type launchSelection struct {
	// Tasks は起動するタスク（タスクの ID の昇順）。
	Tasks []Task
	// NotStarted は起動しなかった理由（課題の単位の理由と、タスクの単位の理由。ChallengeID は課題の ID）。
	NotStarted []NotStarted
	// Busy は、終わっていない実行を持つタスクがあること（起動できるタスクが無くても、理由を出さずに待つ根拠）。
	Busy bool
	// Blocked が空でなければ、進めようが無いので保留にする（tasks_blocked）。値は保留の問い。
	Blocked string
}

// isLaunchableTask は、タスク t が起動できるか（状態だけで決まる部分）: 作業中（in_progress。再開・やり直し）か、未着手で
// 前提のタスクがすべて済み（done）のもの。
func isLaunchableTask(t Task, byID map[string]Task) bool {
	switch t.Status {
	case TaskStatusInProgress:
		return true
	case TaskStatusPending:
		for _, d := range t.DependsOn {
			if byID[d].Status != TaskStatusDone {
				return false
			}
		}
		return true
	}
	return false
}

// blockedPendingTasks は、前提に取りやめ（cancelled）のタスクを持つ、または、そのようなタスクを前提に持つ未着手のタスクの
// ID の集合（前提をたどって決める。取りやめのタスクを前提に持つ限り、起動できる日が来ない）。
func blockedPendingTasks(all []Task, byID map[string]Task) map[string]bool {
	blocked := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, t := range all {
			if t.Status != TaskStatusPending || blocked[t.ID] {
				continue
			}
			for _, d := range t.DependsOn {
				if byID[d].Status == TaskStatusCancelled || blocked[d] {
					blocked[t.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return blocked
}

// selectLaunchTasks は課題 cid（表示の ID は challengeID）の委譲の段が起動するタスクを決める。conn が nil なら、委譲先の
// 形による判定をしない。
//
// 統合先を使わない課題は、起動できるタスクがちょうど 1 個のときだけ起動する（0 個は no_tasks、2 個以上は
// integration_unavailable）。統合先を使う課題は、タスクが 1 個でも 2 個以上でも起動する（タスクの単位で見る）:
//   - 取りやめでないタスク（未着手・作業中・検証中・済み）のリポジトリが 2 つ以上 → 進めようが無いので、保留にする（tasks_blocked）
//   - 起動できるタスク（isLaunchableTask）で、終わっていない実行を持たず、対応する子が起動を妨げず（taskUpstreamExclusion）、
//     委譲先が form: cli のものを起動する。form が cli でない委譲先のタスクは integration_unavailable
//   - 起動するタスクが無く、進めようが無い（blockedByCancelledTask）なら、保留にする
//
// 計画の検査（validateJ2Output）はここで掛けない（呼び出し側が、起動できるときだけ後で行う）。
func selectLaunchTasks(ctx context.Context, tx *sql.Tx, conn *ConnectorsDeclaration, challengeID string, cid int64) (*launchSelection, error) {
	sel, err := selectLaunchTasksOfChallenge(ctx, tx, conn, challengeID, cid)
	if err != nil {
		return nil, err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, cid).Scan(&status); err != nil {
		return nil, err
	}
	if Status(status) != StatusAwaitingHuman {
		return sel, nil
	}
	// 人間対応待ちの課題（タスクを止める保留だけが開いているものだけが、ここへ来る）は、進めてよいタスクだけを選ぶ（述語 taskMayProceed）。
	// 進めようが無い課題の保留（tasks_blocked）は、課題の状態で決めるので、着手中へ戻ってから行う（Blocked を返さない）。
	out := &launchSelection{Busy: sel.Busy, NotStarted: sel.NotStarted}
	held := 0
	for _, t := range sel.Tasks {
		ok, err := taskMayProceed(ctx, tx, cid, taskIntID(&t))
		if err != nil {
			return nil, err
		}
		if ok {
			out.Tasks = append(out.Tasks, t)
		} else {
			held++
		}
	}
	// 起動できるタスクが保留で外れて残らなかったときも、起動しなかった理由を残す（課題が着手中のときの理由は引き継ぐ）。
	if len(out.Tasks) == 0 && held > 0 && len(out.NotStarted) == 0 && !out.Busy {
		out.NotStarted = append(out.NotStarted, NotStarted{ChallengeID: challengeID, Reason: NotStartedNoTasks,
			Detail: "起動できるタスクは、タスクを止める保留の回答を待っている"})
	}
	return out, nil
}

// selectLaunchTasksOfChallenge は selectLaunchTasks の、課題の状態を見ずに、起動できるタスクを選ぶ部分。
func selectLaunchTasksOfChallenge(ctx context.Context, tx *sql.Tx, conn *ConnectorsDeclaration, challengeID string, cid int64) (*launchSelection, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Task, len(all))
	for _, t := range all {
		byID[t.ID] = t
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	sel := &launchSelection{}
	ns := func(r NotStartedReason, detail string) {
		sel.NotStarted = append(sel.NotStarted, NotStarted{ChallengeID: challengeID, Reason: r, Detail: detail})
	}
	if integ == nil || !*integ {
		var cands []Task
		var waiting []string
		for _, t := range all {
			if !isLaunchableTask(t, byID) {
				continue
			}
			if tid := taskIntID(&t); tid > 0 {
				open, err := taskHasOpenContractRun(ctx, tx, cid, tid)
				if err != nil {
					return nil, err
				}
				if open {
					waiting = append(waiting, t.ID)
					continue
				}
			}
			cands = append(cands, t)
		}
		switch {
		case len(cands) == 0 && len(waiting) > 0:
			ns(NotStartedNoTasks, waitingTasksDetail(waiting))
		case len(cands) == 0:
			ns(NotStartedNoTasks, "起動できるタスクが無い")
		case len(cands) >= 2:
			ns(NotStartedIntegrationUnavailable, "統合先を使わない課題は、起動できるタスクが 2 個以上あるとき起動しない")
		default:
			if n, err := taskUpstreamExclusion(ctx, tx, challengeID, cid, &cands[0]); err != nil {
				return nil, err
			} else if n != nil {
				sel.NotStarted = append(sel.NotStarted, *n)
			} else {
				sel.Tasks = cands
			}
		}
		return sel, nil
	}

	// タスクを止める保留が開いているタスクは起動しない（保留が閉じるまで延ばす）。開いている間は、進めようが無い課題の保留
	// （tasks_blocked）も立てない（課題の状態で決めるまま。人間対応待ちの間は立てない）。
	heldTasks, err := openTaskHoldTaskIDs(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	// 統合先を使う課題は、リポジトリを 1 つに限る。済みのタスクも数える（上げる依頼が、済みのタスクのリポジトリが 1 つのときだけ始まるのと
	// 同じ基準。数えないと、別のリポジトリのタスクを足した計画が、走って費用を使った後で、上げる依頼が毎回始まらない）。
	repos := map[string]bool{}
	var repoList []string
	for _, t := range all {
		if t.Status != TaskStatusCancelled && !repos[t.Repo] {
			repos[t.Repo] = true
			repoList = append(repoList, t.Repo)
		}
	}
	if len(repoList) >= 2 {
		// 進めようが無い（計画の検査が通すはずの無い計画。手で登録した計画・過去の計画）。毎周起動しないだけでオーナーに見えないので、保留にする。
		// ただし、作業している実行・検証中のタスクがある間は、ほかの「進めようが無い」と同じく、起動しないだけで待つ（保留にすると、動いている
		// 作業の結果を受け取る前に課題を止める）。保留は、起動するタスクが無く、実行中でもなくなってから立てる。
		busy, err := taskLessHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		for _, t := range all {
			if busy {
				break
			}
			if t.Status == TaskStatusVerifying {
				busy = true
			} else if tid := taskIntID(&t); tid > 0 && t.Status != TaskStatusCancelled {
				if busy, err = taskHasActiveRun(ctx, tx, cid, tid); err != nil {
					return nil, err
				}
			}
		}
		if busy || len(heldTasks) > 0 {
			sel.Busy = true
			return sel, nil
		}
		sel.Blocked = fmt.Sprintf("統合先を使う課題のタスクは、1 つのリポジトリにそろえる必要があるが、この課題のタスクは 2 つ以上のリポジトリにまたがっている"+
			"（取りやめたタスクは数えない。済み・未完のタスクのリポジトリ: %s）。このままでは進められない。"+
			"計画を作り直す回答（タスクのリポジトリを 1 つにそろえる）をするか、課題を取り下げて進めてください。", strings.Join(repoList, "・"))
		return sel, nil
	}
	taskLessBusy, err := taskLessHasActiveRun(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	sel.Busy = taskLessBusy
	blocked := blockedPendingTasks(all, byID)
	var notCLI []string
	var waiting []string // 終わっていない契約の実行（レビュー待ち・オーナー待ち）だけを持つタスク（起動せず、待つ）
	for _, t := range all {
		if taskLessBusy || !isLaunchableTask(t, byID) {
			continue
		}
		if heldTasks[taskIntID(&t)] {
			sel.Busy = true // 保留の回答を待つタスク（起動せず、待つ）
			continue
		}
		if tid := taskIntID(&t); tid > 0 {
			busy, err := taskHasActiveRun(ctx, tx, cid, tid)
			if err != nil {
				return nil, err
			}
			if busy {
				sel.Busy = true
				continue
			}
			open, err := taskHasOpenContractRun(ctx, tx, cid, tid)
			if err != nil {
				return nil, err
			}
			if open {
				waiting = append(waiting, t.ID)
				continue
			}
		}
		if n, err := taskUpstreamExclusion(ctx, tx, challengeID, cid, &t); err != nil {
			return nil, err
		} else if n != nil {
			sel.NotStarted = append(sel.NotStarted, *n)
			continue
		}
		if conn != nil {
			if _, c := conn.findConnectorRepo(t.Repo); c != nil && !c.UsesContractCommands() {
				notCLI = append(notCLI, fmt.Sprintf("%s（委譲先 %s・形 %s）", t.ID, c.ID, c.Form))
				continue
			}
		}
		sel.Tasks = append(sel.Tasks, t)
	}
	if len(notCLI) > 0 {
		ns(NotStartedIntegrationUnavailable, "統合先を使う課題のタスクは、委譲の形が契約の形（form: cli）でない委譲先へは起動しない: "+strings.Join(notCLI, "・"))
	}
	if len(sel.Tasks) > 0 || sel.Busy {
		return sel, nil
	}
	if len(heldTasks) > 0 {
		return sel, nil
	}
	if q, err := blockedByCancelledTask(ctx, tx, cid, all, blocked); err != nil {
		return nil, err
	} else if q != "" {
		sel.Blocked = q
		return sel, nil
	}
	if len(sel.NotStarted) == 0 {
		if len(waiting) > 0 {
			ns(NotStartedNoTasks, waitingTasksDetail(waiting))
		} else {
			ns(NotStartedNoTasks, "起動できるタスクが無い（前提のタスクが済むのを待っている）")
		}
	}
	return sel, nil
}

// taskHasOpenContractRun は、課題 cid のタスク taskID に、終わっていない契約の実行（レビュー待ち・オーナー待ちを含む。
// ended_at IS NULL）があるかを返す。beginContractRunForTask が start の前に断る条件と同じ。taskHasActiveRun はレビュー待ち・
// オーナー待ちを数えないので、起動できるタスクの候補は、この確かめでも外す（外さないと、委譲の準備まで進んでから断られる）。
func taskHasOpenContractRun(ctx context.Context, tx *sql.Tx, cid, taskID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND task_id = ? AND ended_at IS NULL`, cid, taskID).Scan(&n)
	return n > 0, err
}

// blockedByCancelledTask は、統合先を使う課題が進めようが無いとき、保留の問いを返す（そうでなければ空）。実行中のタスク
// （in_progress）も、検証中のタスクも、終わっていない実行・契約の実行も無く、かつ、未着手のタスクがどれも前提に取りやめの
// タスクがあるために起動できない（blocked）か、未完のタスクも済みのタスクも無い（全部が取りやめ）とき。
func blockedByCancelledTask(ctx context.Context, tx *sql.Tx, cid int64, all []Task, blocked map[string]bool) (string, error) {
	var pending []Task
	done := 0
	for _, t := range all {
		switch t.Status {
		case TaskStatusInProgress, TaskStatusVerifying:
			return "", nil
		case TaskStatusPending:
			pending = append(pending, t)
		case TaskStatusDone:
			done++
		}
	}
	allCancelled := len(all) > 0 && len(pending) == 0 && done == 0
	if len(pending) == 0 && !allCancelled {
		return "", nil
	}
	for _, t := range pending {
		if !blocked[t.ID] {
			return "", nil
		}
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM run WHERE challenge_id = ? AND result IS NULL)
		+ (SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL)`, cid, cid).Scan(&open); err != nil {
		return "", err
	}
	if open > 0 {
		return "", nil
	}
	if allCancelled {
		return "この課題のタスクは、全部が取りやめになっていて、進めるタスクが無い。計画を作り直す回答をするか、課題を取り下げて進めてください。", nil
	}
	lines := make([]string, 0, len(pending))
	for _, t := range pending {
		var causes []string
		for _, d := range t.DependsOn {
			if dep := byIDOf(all, d); dep.Status == TaskStatusCancelled || blocked[d] {
				causes = append(causes, fmt.Sprintf("%s（%s）", d, dep.Title))
			}
		}
		lines = append(lines, fmt.Sprintf("- %s（%s）: 前提のタスク %s が取りやめ、または、取りやめのタスクを前提に持つため起動できない", t.ID, t.Title, strings.Join(causes, "・")))
	}
	return "次のタスクは、前提のタスクが取りやめになったため起動できず、ほかに進めるタスクも無い。計画を作り直す回答をするか、課題を取り下げて進めてください。\n" + strings.Join(lines, "\n"), nil
}

func byIDOf(all []Task, id string) Task {
	for _, t := range all {
		if t.ID == id {
			return t
		}
	}
	return Task{}
}

// taskIntID はタスクの内部整数 ID。
func taskIntID(t *Task) int64 {
	n, _ := parseTaskID(t.ID)
	return n
}

// taskIDPtr はタスクの内部整数 ID のポインタ（ID を持たないタスクのゼロ値は nil＝NULL）。
func taskIDPtr(t *Task) *int64 {
	if n := taskIntID(t); n > 0 {
		return &n
	}
	return nil
}

// newInvokerRecorder は、委譲の run を原因とする作業ログ（経路 invoker）の記録口を tx に作る。
func (s *Store) newInvokerRecorder(tx *sql.Tx, runID *int64) (*activityRecorder, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	return &activityRecorder{
		tx: tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone,
		insert: s.insertActivity, runID: runID,
	}, nil
}

// startTaskTx は委譲の起動で、pending のタスクを in_progress にする（作業ログは同じトランザクション）。
// すでに in_progress など pending でないタスクの再開・やり直しでは何も変えない。
func (s *Store) startTaskTx(ctx context.Context, tx *sql.Tx, taskID int64, runID *int64) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if TaskStatus(status) != TaskStatusPending {
		return nil
	}
	rec, err := s.newInvokerRecorder(tx, runID)
	if err != nil {
		return err
	}
	_, err = setTaskStatus(ctx, tx, rec, taskID, TaskStatusInProgress)
	return err
}

// verifyTaskInTransition は、課題が検証中へ進む遷移（tc）と同じトランザクションで、タスクを verifying にする
// （作業ログも同じトランザクション）。taskID が nil（過去の行など）なら何もしない。
func verifyTaskInTransition(ctx context.Context, tc *transitionCtx, taskID *int64) error {
	if taskID == nil {
		return nil
	}
	// 作業中のタスクだけを検証中にする（計画の作り直しで取りやめになったタスクなどを動かさない）。
	var status string
	if err := tc.tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if TaskStatus(status) != TaskStatusInProgress {
		return nil
	}
	_, err := setTaskStatus(ctx, tc.tx, tc.rec, *taskID, TaskStatusVerifying)
	return err
}

// verifyContractTask は、契約の run（externalRunID）が結び付くタスクを検証中にする（課題が検証中へ進む遷移 tc と同じ
// トランザクション）。タスクに結び付かない契約の run（過去の行）では何もしない。
func verifyContractTask(ctx context.Context, tc *transitionCtx, externalRunID string) error {
	taskID, err := contractRunTaskID(ctx, tc.tx, tc.id, externalRunID)
	if err != nil {
		return err
	}
	return verifyTaskInTransition(ctx, tc, taskID)
}

// contractRunTaskID は課題の、契約の実行 ID が externalRunID の契約の run の task_id を読む（無ければ nil）。
func contractRunTaskID(ctx context.Context, tx *sql.Tx, challengeID int64, externalRunID string) (*int64, error) {
	var v sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT task_id FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`,
		challengeID, externalRunID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !v.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v.Int64, nil
}

// formatTaskSection は J3 の入力の区画「タスク」の本文（題名・何をするか・完了条件・やらないこと）。
func formatTaskSection(t Task) string {
	nonGoals := "（なし）"
	if len(t.NonGoals) > 0 {
		lines := make([]string, len(t.NonGoals))
		for i, g := range t.NonGoals {
			lines[i] = "- " + g
		}
		nonGoals = "\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("題名: %s\n\n何をするか:\n%s\n\n完了条件:\n%s\n\nやらないこと: %s", t.Title, t.Body, t.DoneCriteria, nonGoals)
}

// formatBriefTaskSection は J3 の入力の区画「タスク」の本文。formatTaskSection（完了の検証と共有する）の後ろに、
// タスクが受け持つ受入基準の節を足す（受け持つ基準が無ければ足さない）。list は承認した計画に書き足した一覧。
func formatBriefTaskSection(t Task, list *planAcceptanceCriteria) string {
	s := formatTaskSection(t)
	if lines := formatCoveredCriteria(list, t.Covers); lines != "" {
		s += "\n\n" + coveredCriteriaHeading + ":\n" + strings.TrimSuffix(lines, "\n")
	}
	return s
}

// taskDocPlan は、タスクの文面の「背景」に入れる、承認済みの計画の中身。
type taskDocPlan struct {
	Summary  string
	Topics   []taskDocTopic
	Expected []StageExpected
	NonGoals []string
	// Criteria は計画に書き足した受入基準の一覧（一覧を持たない計画は nil）。タスクが受け持つ基準の節に使う。
	Criteria *planAcceptanceCriteria
}

// taskDocTopic は論点 1 つ（題名・どう決めたか・問いと記録した答え）。
type taskDocTopic struct {
	Title     string
	Decision  string
	Questions []taskDocQuestion
}

// taskDocQuestion は問い 1 つと、記録した答え（答えの記録が無いときは Answer が空）。
type taskDocQuestion struct {
	Number   int64
	Text     string
	Answer   string
	Answered bool
}

// formatTaskDocument は起動するタスクの文面（UTF-8 の Markdown）を組み立てる。LLM も判断の呼び出しの口も使わない。
// 上から、背景（承認済みの計画の、方針・論点ごとの問いと答え・完了したら見えるもの・やらないこと）、このタスク
// （題名・何をするか・完了条件・受け持つ受入基準〔あるときだけ〕・やらないこと・対応する取り込み元〔タスクが子に対応するときだけ。取り込みのときに記録した
// 子の題名・URL・本文。上流は読み直さない〕）、ほかのタスク（題名の一覧と、範囲外の明記）の順。ほかのタスクの
// 「何をするか」は入れない。others は呼び出し側が取りやめを除いて渡す。
func formatTaskDocument(plan taskDocPlan, task Task, source *SourceChild, others []Task) string {
	var b strings.Builder
	b.WriteString("# 背景\n\n## 方針\n\n")
	b.WriteString(blankOr(plan.Summary, "（なし）"))
	b.WriteString("\n\n## 論点と決定\n\n")
	if len(plan.Topics) == 0 {
		b.WriteString("（なし）\n\n")
	}
	for _, t := range plan.Topics {
		fmt.Fprintf(&b, "### %s\n\n決定: %s\n\n", t.Title, blankOr(t.Decision, "（なし）"))
		for _, q := range t.Questions {
			ans := "（答えの記録なし）"
			if q.Answered {
				ans = q.Answer
			}
			fmt.Fprintf(&b, "- 問い: %s\n  答え: %s\n", q.Text, ans)
		}
		if len(t.Questions) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("## 完了したら見えるもの\n\n")
	if len(plan.Expected) == 0 {
		b.WriteString("（なし）\n")
	}
	for _, e := range plan.Expected {
		fmt.Fprintf(&b, "- [%s] %s\n  前: %s\n  後: %s\n", e.Kind, e.Scene, e.Before, e.After)
	}
	b.WriteString("\n## やらないこと\n\n")
	b.WriteString(bulletList(plan.NonGoals))
	b.WriteString("\n# このタスク\n\n")
	fmt.Fprintf(&b, "題名: %s\n\n## 何をするか\n\n%s\n\n## 完了条件\n\n%s\n\n", task.Title, task.Body, task.DoneCriteria)
	if lines := formatCoveredCriteria(plan.Criteria, task.Covers); lines != "" {
		fmt.Fprintf(&b, "## %s\n\n%s\n", coveredCriteriaHeading, lines)
	}
	fmt.Fprintf(&b, "## やらないこと\n\n%s\n", bulletList(task.NonGoals))
	if source != nil {
		fmt.Fprintf(&b, "\n## 対応する取り込み元\n\n題名: %s\n\nURL: %s\n\n本文:\n\n%s\n", source.Title, source.URL, source.Body)
	}
	b.WriteString("\n# ほかのタスク\n\n")
	if len(others) == 0 {
		b.WriteString("同じ課題のほかのタスクは無い。\n")
		return b.String()
	}
	b.WriteString("ほかのタスクは範囲外である。このタスクの範囲だけを行う。\n\n")
	for _, o := range others {
		fmt.Fprintf(&b, "- %s\n", o.Title)
	}
	return b.String()
}

func blankOr(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "（なし）\n"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
	return b.String()
}

// loadTaskDocument は課題 cid の、承認済みの計画 plan とタスク task から、タスクの文面を組み立てる（読むだけ）。
// 問いの答えは plan_answer のその問いの最後の行（選択肢の答えは選択肢の文、文の答えはその文）。ほかのタスクは、
// 最新の計画の版のタスクのうち、このタスク以外で取りやめでないもの。
func loadTaskDocument(ctx context.Context, tx *sql.Tx, cid int64, plan approvedPlan, task Task) (string, error) {
	var raw j2RawOutput
	if plan.HasSpec {
		if err := json.Unmarshal([]byte(plan.Spec), &raw); err != nil {
			return "", fmt.Errorf("%w: the approved plan spec is malformed", ErrValidation)
		}
	}
	answers, err := listPlanAnswers(ctx, tx, cid, int64(plan.Version))
	if err != nil {
		return "", err
	}
	byQuestion := make(map[int64]PlanAnswer, len(answers))
	for _, a := range answers {
		byQuestion[a.QuestionNumber] = a
	}
	criteria, err := loadPlanAcceptanceCriteria(plan.Spec)
	if err != nil {
		return "", fmt.Errorf("%w: the approved plan spec is malformed", ErrValidation)
	}
	doc := taskDocPlan{Expected: raw.Expected, NonGoals: raw.NonGoals, Criteria: criteria}
	if raw.Summary != nil {
		doc.Summary = *raw.Summary
	}
	var n int64
	for _, t := range raw.Topics {
		topic := taskDocTopic{Title: t.Title, Decision: t.Decision}
		for _, q := range t.Questions {
			n++
			tq := taskDocQuestion{Number: n, Text: q.Question}
			if a, ok := byQuestion[n]; ok {
				switch {
				case a.Choice != nil && *a.Choice >= 1 && int(*a.Choice) <= len(q.Options):
					tq.Answer, tq.Answered = q.Options[*a.Choice-1].Label, true
				case a.Text != nil:
					tq.Answer, tq.Answered = *a.Text, true
				}
			}
			topic.Questions = append(topic.Questions, tq)
		}
		doc.Topics = append(doc.Topics, topic)
	}
	var others []Task
	if latest, ok, err := latestPlanVersion(ctx, tx, cid); err != nil {
		return "", err
	} else if ok {
		all, err := listTasks(ctx, tx, cid)
		if err != nil {
			return "", err
		}
		for _, t := range all {
			if t.PlanVersion == latest && t.ID != task.ID && t.Status != TaskStatusCancelled {
				others = append(others, t)
			}
		}
	}
	var source *SourceChild
	if task.SourceKey != "" {
		children, err := loadSourceChildren(ctx, tx, cid)
		if err != nil {
			return "", err
		}
		for i := range children {
			if children[i].ExternalKey == task.SourceKey {
				source = &children[i]
			}
		}
	}
	return formatTaskDocument(doc, task, source, others), nil
}

// taskFileName は、起動の実行の記録の置き場へ書くタスクの文面のファイル名。
const taskFileName = "task.md"

// writeTaskFile は文面 text を、起動の実行の記録の置き場 runDir の下の task.md へ UTF-8 で書き、その絶対パスを返す。
// 契約の実行が終わった後も消さない。
func writeTaskFile(runDir, text string) (string, error) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(abs, taskFileName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// waitingTasksDetail は、終わっていない契約の実行（レビュー待ち・オーナー待ち）を持つタスクしか残っていないときの、起動しない理由の詳細。
func waitingTasksDetail(ids []string) string {
	return "起動できるタスクが無い（終わっていない契約の実行を持つタスクが、レビュー・回答を待っている: " + strings.Join(ids, "・") + "）"
}
