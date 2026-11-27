package core

import (
	"context"
	"database/sql"
	"fmt"
)

// このファイルは手 answer の記録（区切り S4b-3）。子の問いに答える手は、保留を作る前に呼ばれるので、閉じる保留が無く、
// 他の手のように保留を書き換えない（executeSupervisorHand の振り分けには入れない）。ここは、答えた問いと答えと根拠を対処の記録に残すだけで、
// 委譲先へ答えを返す処理は、子の問いの呼び出し元が行う。

// recordSupervisorAnswer は、検査を通った答え（req.Output。手は answer）を、問い questions と合わせて、対処の記録（結果 executed）と
// 作業ログに 1 つのトランザクションで書き、対処の記録の ID を返す。questions と答えの件数が合わないか、答えていない問い（nil の要素）があれば ErrValidation（何も書かない）。
// 記録の basis には、答えごとの根拠をまとめて入れる（問いごとの根拠は questions に入る）。保留は指さない（hold_id は NULL）。
// 判断の間に、そのタスクを進めてよくなくなっていたら（課題の取り下げ・そのタスクの保留など。述語は childQuestionTargetMayProceed）、答えを実行せず、同じ書き込みで断りの記録（rejected・state_changed）を残して
// executed を偽で返す（呼び出し元は、委譲先へ答えを返さない）。
func (s *Store) recordSupervisorAnswer(ctx context.Context, req supervisorHandRequest, questions []ChildQuestion) (id int64, executed bool, err error) {
	o := req.Output
	if o.Hand != HandAnswer || len(o.Answers) != len(questions) {
		return 0, false, fmt.Errorf("%w: answers do not match the questions", ErrValidation)
	}
	answers, aerr := allAnswered(o.Answers)
	if aerr != nil {
		return 0, false, fmt.Errorf("%w: %w", ErrValidation, aerr)
	}
	recorded := make([]SupervisorQuestion, len(questions))
	var basis []SupervisorBasis
	for i, q := range questions {
		recorded[i] = SupervisorQuestion{Question: q.Question, Answer: answers[i].Answer, Basis: answers[i].Basis}
		basis = append(basis, answers[i].Basis...)
	}
	in := req.actionInput(HandAnswer, o.Findings, o.Note, basis, ActionExecuted, "")
	in.HoldID = nil
	in.Questions = recorded
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, req.Target.ChallengeID)
		if err != nil {
			return err
		}
		executed, err = childQuestionTargetMayProceed(ctx, tx, ch, req.Target.TaskID)
		if err != nil {
			return err
		}
		if !executed {
			in = req.actionInput(HandAnswer, o.Findings, o.Note, basis, ActionRejected, RejectStateChanged)
			in.HoldID = nil
		}
		id, err = s.writeSupervisorAction(ctx, tx, req, in)
		return err
	})
	if err != nil {
		return 0, false, classifyReadWriteErr(err)
	}
	return id, executed, nil
}

// recordSupervisorChildFailure は、子の問いについて、判断できなかった・出力に不備があった（答えの件数・選択肢・根拠を含む）とき、
// 対処の記録（結果 failed）と作業ログを書き、対処の記録の ID を返す。保留はまだ無いので、hold_id は NULL。
// 呼び出し元は、この ID と理由を HoldEscalation（FailedQuestion を真）に入れて、問い全部の保留を 1 つ作る。hand は選ばれた手（無ければ nil）。
func (s *Store) recordSupervisorChildFailure(ctx context.Context, req supervisorHandRequest, hand *SupervisorHand, reason string) (int64, error) {
	var h SupervisorHand
	if hand != nil {
		h = *hand
	}
	in := req.actionInput(h, reason, "", nil, ActionFailed, "")
	in.HoldID = nil
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.writeSupervisorAction(ctx, tx, req, in)
		return err
	})
	return id, classifyReadWriteErr(err)
}
