package core

import (
	"fmt"
	"strings"
)

// このファイルはスーパーバイザーの判断点の出力の検査（区切り S4b-2）。実行の結果と出力の JSON から、手を実行してよいかを決める。
// ストアは読み書きしない（出力の不備の run を失敗に数える書き込みは、呼び出し元の verifySupervisorResult が行う）。

// supervisorFailedQuestionHead は、判断できなかった保留（対処の記録の結果が failed）の問いの先頭に付ける固定の 1 行。
const supervisorFailedQuestionHead = "スーパーバイザーが判断できなかった。"

// svOutputFlaw は判断点の結果が手を実行できる形でないことの理由。
type svOutputFlaw struct {
	// Reason は対処の記録・保留へ残す理由の文。
	Reason string
	// BadOutput は、判断点の実行は成功で終わったが出力に不備があった（run を失敗に数える対象）ことを表す。
	BadOutput bool
}

// inspectSupervisorOutput は判断点の実行の結果 run を検査する。手を実行してよい出力なら out を返し、
// 不備（実行が成功で終わらなかった・出力の形・必須の項目・閉じた一覧の外の手・選べる手の外の手・選択肢の件数と見出し）があれば flaw を返す。
func inspectSupervisorOutput(run *RunJudgmentResult, offered []SupervisorHand) (out *SVOutput, flaw *svOutputFlaw) {
	if run.Result != RunResultSucceeded {
		return nil, &svOutputFlaw{Reason: fmt.Sprintf("スーパーバイザーの判断点の実行が成功で終わらなかった（%s）", run.Result)}
	}
	parsed, err := ParseSVOutput(run.StructuredOutput, offered)
	if err != nil {
		return nil, &svOutputFlaw{Reason: fmt.Sprintf("スーパーバイザーの出力に不備があった（%v）", err), BadOutput: true}
	}
	return parsed, nil
}

// ChildQuestion は子の問い 1 件（スーパーバイザーが答える対象）。Options は選択肢の見出し（選択肢がある問いだけ。無ければ空）。
type ChildQuestion struct {
	Question string
	Options  []string
}

// inspectChildQuestionOutput は子の問い（止まり方 child_question）への判断点の実行の結果を検査する。inspectSupervisorOutput の検査に加え、
// 手が answer のとき、答えの件数が問いの数と合うこと・全部の問いに答えていること（null の要素が無い）・選択肢がある問いの答えが選択肢の見出しのどれかであること・
// 問いごとに根拠として数えられる basis が 1 件以上あること（root はワークスペースの根）を確かめる。不備は BadOutput の flaw。
func inspectChildQuestionOutput(run *RunJudgmentResult, offered []SupervisorHand, questions []ChildQuestion, root string) (*SVOutput, *svOutputFlaw) {
	out, flaw := inspectSupervisorOutput(run, offered)
	if flaw != nil || out.Hand != HandAnswer {
		return out, flaw
	}
	if err := checkChildAnswers(out.Answers, questions, root); err != nil {
		return nil, &svOutputFlaw{Reason: fmt.Sprintf("スーパーバイザーの答えに不備があった（%v）", err), BadOutput: true}
	}
	return out, nil
}

// allAnswered は、問いごとの答え answers が全部ある（null の要素が無い）ときに、その値の並びを返す。委譲先の問いは、一部の問いにだけ答えられない。
func allAnswered(answers []*SVAnswer) ([]SVAnswer, error) {
	out := make([]SVAnswer, len(answers))
	for i, a := range answers {
		if a == nil {
			return nil, fmt.Errorf("問い %d に答えていない（委譲先の問いは、全部に答えるか全部上げる）", i+1)
		}
		out[i] = *a
	}
	return out, nil
}

// checkChildAnswers は答え answers が問い questions に対して成り立つかを確かめる。
func checkChildAnswers(answers []*SVAnswer, questions []ChildQuestion, root string) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("答えの件数 %d が問いの数 %d と合わない", len(answers), len(questions))
	}
	all, err := allAnswered(answers)
	if err != nil {
		return err
	}
	for i, a := range all {
		q := questions[i]
		if len(q.Options) > 0 {
			found := false
			for _, o := range q.Options {
				if o == a.Answer {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("問い %d の答え %q が選択肢にない", i+1, a.Answer)
			}
		}
		if countableBasis(root, a.Basis) == 0 {
			return fmt.Errorf("問い %d に、根拠として数えられる場所（ワークスペースの中の通常のファイル）がない", i+1)
		}
	}
	return nil
}

// prefixSupervisorFailedQuestion は問いの先頭へ固定の 1 行を付ける（すでに付いていれば付けない）。
func prefixSupervisorFailedQuestion(question string) string {
	if strings.HasPrefix(question, supervisorFailedQuestionHead+"\n") {
		return question
	}
	return supervisorFailedQuestionHead + "\n" + question
}
