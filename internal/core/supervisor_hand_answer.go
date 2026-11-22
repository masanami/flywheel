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
// 作業ログに 1 つのトランザクションで書き、対処の記録の ID を返す。questions と答えの件数が合わなければ ErrValidation（何も書かない）。
// 記録の basis には、答えごとの根拠をまとめて入れる（問いごとの根拠は questions に入る）。保留は指さない（hold_id は NULL）。
func (s *Store) recordSupervisorAnswer(ctx context.Context, req supervisorHandRequest, questions []ChildQuestion) (int64, error) {
	o := req.Output
	if o.Hand != HandAnswer || len(o.Answers) != len(questions) {
		return 0, fmt.Errorf("%w: answers do not match the questions", ErrValidation)
	}
	recorded := make([]SupervisorQuestion, len(questions))
	var basis []SupervisorBasis
	for i, q := range questions {
		recorded[i] = SupervisorQuestion{Question: q.Question, Answer: o.Answers[i].Answer, Basis: o.Answers[i].Basis}
		basis = append(basis, o.Answers[i].Basis...)
	}
	in := req.actionInput(HandAnswer, o.Findings, o.Note, basis, ActionExecuted, "")
	in.HoldID = nil
	in.Questions = recorded
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.writeSupervisorAction(ctx, tx, req, in)
		return err
	})
	return id, classifyReadWriteErr(err)
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
