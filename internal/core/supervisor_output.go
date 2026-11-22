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

// prefixSupervisorFailedQuestion は問いの先頭へ固定の 1 行を付ける（すでに付いていれば付けない）。
func prefixSupervisorFailedQuestion(question string) string {
	if strings.HasPrefix(question, supervisorFailedQuestionHead+"\n") {
		return question
	}
	return supervisorFailedQuestionHead + "\n" + question
}
