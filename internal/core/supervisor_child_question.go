package core

// このファイルは、結末が「問い」の委譲の報告（子の問い）をスーパーバイザーが受ける入口（区切り S4b-3）。
// 保留を作る前に判断点 SV を 1 回呼び、全問に答えれば保留を作らず（次の起動が答えを渡して委譲先を再開する）、
// 上げるときだけ、全部の問いを 1 つの保留にする（止まり方 child_question）。判断点を呼ばない・呼べないときは、保留の印（supervisor_state）を付けない。

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// childQuestionEnv は、委譲の照合が結末 questions をスーパーバイザーへ回すための口。Answered は、全問に答えて保留を作らなかったとき真になる。
type childQuestionEnv struct {
	In       DelegateInput
	Cycle    *JudgmentCycle
	Answered bool
	// Ctx は取り消しの効く文脈（照合の記録は取り消されても行うが、判断点の起動は周の中断で止める）。
	Ctx context.Context
}

// childRoute は子の問いの振り分けの結果。Answered が真なら保留を作らない（Answers が、問いごとの答え）。偽のとき、Escalation はスーパーバイザーが上げた（または判断できなかった）
// ときの保留に残す値で、nil なら判断点を呼ばなかった・呼べなかった（印は NULL）。Reason は、呼べなかった理由（保留の問いの先頭に書く。無ければ空）。
type childRoute struct {
	Answered   bool
	Answers    []SVAnswer
	Escalation *HoldEscalation
	Reason     string
}

// childRouteParams は、子の問いをスーパーバイザーへ回す共通の入口（routeChildQuestions）の入力。委譲の報告の問いと契約の判断待ちが共有する。
type childRouteParams struct {
	// Ctx は取り消しの効く文脈（判断点の起動は周の中断で止める）。
	Ctx         context.Context
	Decl        *AgentDeclaration
	Judgment    JudgmentInvoker
	CycleID     string
	Cycle       *JudgmentCycle
	ChallengeID int64
	TaskID      *int64
	// Question は判断点へ渡す問いの文。Questions は問いごとの文と選択肢（答えの検査に使う）。
	Question  string
	Questions []ChildQuestion
	// Prepare が非 nil なら、検査を通った答えを記録する前に呼ぶ。エラーを返したら、答えを委譲先へ返せないので、
	// 対処の記録を結果 failed で書き、固定の 1 行（supervisorAnswerNotReturnedHead）つきで上げる（答えは記録しない）。
	Prepare func(answers []SVAnswer) error
}

// supervisorAnswerNotReturnedHead は、スーパーバイザーが答えたが委譲先へ返せなかった保留の、問いの先頭に付ける固定の 1 行。
const supervisorAnswerNotReturnedHead = "スーパーバイザーの答えを、委譲先へ返せなかった。"

// routeQuestionsToSupervisor は結末 questions の報告をスーパーバイザーへ回すか決め、回すなら判断点を呼んで手を実行する。
// 回さないもの（判断点を呼ばない）: 宣言の supervisor が偽・「人に聞く種類」に当たる問いを 1 件でも含む報告・答えによる再開の上限に達した課題。
// 委譲の意思決定者が人でも、種類が「人に聞く種類」に当たらなければ回す。エラーはストアの失敗だけ。
func (s *Store) routeQuestionsToSupervisor(ctx context.Context, dc *delegationContext, report *DelegationReport, env *childQuestionEnv) (childRoute, error) {
	decl := env.In.AgentDecl
	if decl == nil || !decl.Supervisor || env.In.Judgment == nil || env.In.ConnDecl == nil ||
		j4RuleEscalates(report.Questions, env.In.ConnDecl.HumanQuestionKinds) {
		return childRoute{}, nil
	}
	jctx := env.Ctx
	if jctx == nil {
		jctx = ctx
	}
	cid, ok := parseChallengeID(dc.Challenge.ID)
	if !ok {
		return childRoute{}, ErrValidation
	}
	questions := make([]ChildQuestion, len(report.Questions))
	for i, q := range report.Questions {
		questions[i] = ChildQuestion{Question: q.Text, Options: q.Options}
	}
	return s.routeChildQuestions(ctx, childRouteParams{
		Ctx: jctx, Decl: decl, Judgment: env.In.Judgment, CycleID: env.In.CycleID, Cycle: env.Cycle,
		ChallengeID: cid, TaskID: taskIDPtr(&dc.Task), Question: formatQuestionsHold(report), Questions: questions,
	})
}

// routeChildQuestions は、回すと決まった子の問い p をスーパーバイザーへ回す（判断点を 1 回呼び、手を実行する）。
// 課題が着手中でないとき・答えによる再開の上限に達しているときは、判断点を呼ばない（Escalation なしの空の値）。
func (s *Store) routeChildQuestions(ctx context.Context, p childRouteParams) (childRoute, error) {
	jctx := p.Ctx
	if jctx == nil {
		jctx = ctx
	}
	if inProgress, err := s.challengeInProgress(jctx, p.ChallengeID); err != nil || !inProgress {
		return childRoute{}, err
	}
	// 答えによる再開の上限に達していれば、判断点を呼ばない（保留を作る入口が、問いの先頭に固定の 1 行を付ける）。
	reached, err := s.answerLimitReached(jctx, p.ChallengeID)
	if err != nil || reached {
		return childRoute{}, err
	}

	t := supervisorTarget{ChallengeID: p.ChallengeID, TaskID: p.TaskID, StopKind: StopKindChildQuestion, Question: p.Question}
	call, err := s.callChildQuestionJudgment(jctx, p, t)
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			return childRoute{Reason: "周の上限のため、スーパーバイザーを呼べなかった。"}, nil
		}
		return childRoute{}, err
	}
	if call.NotStarted != nil {
		return childRoute{Reason: fmt.Sprintf("スーパーバイザーを呼べなかった（理由: %s）。", call.NotStarted.Reason)}, nil
	}
	if call.Run.RateLimited {
		return childRoute{Reason: "利用上限のため、スーパーバイザーを呼べなかった。"}, nil
	}
	req := supervisorHandRequest{CycleID: p.CycleID, Target: t, RunID: call.Run.RunID, Decl: p.Decl}
	out, bad := s.verifyChildQuestionResult(jctx, call, p.Questions)
	if bad != "" {
		id, err := s.recordSupervisorChildFailure(ctx, req, nil, bad)
		if err != nil {
			return childRoute{}, err
		}
		return childRoute{Escalation: &HoldEscalation{
			Findings: bad, Recommendation: "スーパーバイザーでは対処できなかった。オーナーが判断してください。", ActionID: id, FailedQuestion: true,
		}}, nil
	}
	req.Output = *out
	if out.Hand == HandAnswer {
		if p.Prepare != nil {
			if perr := p.Prepare(out.Answers); perr != nil {
				hand := HandAnswer
				reason := fmt.Sprintf("%s（%v）", supervisorAnswerNotReturnedHead, perr)
				id, err := s.recordSupervisorChildFailure(ctx, req, &hand, reason)
				if err != nil {
					return childRoute{}, err
				}
				return childRoute{Reason: supervisorAnswerNotReturnedHead, Escalation: &HoldEscalation{
					Findings: reason, Recommendation: "接続ツールへ答えを返せなかった。オーナーが判断してください。", ActionID: id,
				}}, nil
			}
		}
		_, executed, err := s.recordSupervisorAnswer(ctx, req, p.Questions)
		if err != nil {
			return childRoute{}, err
		}
		if !executed {
			// 判断の間に課題が着手中でなくなった。答えを返さず、保留も作らない（保留を作る入口が、着手中でない課題には作らない）。
			return childRoute{}, nil
		}
		return childRoute{Answered: true, Answers: out.Answers}, nil
	}
	id, executed, err := s.recordSupervisorChildEscalate(ctx, req)
	if err != nil {
		return childRoute{}, err
	}
	if !executed {
		// 判断の間に課題が着手中でなくなった。保留は作れない（保留を作る入口が、着手中でない課題には作らない）ので、断りの記録だけを残す。
		return childRoute{}, nil
	}
	return childRoute{Escalation: &HoldEscalation{Findings: out.Findings, Recommendation: out.Recommendation, ActionID: id}}, nil
}

// callChildQuestionJudgment は、保留を作る前の子の問い t について入力を組み立て、判断点 SV を 1 回呼ぶ。選べる手は answer と escalate。
func (s *Store) callChildQuestionJudgment(ctx context.Context, p childRouteParams, t supervisorTarget) (svCall, error) {
	offered := SupervisorHandsFor(StopKindChildQuestion)
	sections, err := s.buildSupervisorSections(ctx, p.Decl, t, offered)
	if err != nil {
		return svCall{}, err
	}
	return s.invokeSupervisorJudgment(ctx, p.Decl, p.Judgment, p.CycleID, p.Cycle, t.ChallengeID, t.TaskID, sections, offered)
}

// formatChildQuestionHoldReason は、判断点を呼べなかった理由を、保留の問いの先頭へ付けた文にする。
func formatChildQuestionHoldReason(reason, question string) string {
	if strings.TrimSpace(reason) == "" {
		return question
	}
	return reason + "\n\n" + question
}
