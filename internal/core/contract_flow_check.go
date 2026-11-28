package core

// このファイルは、flow の形の委譲先でタスクの委譲を始める前の確かめ（フローの定義が検査を通るか）を持つ
// （docs/features/flow-customization.md「壊れた定義」）。
//
//   - 始める前: 同じ実行ファイルの `flow check --json <フローの名前>` を、契約のコマンドと同じ実行ファイルと --workspace で呼ぶ。
//     通らなければ委譲を始めず、委譲を止める保留（理由 flow_definition_invalid）を立てる。同じタスクに同じ理由の保留が開いていれば
//     立て直さない。確かめのコマンド自体が起動できない・JSON が読めないときは、始めるコマンドが失敗したときと同じ扱いにする。
//   - 次のサイクル: 委譲の段の初めに、開いている flow_definition_invalid の保留ごとに確かめ直し、通れば保留を回答なしで閉じる
//     （T22。作業ログに残る）。閉じた後は、いつもどおりタスクを選んで委譲を始める。
//
// 組み込みの定義へ切り替えて動かすことはしない。予算の上限は core が持ち、定義からは外せない（委譲の実行の上限額は、
// 課題とタスクの枠と周の上限から core が決め、定義の値を読まない）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FlowCheckProblem は確かめの出力の problems の 1 要素（どのファイルのどこが、なぜ悪いか）。Line は 0 なら行が無い。
type FlowCheckProblem struct {
	File    string
	Line    int
	Message string
}

// FlowCheckDefinition は確かめの出力の definitions の 1 要素。
type FlowCheckDefinition struct {
	Name     string
	Source   string // workspace | builtin
	Kind     string // replace | new | builtin
	Path     string // 定義のファイルの絶対パス（組み込みは空）
	SHA256   string // 通らなければ空
	OK       bool
	Problems []FlowCheckProblem
	// Description は定義の説明（通らなければ空）。計画を作る判断点の入力で、ワークスペースの定義を選ぶ目安にする。
	Description string
}

// FlowCheckOutput は確かめのコマンド 1 回の結果。Result は launch_failed | timed_out | malformed | errored | succeeded のどれか
// （succeeded は、終了コードが 0 か 1 で、標準出力が確かめの JSON として読めたこと。通ったかは Definitions の OK で見る）。
type FlowCheckOutput struct {
	Result       RunResult
	Definitions  []FlowCheckDefinition
	ErrorSummary string
}

// flowCheckCommand は、フロー name を確かめるコマンドの引数の配列（契約のコマンドと同じ実行ファイルと --workspace）。
func (s *Store) flowCheckCommand(name string) ([]string, error) {
	if s.flowExecutable == "" {
		return nil, fmt.Errorf("%w: the executable path for form flow connectors is not set", ErrValidation)
	}
	return []string{s.flowExecutable, "flow", "check", "--workspace", s.workspace, "--json", name}, nil
}

// flowCheckVerdict は確かめの結論。Definition は確かめた定義（読めたとき）。Unavailable が真なら確かめられなかった
// （Result・Error に失敗の種類と詳細）。
type flowCheckVerdict struct {
	OK          bool
	Definition  *FlowCheckDefinition
	Unavailable bool
	Result      RunResult
	Error       string
}

// checkFlowDefinition はフロー name の定義を確かめる。
func (s *Store) checkFlowDefinition(ctx context.Context, client ContractClient, agent *AgentDeclaration, name string) flowCheckVerdict {
	command, err := s.flowCheckCommand(name)
	if err != nil {
		return flowCheckVerdict{Unavailable: true, Result: RunResultLaunchFailed, Error: err.Error()}
	}
	timeout := 0
	if agent != nil {
		timeout = agent.TimeoutSec.Delegate
	}
	out, err := client.CheckFlow(ctx, ContractLaunchInput{Workspace: s.workspace, WorkDir: s.workspace, Command: command, TimeoutSec: timeout})
	if err != nil {
		return flowCheckVerdict{Unavailable: true, Result: RunResultLaunchFailed, Error: err.Error()}
	}
	if out.Result != RunResultSucceeded {
		r := out.Result
		if !r.valid() {
			r = RunResultErrored
		}
		return flowCheckVerdict{Unavailable: true, Result: r, Error: out.ErrorSummary}
	}
	for i := range out.Definitions {
		d := &out.Definitions[i]
		if d.Name != name {
			continue
		}
		if d.OK && d.SHA256 == "" {
			break // 通ったのにハッシュの無い出力は形が合わない
		}
		return flowCheckVerdict{OK: d.OK, Definition: d}
	}
	return flowCheckVerdict{Unavailable: true, Result: RunResultMalformed,
		Error: fmt.Sprintf("the output of the flow check has no definition %q of the expected shape", name)}
}

// flowCheckUnavailableOutput は、確かめられなかったときに、始めるコマンドの代わりに返す結果（始めるコマンドが失敗したときと同じ扱いにする）。
func flowCheckUnavailableOutput(v flowCheckVerdict) ContractLaunchOutput {
	return ContractLaunchOutput{Result: v.Result, ErrorSummary: "委譲の前のフローの定義の確かめ（flow check）が失敗した: " + v.Error}
}

// flowDefinitionInvalidQuestion は、定義が確かめを通らなかったときの保留の問い。
func flowDefinitionInvalidQuestion(task Task, name string, d *FlowCheckDefinition) string {
	var b strings.Builder
	where := "組み込みの定義"
	if d.Source == FlowDefinitionSourceWorkspace {
		where = "ワークスペースの定義"
		if d.Path != "" {
			where += "（" + d.Path + "）"
		}
	}
	fmt.Fprintf(&b, "タスク %s のフロー %q の%sが検査を通らないので、委譲を始めていない。", task.ID, name, where)
	if len(d.Problems) > 0 {
		b.WriteString("悪い所:\n")
		for _, p := range d.Problems {
			b.WriteString("- ")
			b.WriteString(formatFlowCheckProblem(p))
			b.WriteString("\n")
		}
	} else {
		b.WriteString("\n")
	}
	b.WriteString("定義を直せば、次のサイクルで自動で委譲を始める（組み込みの定義へ切り替えて動かすことはしない）。")
	return b.String()
}

// formatFlowCheckProblem は「ファイル:行: 理由」（行が無ければ「ファイル: 理由」、ファイルも無ければ理由だけ）。
func formatFlowCheckProblem(p FlowCheckProblem) string {
	switch {
	case p.File != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
	case p.File != "":
		return p.File + ": " + p.Message
	}
	return p.Message
}

// holdFlowDefinitionInvalid は、定義が確かめを通らないときの委譲を止める保留を立て、起動しないことを表す ErrValidation の誤りを返す。
// 同じタスクに同じ理由の保留が開いていれば立て直さない。課題が着手中でも人間対応待ちでもなければ、保留は立てない。
func (s *Store) holdFlowDefinitionInvalid(ctx context.Context, challengeID string, task Task, name string, d *FlowCheckDefinition) error {
	question := flowDefinitionInvalidQuestion(task, name, d)
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
		var open int
		if err := tc.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND task_id IS ? AND reason = ? AND answer IS NULL AND closed_at IS NULL`,
			tc.id, int64PtrColumn(taskIDPtr(&task)), string(holdReasonFlowDefinitionInvalid)).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return errReconcileNotEligible // 同じ理由の保留が開いている
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
		_, err = s.insertHoldWithOccurrenceOptions(ctx, tc, question, holdReasonFlowDefinitionInvalid, "", scoped)
		return err
	})
	if herr != nil && !errors.Is(herr, ErrInvalidTransition) && !errors.Is(herr, ErrTerminalState) && !errors.Is(herr, errReconcileNotEligible) {
		return herr
	}
	return fmt.Errorf("%w: the flow definition %q of task %s does not pass the check; the delegation was not started", ErrValidation, name, task.ID)
}

// flowDefinitionHold は、開いている flow_definition_invalid の保留 1 件。
type flowDefinitionHold struct {
	HoldID      int64
	ChallengeID int64
	TaskID      int64
}

// recheckFlowDefinitionHolds は、開いている flow_definition_invalid の保留（challengeID が非 nil ならその課題のものだけ）ごとに、
// そのタスクのフローの定義を確かめ直し、通れば保留を回答なしで閉じる（T22）。通らない・確かめられないときは何もしない（保留は開いたまま）。
// 閉じた保留の数を返す。ストアの失敗だけを返す。
func (s *Store) recheckFlowDefinitionHolds(ctx context.Context, client ContractClient, agent *AgentDeclaration, conn *ConnectorsDeclaration, challengeID *string) (int, error) {
	if client == nil || conn == nil {
		return 0, nil
	}
	var holds []flowDefinitionHold
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		q := `SELECT h.id, h.challenge_id, h.task_id FROM hold h JOIN challenge c ON c.id = h.challenge_id
			WHERE h.reason = ? AND h.answer IS NULL AND h.closed_at IS NULL AND h.task_id IS NOT NULL AND c.status = ?`
		args := []any{string(holdReasonFlowDefinitionInvalid), string(StatusAwaitingHuman)}
		if challengeID != nil {
			cid, ok := parseChallengeID(*challengeID)
			if !ok {
				return nil
			}
			q += ` AND h.challenge_id = ?`
			args = append(args, cid)
		}
		rows, err := tx.QueryContext(ctx, q+` ORDER BY h.id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var h flowDefinitionHold
			if err := rows.Scan(&h.HoldID, &h.ChallengeID, &h.TaskID); err != nil {
				return err
			}
			holds = append(holds, h)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	closed := 0
	for _, h := range holds {
		var task *Task
		if err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
			task, err = loadTask(ctx, tx, h.TaskID)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}); classifyReadWriteErr(err) != nil {
			return closed, classifyReadWriteErr(err)
		}
		if task == nil {
			continue
		}
		_, connector := conn.findConnectorRepo(task.Repo)
		if connector == nil || connector.Form != ConnectorFormFlow {
			continue // 宣言が変わった。保留はオーナーが扱う
		}
		name := taskStartFlow(connector, *task)
		if name == "" {
			continue
		}
		v := s.checkFlowDefinition(ctx, client, agent, name)
		if !v.OK {
			continue
		}
		ok, err := s.closeFlowDefinitionHold(ctx, h, name, v.Definition)
		if err != nil {
			return closed, err
		}
		if ok {
			closed++
		}
	}
	return closed, nil
}

// closeFlowDefinitionHold は保留 h を回答なしで閉じ、課題を保留の直前の状態へ戻す（ほかの開いている保留が残れば人間対応待ちのまま）。
// 保留が既に閉じていた・課題が人間対応待ちでなくなっていたときは何もしない（偽）。
func (s *Store) closeFlowDefinitionHold(ctx context.Context, h flowDefinitionHold, name string, d *FlowCheckDefinition) (bool, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return false, err
	}
	resolvePreceding := func(ctx context.Context, tx *sql.Tx, cid int64, _ *Challenge) (Status, error) {
		var from string
		err := tx.QueryRowContext(ctx, `SELECT from_status FROM hold WHERE id = ? AND challenge_id = ? AND reason = ? AND answer IS NULL AND closed_at IS NULL`,
			h.HoldID, cid, string(holdReasonFlowDefinitionInvalid)).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errReconcileNotEligible
		}
		return Status(from), err
	}
	_, err = s.runTransition(context.WithoutCancel(ctx), runTransitionParams{
		actor: actor, channel: ChannelInvoker, verification: VerificationNone, id: formatChallengeID(h.ChallengeID),
		op: OpFlowDefinitionUnhold, preceding: NoStatus, resolvePreceding: resolvePreceding, restricted: true,
	}, func(ctx context.Context, tc *transitionCtx) error {
		if err := requireOneRow(tc.tx.ExecContext(ctx,
			`UPDATE hold SET closed_at = ? WHERE id = ? AND answer IS NULL AND closed_at IS NULL`, tc.nowStr, h.HoldID)); err != nil {
			return err
		}
		tc.after["closed_holds"] = []string{formatHoldID(h.HoldID)}
		tc.after["closed_holds_reason"] = "フローの定義の確かめが通ったため"
		tc.after["task_id"] = formatTaskID(h.TaskID)
		tc.after["flow"] = name
		if d != nil {
			tc.after["definition_source"] = d.Source
			tc.after["definition_sha256"] = d.SHA256
		}
		return nil
	})
	if errors.Is(err, errReconcileNotEligible) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		return false, nil
	}
	return err == nil, err
}

// countRecheckableFlowDefinitionHolds は、開いている flow_definition_invalid の保留のうち、保留を立てた後にワークスペースの
// フローの定義の置き場（<ws>/.flywheel/flows とその下の指示文）が変わったものの数を返す（サイクルを回すと進むものの数え）。
// 置き場が変わっていない保留は、確かめ直しても通らないので数えない（定義が壊れたままのワークスペースでサイクルを空回りさせない）。
// 置き場が無くなった（組み込みへ戻した）ときは変わったとみなす。
func countRecheckableFlowDefinitionHolds(ctx context.Context, tx *sql.Tx, workspace string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT h.raised_at FROM hold h JOIN challenge c ON c.id = h.challenge_id
		WHERE h.reason = ? AND h.answer IS NULL AND h.closed_at IS NULL AND h.task_id IS NOT NULL AND c.status = ?`,
		string(holdReasonFlowDefinitionInvalid), string(StatusAwaitingHuman))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var raised []time.Time
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, err
		}
		t, err := parseTimestamp(at)
		if err != nil {
			return 0, err
		}
		raised = append(raised, t)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(raised) == 0 {
		return 0, nil
	}
	changed, gone := flowsDirLatestChange(filepath.Join(workspace, ".flywheel", "flows"))
	n := 0
	for _, t := range raised {
		if gone || changed.After(t) {
			n++
		}
	}
	return n, nil
}

// flowsDirLatestChange は、置き場のディレクトリと、その中・prompts の下の項目の更新の日時のうち最も新しいものを返す。
// 置き場が無ければ gone が真。
func flowsDirLatestChange(dir string) (latest time.Time, gone bool) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return time.Time{}, true
	}
	latest = fi.ModTime()
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(latest) {
				latest = info.ModTime()
			}
			if e.IsDir() && depth < 3 {
				walk(filepath.Join(d, e.Name()), depth+1)
			}
		}
	}
	walk(dir, 0)
	return latest, false
}
