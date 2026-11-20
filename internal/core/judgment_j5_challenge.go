package core

// このファイルは、課題の単位の確かめ（統合先を使う、検証中の課題を対象にする J5）を持つ。
//
//   - 入力: 計画の背景（方針・完了したら見えるもの・やらないこと）・課題の取り込み元（見直し前と同じ読み方）・全部のタスクの題名と完了条件
//     （済み・取りやめの別）・上げる依頼が用意した実物（反映の操作の成果物。上げる依頼の成果物の PR）・終わっていない上げる依頼の facts・
//     直前の保留が challenge_check_not_met で計画を作り直さない回答だったときの回答の文。タスクごとの検証はやり直さない。
//   - 実行の記録の task_id は NULL（タスクに結び付かない実行）。
//   - 結果: 満たした・判断できないは、見直し前と同じ（applyJ5）。満たしていない（applyJ5ChallengeNotMet）は、課題を着手中へ戻して保留にし
//     （1 つの遷移。保留の理由は challenge_check_not_met・回答で戻る先は着手中）、作った反映の操作を閉じる。上げる依頼は、次の状態の確認が
//     commands.cancel で取り消す（PollContractRuns の cancelClosedReflectRun）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// 課題の単位の確かめの入力の区画のラベル（指示文はこの名前で区画を指す）。
	labelAllTasksSection             = "全部のタスク"
	labelPlanBackgroundSection       = "計画の背景"
	labelReflectFactsSection         = "上げる依頼が値で返したこと"
	labelChallengeCheckAnswerSection = "確かめの指摘への回答"
)

// j5ChallengeScope は、統合先を使う、検証中の課題の課題の単位の確かめが、始めたときに読んだ入力。
type j5ChallengeScope struct {
	// Tasks は課題の全部のタスク（ID の昇順）。
	Tasks []Task
	// Facts は終わっていない上げる依頼の契約の実行の facts の記録（無ければ nil）。
	Facts map[string]string
	// Answer は、直前の保留が challenge_check_not_met で、計画を作り直さない回答だったときの、回答の文（それ以外は空）。
	Answer string
}

// loadJ5ChallengeScope は課題 cid が統合先を使うとき、課題の単位の確かめの入力を読む。統合先を使わなければ nil。
func loadJ5ChallengeScope(ctx context.Context, tx *sql.Tx, cid int64) (*j5ChallengeScope, error) {
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if integ == nil || !*integ {
		return nil, nil
	}
	sc := &j5ChallengeScope{}
	if sc.Tasks, err = listTasks(ctx, tx, cid); err != nil {
		return nil, err
	}
	var raw sql.NullString
	qerr := tx.QueryRowContext(ctx,
		`SELECT facts FROM contract_run WHERE challenge_id = ? AND kind = ? AND ended_at IS NULL ORDER BY id DESC LIMIT 1`, cid, contractRunKindReflect).Scan(&raw)
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return nil, qerr
	}
	if raw.Valid {
		var m map[string]string
		if json.Unmarshal([]byte(raw.String), &m) == nil {
			sc.Facts = m
		}
	}
	// 直前の保留（最後の保留）が challenge_check_not_met で、計画を作り直さない回答のとき、その回答の文。その回答の後に、課題の単位の確かめが
	// 1 回済んでいれば（回答は読まれている）渡さない。
	var answer, reason, answeredAt sql.NullString
	var replan int
	qerr = tx.QueryRowContext(ctx,
		`SELECT answer, reason, answered_at, replan FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&answer, &reason, &answeredAt, &replan)
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return nil, qerr
	}
	if qerr == nil && answer.Valid && answeredAt.Valid && replan == 0 && reason.String == string(holdReasonChallengeCheckNotMet) {
		var later int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM run WHERE challenge_id = ? AND judgment = ? AND task_id IS NULL AND result = ? AND started_at > ?`,
			cid, string(JudgmentJ5), string(RunResultSucceeded), answeredAt.String).Scan(&later); err != nil {
			return nil, err
		}
		if later == 0 {
			sc.Answer = answer.String
		}
	}
	return sc, nil
}

// j5ChallengeCheckNote は区画「達成条件」の後ろへ足す、課題の単位の確かめであることの注記。
const j5ChallengeCheckNote = "この確認は課題の単位の確かめである。課題の達成条件（完了したら見えるもの）で、反映先へ上げる実物（成果物の PR）の全体を確かめる。" +
	"タスクごとの検証は済んでいるので、やり直さない。"

// j5ChallengeCheckDelegationReport は、課題の単位の確かめでの区画「直前の委譲の報告」の本文。
const j5ChallengeCheckDelegationReport = "課題の単位の確かめでは、委譲の報告は無い。反映先へ上げる実物（成果物の PR）を、「照合の結果」「PR の状態とチェックの結果」「PR の差分」で読む。"

func taskStatusLabel(s TaskStatus) string {
	switch s {
	case TaskStatusDone:
		return "済み"
	case TaskStatusCancelled:
		return "取りやめ"
	}
	return string(s)
}

// formatAllTasksSection は区画「全部のタスク」の本文（題名と完了条件。済み・取りやめの別つき）。
func formatAllTasksSection(tasks []Task) string {
	if len(tasks) == 0 {
		return "（タスクなし）"
	}
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "- [%s] %s\n  完了条件: %s\n", taskStatusLabel(t.Status), t.Title, t.DoneCriteria)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatPlanBackgroundSection は区画「計画の背景」の本文（方針・完了したら見えるもの・やらないこと）。完了したら見えるものは、課題の予想を優先し、
// 無ければ承認済みの計画の構造化した出力のものを書く。
func formatPlanBackgroundSection(ch Challenge, plan approvedPlan) string {
	var sp j2RawOutput
	if plan.HasSpec {
		_ = json.Unmarshal([]byte(plan.Spec), &sp)
	}
	var b strings.Builder
	if sp.Summary != nil && strings.TrimSpace(*sp.Summary) != "" {
		b.WriteString("方針: " + strings.TrimSpace(*sp.Summary) + "\n")
	}
	expected := ch.Expected
	if len(expected) == 0 {
		expected = sp.Expected
	}
	if len(expected) > 0 {
		b.WriteString("完了したら見えるもの:\n")
		for _, e := range expected {
			fmt.Fprintf(&b, "- %s（変更前: %s → 変更後: %s）\n", e.Scene, e.Before, e.After)
		}
	} else {
		b.WriteString("完了したら見えるもの: （記録なし）\n")
	}
	nonGoals := ch.NonGoals
	if len(nonGoals) == 0 {
		nonGoals = sp.NonGoals
	}
	if len(nonGoals) > 0 {
		b.WriteString("やらないこと:\n")
		for _, n := range nonGoals {
			b.WriteString("- " + n + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// challengeCheckSections は prepareJ5 が課題の単位の確かめのときに足す区画。
func challengeCheckSections(vc *j5Context) []JudgmentDataSection {
	sc := vc.ChallengeScope
	out := []JudgmentDataSection{
		{Label: labelPlanBackgroundSection, Content: formatPlanBackgroundSection(vc.Challenge, vc.Plan)},
		{Label: labelAllTasksSection, Content: formatAllTasksSection(sc.Tasks)},
	}
	if t := formatFactsSection(sc.Facts); t != "" {
		out = append(out, JudgmentDataSection{Label: labelReflectFactsSection, Content: t})
	}
	if strings.TrimSpace(sc.Answer) != "" {
		out = append(out, JudgmentDataSection{Label: labelChallengeCheckAnswerSection,
			Content: "前の確かめが満たしていないで保留になり、人が、計画を作り直さずに回答した文（J5 の読み違いを正す・flywheel の外で統合先を直した、など）。この回答を踏まえて確かめ直す。\n" + sc.Answer})
	}
	return out
}

// challengeCheckNotMetQuestion は、課題の単位の確かめが満たしていないときの保留の問い。
func challengeCheckNotMetQuestion(v *j5ValidatedOutput) string {
	feedback := ""
	if v.Feedback != nil {
		feedback = strings.TrimSpace(*v.Feedback)
	}
	return "課題の単位の確かめ（統合先に集めた全体を、課題の完了条件で確かめた結果）が、満たしていなかった。課題を着手中へ戻して保留にした。\n" +
		"理由: " + v.Reason + "\n指摘:\n" + feedback + "\n" +
		"計画を作り直す回答（--replan）で、直すタスクを足せる（済みのタスクは残り、同じ統合先に続けて集まる）。" +
		"統合先を flywheel の外で直した、または確かめの読み違いだと考えるときは、計画を作り直さずに回答する（回答の文は、次の確かめの入力に入る）。" +
		"回答すると、次の委譲の段が、上げる依頼を始め直し、課題の単位の確かめをやり直す。"
}

// finalizeJ5ChallengeOutput は課題の単位の確かめの出力を書く。満たした・判断できない・不正な出力は見直し前と同じ（finalizeJ5Output）。満たしていないは
// applyJ5ChallengeNotMet。
func (s *Store) finalizeJ5ChallengeOutput(ctx context.Context, runIDDisplay, challengeIDDisplay string, structured []byte, vh *j5VerifiedHeads) (outcome string, status *string, result RunResult, err error) {
	v, ok := validateJ5Output(structured)
	if !ok || v.Verdict != J5VerdictNotMet {
		return s.finalizeJ5Output(ctx, runIDDisplay, challengeIDDisplay, structured, vh)
	}
	mapped, err := s.applyJ5ChallengeNotMet(ctx, runIDDisplay, challengeIDDisplay, v, vh)
	if err != nil {
		return "", nil, "", err
	}
	outcome = string(v.Verdict)
	if !mapped {
		return outcome, nil, RunResultSucceeded, nil
	}
	st := string(StatusAwaitingHuman)
	return outcome, &st, RunResultSucceeded, nil
}

// applyJ5ChallengeNotMet は、課題の単位の確かめが満たしていないとき、1 つの遷移（保留 T11）で、反映の操作を閉じ、課題を着手中へ戻して保留にする
// （保留の理由は challenge_check_not_met・回答で戻る先は着手中・どのタスクにも結び付けない）。課題が検証中でなければ書かず、mapped=false。
func (s *Store) applyJ5ChallengeNotMet(ctx context.Context, runIDDisplay, challengeIDDisplay string, v *j5ValidatedOutput, vh *j5VerifiedHeads) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusVerifying {
			return errReconcileNotEligible
		}
		mapped = true
		if heads := vh.logEntries(); len(heads) > 0 {
			tc.after[activityKeyVerifiedHeads] = heads
		}
		if err := closeReflectOperations(ctx, tc, tc.id, "", "課題の単位の確かめが満たしていなかったため、反映の操作を閉じた"); err != nil {
			return err
		}
		if v.Actual != nil {
			tc.patchStages(StagePatch{Actual: StageSet(v.Actual)})
		}
		if err := s.insertHoldWithOccurrenceFrom(ctx, tc, challengeCheckNotMetQuestion(v), holdReasonChallengeCheckNotMet, "", StatusInProgress); err != nil {
			return err
		}
		if v.ConflictOverriddenFrom != "" {
			return nil // 衝突の PR のための置き換えは、達成条件の未達ではないので発生に数えない
		}
		return s.insertJ5Occurrence(ctx, tc, OccurrenceKindVerificationNotMet)
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}
