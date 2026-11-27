package core

// このファイルはスーパーバイザーの判断点（SV。区切り S4b-2）の「判断点を呼ぶ」部分: 入力（データの区画）の組み立てと呼び出し。
// 判断点は型つきの値を返すだけで、ストアを書かない（書くのは手の実行）。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// supervisorRunRecordLimit は入力に渡す、タスクの実行の記録の件数（新しいほうから）。
const supervisorRunRecordLimit = 5

// svCall は判断点の呼び出しの結果。NotStarted が非 nil なら起動しなかった（Run は nil）。
type svCall struct {
	Run        *RunJudgmentResult
	NotStarted *NotStarted
	Offered    []SupervisorHand
}

// callSupervisorJudgment は保留 t について入力を組み立て、判断点 SV を 1 回呼ぶ（費用は課題とサイクルの両方に入る）。
func (s *Store) callSupervisorJudgment(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle, t supervisorTarget) (svCall, error) {
	offered, err := s.offeredSupervisorHands(ctx, t)
	if err != nil {
		return svCall{}, err
	}
	sections, err := s.buildSupervisorSections(ctx, in.AgentDecl, t, offered)
	if err != nil {
		return svCall{}, err
	}
	return s.invokeSupervisorJudgment(ctx, in.AgentDecl, in.Invoker, cycleID, jc, t.ChallengeID, t.TaskID, sections, offered)
}

// invokeSupervisorJudgment は組み立てた入力で判断点 SV を 1 回呼ぶ（費用は課題とサイクルの両方に入る）。保留のある呼び出し（callSupervisorJudgment）と、
// 保留を作る前の子の問いの呼び出し（callChildQuestionJudgment）が共有する。
func (s *Store) invokeSupervisorJudgment(ctx context.Context, decl *AgentDeclaration, invoker JudgmentInvoker, cycleID string, jc *JudgmentCycle,
	challengeID int64, taskID *int64, sections []JudgmentDataSection, offered []SupervisorHand) (svCall, error) {
	runIn := RunJudgmentInput{
		ChallengeID:  formatChallengeID(challengeID),
		Judgment:     JudgmentSV,
		MaxBudgetUSD: decl.JudgmentBudgetFor(JudgmentSV),
		TimeoutSec:   decl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: svOutputSchema(),
		Invoker:      invoker,
		TaskID:       taskID,
	}
	if jc != nil {
		res, ns, err := jc.RunJudgment(ctx, s, runIn)
		return svCall{Run: res, NotStarted: ns, Offered: offered}, err
	}
	runIn.CycleID = &cycleID
	res, err := s.RunJudgment(ctx, runIn)
	return svCall{Run: res, Offered: offered}, err
}

// buildSupervisorSections は判断点 SV の入力（データの区画）を組み立てる。読み取りだけ。
func (s *Store) buildSupervisorSections(ctx context.Context, decl *AgentDeclaration, t supervisorTarget, offered []SupervisorHand) ([]JudgmentDataSection, error) {
	var sections []JudgmentDataSection
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, t.ChallengeID)
		if err != nil {
			return err
		}
		holds, err := loadHolds(ctx, tx, t.ChallengeID)
		if err != nil {
			return err
		}
		sections = append(sections, JudgmentDataSection{Label: "止まり方と保留の問い",
			Content: fmt.Sprintf("止まり方: %s\n保留の問い: %s", t.StopKind, t.Question)})

		var b strings.Builder
		fmt.Fprintf(&b, "課題: %s\n", ch.Title)
		if t.TaskID != nil {
			task, err := loadTask(ctx, tx, *t.TaskID)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "タスク: %s\nタスクの完了条件（番号つき）:\n%s", task.Title, numberedLines(task.DoneCriteria))
		} else {
			fmt.Fprintf(&b, "課題の完了条件（番号つき）:\n%s\n課題のタスク（題名と状態）:\n", numberedLines(ch.DoneCriteria))
			rows, err := tx.QueryContext(ctx, `SELECT title, status FROM task WHERE challenge_id = ? ORDER BY id`, t.ChallengeID)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var title, status string
				if err := rows.Scan(&title, &status); err != nil {
					return err
				}
				fmt.Fprintf(&b, "- [%s] %s\n", status, title)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		sections = append(sections, JudgmentDataSection{Label: "課題とタスク", Content: strings.TrimRight(b.String(), "\n")})

		if p, found, err := loadApprovedPlanAnySpec(ctx, tx, t.ChallengeID); err != nil {
			return err
		} else if found && strings.TrimSpace(p.Body) != "" {
			sections = append(sections, JudgmentDataSection{Label: "計画の方針", Content: p.Body})
		}
		if t.TaskID != nil {
			rec, err := supervisorRunRecords(ctx, tx, t.ChallengeID, *t.TaskID)
			if err != nil {
				return err
			}
			if rec != "" {
				sections = append(sections, JudgmentDataSection{Label: "実行の記録", Content: rec})
			}
		}
		var where string
		var args []any
		if t.TaskID != nil {
			where, args = `WHERE task_id = ?`, []any{*t.TaskID}
		} else {
			where, args = `WHERE challenge_id = ? AND task_id IS NULL`, []any{t.ChallengeID}
		}
		actions, err := querySupervisorActions(ctx, tx, where, args...)
		if err != nil {
			return err
		}
		if len(actions) > 0 {
			sections = append(sections, JudgmentDataSection{Label: "これまでの対処の記録", Content: formatSupervisorActions(actions)})
		}
		conds, err := splittableConditions(ctx, tx, t)
		if err != nil {
			return err
		}
		if len(conds) > 0 {
			sections = append(sections, JudgmentDataSection{Label: "回せる条件", Content: formatSplittableConditions(conds)})
		}
		var hb strings.Builder
		for _, h := range offered {
			fmt.Fprintf(&hb, "- %s: %s\n", h, supervisorHandDescription(t.StopKind, h))
		}
		sections = append(sections, JudgmentDataSection{Label: "選べる手の一覧", Content: strings.TrimRight(hb.String(), "\n")})
		if hs := formatHoldsSection(holds); hs != "" {
			sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: hs})
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}
	return append(sections, memoryIndexSections(s.workspace, decl)...), nil
}

// numberedLines は完了条件の文を、空でない行ごとに 1 から番号を振った文にする（行頭の箇条書きの印・番号は外す）。
func numberedLines(text string) string {
	var b strings.Builder
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, m := range []string{"- ", "* ", "• "} {
			line = strings.TrimSpace(strings.TrimPrefix(line, m))
		}
		if i := strings.IndexAny(line, ".)"); i > 0 && i+1 < len(line) && line[i+1] == ' ' {
			if _, err := strconv.Atoi(line[:i]); err == nil {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		if line == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s\n", n, line)
	}
	if n == 0 {
		return "(なし)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// supervisorDelegateOutputRunes は、契約でない委譲の実行の出力（委譲先が返した値）を入力へ入れるときの文字数の上限。
const supervisorDelegateOutputRunes = 1000

// supervisorRunRecords はタスクの実行の記録（契約の実行と、契約でない委譲の実行〔run の kind が delegate〕）を、
// 新しいほうから最大 supervisorRunRecordLimit 件、状態・要約・委譲先が返した値・成果物の識別つきで書く。
// 新しさは、実行の開始の時刻で比べる。
func supervisorRunRecords(ctx context.Context, tx *sql.Tx, cid, tid int64) (string, error) {
	type rec struct {
		startRun              int64  // 成果物の持ち主の run の ID
		startedAt             string // 新しさの比べ方（時刻の文字列は辞書順で時刻順）
		seq                   int64  // 同じ時刻のときの比べ方（種類ごとの行の ID）
		label                 string
		state, summary, facts string
	}
	var recs []rec
	rows, err := tx.QueryContext(ctx, `SELECT id, state, summary, COALESCE(facts, ''), start_run_id, started_at FROM contract_run
		WHERE challenge_id = ? AND task_id = ? AND kind = ? ORDER BY id DESC LIMIT ?`, cid, tid, contractRunKindTask, supervisorRunRecordLimit)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.seq, &r.state, &r.summary, &r.facts, &r.startRun, &r.startedAt); err != nil {
			_ = rows.Close()
			return "", err
		}
		r.label = strconv.FormatInt(r.seq, 10)
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	_ = rows.Close()
	drows, err := tx.QueryContext(ctx, `SELECT id, started_at, COALESCE(result, ''), COALESCE(error, ''), COALESCE(output, '') FROM run
		WHERE challenge_id = ? AND task_id = ? AND kind = ? ORDER BY id DESC LIMIT ?`, cid, tid, string(runKindDelegate), supervisorRunRecordLimit)
	if err != nil {
		return "", err
	}
	for drows.Next() {
		var r rec
		var result, errText, output string
		if err := drows.Scan(&r.seq, &r.startedAt, &result, &errText, &output); err != nil {
			_ = drows.Close()
			return "", err
		}
		r.label = formatRunID(r.seq)
		r.startRun = r.seq
		r.state = result
		if r.state == "" {
			r.state = "実行中"
		}
		r.summary = errText
		if r.summary == "" {
			r.summary = "(なし)"
		}
		if o := []rune(output); len(o) > supervisorDelegateOutputRunes {
			output = string(o[:supervisorDelegateOutputRunes]) + "…"
		}
		r.facts = output
		recs = append(recs, r)
	}
	if err := drows.Err(); err != nil {
		_ = drows.Close()
		return "", err
	}
	_ = drows.Close()
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].startedAt != recs[j].startedAt {
			return recs[i].startedAt > recs[j].startedAt
		}
		return recs[i].seq > recs[j].seq
	})
	if len(recs) > supervisorRunRecordLimit {
		recs = recs[:supervisorRunRecordLimit]
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "- 実行 %s: 状態 %s\n  要約: %s\n", r.label, r.state, r.summary)
		if r.facts != "" {
			fmt.Fprintf(&b, "  委譲先が返した値: %s\n", r.facts)
		}
		ars, err := tx.QueryContext(ctx, `SELECT kind, ref, COALESCE(state, '') FROM run_artifact WHERE run_id = ? ORDER BY id`, r.startRun)
		if err != nil {
			return "", err
		}
		for ars.Next() {
			var kind, ref, state string
			if err := ars.Scan(&kind, &ref, &state); err != nil {
				_ = ars.Close()
				return "", err
			}
			fmt.Fprintf(&b, "  成果物: %s %s (%s)\n", kind, ref, state)
		}
		if err := ars.Err(); err != nil {
			_ = ars.Close()
			return "", err
		}
		_ = ars.Close()
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// formatSupervisorActions は対処の記録を古い順の 1 行ずつの文にする。
func formatSupervisorActions(actions []SupervisorAction) string {
	var b strings.Builder
	for _, a := range actions {
		hand := "(判断できなかった)"
		if a.Hand != nil {
			hand = string(*a.Hand)
		}
		fmt.Fprintf(&b, "- %s 止まり方 %s 手 %s 結果 %s: %s\n", a.CycleID, a.StopKind, hand, a.Result, a.Findings)
	}
	return strings.TrimRight(b.String(), "\n")
}
