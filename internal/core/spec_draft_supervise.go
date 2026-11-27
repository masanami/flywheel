package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// このファイルは、下書きの問い（止まり方 spec_question）をスーパーバイザーへ回す専用の経路を持つ
// （docs/features/request-to-plan-s2b.md「下書きの問いを、スーパーバイザーへ回す」「答えの検査」「答えを記録へ写す」）。
// 下書きづくりの実行の終わりを読んだ後、記録を書く前に判断点 SV を 1 回呼び、答え・問いの一覧・記録の状態・実行を終わった
// 記録にすること・対処の記録・作業ログを、1 つの書き込みで行う。呼んでいる間は何も書かないので、中断されても次の状態の確認が
// 同じ終わりを扱い直す。委譲先の問いの経路（supervisor_child_question.go）は使わない。

// specSupervision は、判断点を呼んだ下書きの問いの扱い。
type specSupervision struct {
	req supervisorHandRequest
	// action は残す対処の記録（結果は executed か failed。判断の間に状態が変わっていれば rejected に置き換える）。
	action SupervisorActionInput
	// answers は記録の答えの一覧へ足す答え、remaining はオーナーへ上げる問い（空なら保留を作らない）。
	answers   []SpecDraftAnswer
	remaining []SpecDraftQuestion
}

// specSupervisorMayRun は、下書きの問いでスーパーバイザーを呼ぶ呼び出し元の条件（宣言の supervisor が真・判断点を起動する口がある・
// サイクルの中の状態の確認。対処の記録はサイクルの ID が必須）。
func specSupervisorMayRun(in ContractPollInput) bool {
	_, inCycle := parseCycleID(in.CycleID)
	return in.AgentDecl != nil && in.AgentDecl.Supervisor && in.Judgment != nil && inCycle
}

// superviseSpecQuestions は残った問い questions について判断点 SV を 1 回呼び、出力を検査して、扱いを返す。記録も保留も書かない。
// nil（誤りなし）は、呼べなかった（起動できない・サイクルの費用の上限・利用上限）ので、全部の問いをそのまま上げる（対処の記録は残さない）。
// 中断・ほかの実行と重なったときは errSpecSupervisionInterrupted か、そのエラーを返す（呼び出し元は何も書かない）。
func (s *Store) superviseSpecQuestions(ctx context.Context, in ContractPollInput, end *specDraftEnd, questions []SpecDraftQuestion) (*specSupervision, error) {
	if err := in.Judgment.Available(ctx); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, nil
	}
	cid := end.run.ChallengeID
	offered := SupervisorHandsFor(StopKindSpecQuestion)
	sections, err := s.buildSpecSupervisorSections(ctx, in, end.draft, cid, questions, offered)
	if err != nil {
		return nil, err
	}
	call, err := s.invokeSupervisorJudgment(ctx, in.AgentDecl, in.Judgment, in.CycleID, in.Cycle, cid, nil, sections, offered)
	if errors.Is(err, ErrBudgetExceeded) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if call.NotStarted != nil {
		return nil, nil
	}
	if call.Run.Result == RunResultInterrupted || (ctx.Err() != nil && call.Run.Result != RunResultSucceeded) {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, errSpecSupervisionInterrupted
	}
	if call.Run.RateLimited {
		return nil, nil
	}

	// 判定した後は、呼び出し元の ctx の取り消しに関わらず記録する。
	ctx = context.WithoutCancel(ctx)
	scope := specBasisScopeFor(in.ConnDecl, end.draft.Repo)
	out, flaw := inspectSpecQuestionOutput(call.Run, call.Offered, questions, func(path string) bool {
		return specBasisCounts(s.workspace, scope, path)
	})
	out, bad := s.finishSupervisorVerify(ctx, call, out, flaw)

	sv := &specSupervision{
		req: supervisorHandRequest{CycleID: in.CycleID, Target: supervisorTarget{ChallengeID: cid, StopKind: StopKindSpecQuestion},
			RunID: call.Run.RunID, Decl: in.AgentDecl},
		remaining: questions,
	}
	if bad != "" {
		// 検査に 1 件でも落ちたら、通った答えも捨てて、全部の問いを上げる。
		sv.action = sv.input("", bad, "", nil, ActionFailed)
		return sv, nil
	}
	if out.Hand != HandAnswer {
		sv.action = sv.input(out.Hand, out.Findings, out.Note, out.Basis, ActionExecuted)
		return sv, nil
	}
	var basis []SupervisorBasis
	var answered []SupervisorQuestion
	sv.remaining = nil
	for i, a := range out.Answers {
		q := questions[i]
		if a == nil {
			sv.remaining = append(sv.remaining, q)
			continue
		}
		places := make([]SpecDraftBasis, len(a.Basis))
		for k, b := range a.Basis {
			places[k] = SpecDraftBasis(b)
		}
		sv.answers = append(sv.answers, SpecDraftAnswer{Kind: SpecDraftAnswerKindAnswer, Attempt: end.draft.Attempts,
			Question: q.Question, Answer: a.Answer, By: supervisorActorName, Basis: places})
		answered = append(answered, SupervisorQuestion{Question: q.Question, Answer: a.Answer, Basis: a.Basis})
		basis = append(basis, a.Basis...)
	}
	sv.action = sv.input(HandAnswer, out.Findings, out.Note, basis, ActionExecuted)
	sv.action.Questions = answered
	return sv, nil
}

// errSpecSupervisionInterrupted は、判断点の実行が中断で終わったことを表す内部の sentinel。
var errSpecSupervisionInterrupted = errors.New("core: the supervisor run for the spec questions was interrupted")

// input は対処の記録の入力（保留もタスクも指さない）。
func (sv *specSupervision) input(hand SupervisorHand, findings, note string, basis []SupervisorBasis, result SupervisorActionResult) SupervisorActionInput {
	return SupervisorActionInput{CycleID: sv.req.CycleID, ChallengeID: formatChallengeID(sv.req.Target.ChallengeID), RunID: sv.req.RunID,
		StopKind: StopKindSpecQuestion, Hand: hand, Findings: findings, Note: note, Basis: basis, Result: result}
}

// writeSpecSupervisionAction は対処の記録と、答えた件数・上げた件数を足した作業ログを、tx の中で書く。
func (s *Store) writeSpecSupervisionAction(ctx context.Context, tx *sql.Tx, req supervisorHandRequest, in SupervisorActionInput, answered, escalated int) error {
	req.LogExtra = map[string]any{"answered": answered, "escalated": escalated}
	_, err := s.writeSupervisorAction(ctx, tx, req, in)
	return err
}

// recordSpecSupervision は、扱い sv を 1 つの書き込みで写す。全部に答えたら保留を作らずに記録を実行待ちへ戻し（requeued が真）、
// 残りがあれば残りだけの問いの保留を作る（held が真）。書き込みの中で読み直した記録が実行中でないか、課題が分類済でなければ、
// 答えを捨てて何も変えず、断りの記録（rejected・state_changed）だけを残す。causeRunID は実行の終わりを読んだ実行。
func (s *Store) recordSpecSupervision(ctx context.Context, causeRunID string, run *contractRunRow, sv *specSupervision) (held, requeued bool, err error) {
	if len(sv.remaining) == 0 {
		err = s.requeueSpecDraft(ctx, causeRunID, run, sv)
		requeued = err == nil
	} else {
		_, err = s.transitionAsInvoker(ctx, causeRunID, formatChallengeID(run.ChallengeID), OpHold,
			func(ctx context.Context, tc *transitionCtx) error {
				if _, err := requireRunningSpecDraft(ctx, tc.tx, tc.id); err != nil {
					return err
				}
				if err := s.endSpecContractRun(ctx, tc.tx, run); err != nil {
					return err
				}
				if err := s.writeSpecSupervisionAction(ctx, tc.tx, sv.req, sv.action, len(sv.answers), len(sv.remaining)); err != nil {
					return err
				}
				return raiseSpecDraftHold(ctx, tc, specDraftHold{Reason: SpecDraftHoldQuestion, Questions: sv.remaining,
					AddAnswers: sv.answers, SupervisorAnswered: len(sv.answers)})
			})
		held = err == nil
	}
	if errors.Is(err, errSpecDraftHoldNotEligible) || errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		rejected := sv.action
		rejected.Result, rejected.RejectReason, rejected.Questions = ActionRejected, RejectStateChanged, nil
		return false, false, classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
			return s.writeSpecSupervisionAction(ctx, tx, sv.req, rejected, 0, 0)
		}))
	}
	if err != nil {
		return false, false, classifyReadWriteErr(err)
	}
	return held, requeued, nil
}

// requeueSpecDraft は、全部の問いに答えた記録を、答えを足して実行待ちへ戻す。課題の状態は変えない。
func (s *Store) requeueSpecDraft(ctx context.Context, causeRunID string, run *contractRunRow, sv *specSupervision) error {
	runID, ok := parseRunID(causeRunID)
	if !ok {
		return ErrValidation
	}
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	return s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runID, func(tx *sql.Tx, rec *activityRecorder) error {
		ch, err := loadChallenge(ctx, tx, run.ChallengeID)
		if err != nil {
			return err
		}
		if ch.Status != StatusClassified {
			return errSpecDraftHoldNotEligible
		}
		d, err := requireRunningSpecDraft(ctx, tx, run.ChallengeID)
		if err != nil {
			return err
		}
		if err := s.endSpecContractRun(ctx, tx, run); err != nil {
			return err
		}
		if err := updateSpecDraft(ctx, tx, run.ChallengeID, specDraftUpdate{State: SpecDraftStatePending, Attempts: d.Attempts,
			Answers: append(slices.Clone(d.Answers), sv.answers...)}, rec.at); err != nil {
			return err
		}
		return s.writeSpecSupervisionAction(ctx, tx, sv.req, sv.action, len(sv.answers), 0)
	})
}

// buildSpecSupervisorSections は下書きの問いの判断点の入力（データの区画）を組み立てる。読み取りだけ。下書きの本文は入れない。
// 区画の名前は、指示文（sv.md）が名前で指している。
func (s *Store) buildSpecSupervisorSections(ctx context.Context, in ContractPollInput, d *SpecDraft, cid int64,
	questions []SpecDraftQuestion, offered []SupervisorHand) ([]JudgmentDataSection, error) {
	var ch *Challenge
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		ch, err = loadChallenge(ctx, tx, cid)
		return err
	})); err != nil {
		return nil, err
	}
	repoBase := ""
	if repo, _ := in.ConnDecl.findConnectorRepo(d.Repo); repo != nil {
		repoBase = repo.Slots.Base
	}
	sections := []JudgmentDataSection{
		{Label: "止まり方", Content: string(StopKindSpecQuestion)},
		{Label: "課題", Content: strings.TrimRight(fmt.Sprintf("題名: %s\n説明:\n%s", oneLine(ch.Title), ch.Description), "\n")},
		{Label: "下書き", Content: fmt.Sprintf("下書きの場所: %s\n対象のリポジトリ: %s\n元のクローンの場所: %s",
			d.Path, oneLine(d.Repo), repoBase)},
		{Label: "残った問い", Content: formatSpecSupervisorQuestions(questions)},
	}
	answers, notes := formatSpecSupervisorAnswers(d.Answers)
	if answers != "" {
		sections = append(sections, JudgmentDataSection{Label: "これまでの答え", Content: answers})
	}
	if notes != "" {
		sections = append(sections, JudgmentDataSection{Label: "オーナーの添え書き", Content: notes})
	}
	var hands strings.Builder
	for _, h := range offered {
		fmt.Fprintf(&hands, "- %s: %s\n", h, supervisorHandDescription(StopKindSpecQuestion, h))
	}
	sections = append(sections, JudgmentDataSection{Label: "選べる手の一覧", Content: strings.TrimRight(hands.String(), "\n")})
	return append(sections, memoryIndexSections(s.workspace, in.AgentDecl)...), nil
}

// formatSpecSupervisorQuestions は区画「残った問い」の本文。問いは Q1 から記録の並びの順。選択肢の見出しに推奨の印は付けない
// （印の付いた文字列を答えにすると、見出しとの完全一致の検査に落ちる）。どの値も 1 行にして、行の形を崩させない。
func formatSpecSupervisorQuestions(questions []SpecDraftQuestion) string {
	var b strings.Builder
	for i, q := range questions {
		fmt.Fprintf(&b, "Q%d. %s\n選択肢:\n", i+1, oneLine(q.Question))
		for j, o := range q.Options {
			fmt.Fprintf(&b, "%d. %s\n", j+1, oneLine(o.Label))
		}
		fmt.Fprintf(&b, "推奨: %d\n推奨の理由: %s\n\n", q.Recommended, oneLine(q.Reason))
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatSpecSupervisorAnswers は答えの一覧を、答え（区画「これまでの答え」）と添え書き（区画「オーナーの添え書き」）の本文に分ける。
// 無いほうは空。
func formatSpecSupervisorAnswers(list []SpecDraftAnswer) (answers, notes string) {
	var a, n strings.Builder
	// 複数の行の値は、2 行目から字下げして、項目の中に収める。
	item := func(v string) string { return strings.ReplaceAll(strings.Trim(v, "\n"), "\n", "\n  ") }
	for _, e := range list {
		if e.Kind == SpecDraftAnswerKindNote {
			fmt.Fprintf(&n, "- %s\n", item(e.Answer))
			continue
		}
		fmt.Fprintf(&a, "- 問い: %s\n  答え: %s\n  答えた者: %s\n", item(e.Question), item(e.Answer), oneLine(e.By))
	}
	return strings.TrimRight(a.String(), "\n"), strings.TrimRight(n.String(), "\n")
}

// inspectSpecQuestionOutput は下書きの問い questions への判断点の実行の結果を検査する。inspectSupervisorOutput の検査
// （null でない答えが 1 件以上を含む）に加え、手が answer のとき、答えの件数が問いの数と同じこと・null でない答えが、その問いの
// 選択肢の見出しのどれかと完全に一致すること・その答えに、counts が根拠として数える場所が 1 件以上あることを確かめる。
// 不備は BadOutput の flaw。
func inspectSpecQuestionOutput(run *RunJudgmentResult, offered []SupervisorHand, questions []SpecDraftQuestion,
	counts func(path string) bool) (*SVOutput, *svOutputFlaw) {
	out, flaw := inspectSupervisorOutput(run, offered)
	if flaw != nil || out.Hand != HandAnswer {
		return out, flaw
	}
	if err := checkSpecAnswers(out.Answers, questions, counts); err != nil {
		return nil, &svOutputFlaw{Reason: fmt.Sprintf("スーパーバイザーの答えに不備があった（%v）", err), BadOutput: true}
	}
	return out, nil
}

func checkSpecAnswers(answers []*SVAnswer, questions []SpecDraftQuestion, counts func(path string) bool) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("答えの件数 %d が問いの数 %d と合わない", len(answers), len(questions))
	}
	for i, a := range answers {
		if a == nil {
			continue
		}
		if !slices.ContainsFunc(questions[i].Options, func(o SpecDraftOption) bool { return o.Label == a.Answer }) {
			return fmt.Errorf("問い %d の答え %q が選択肢にない", i+1, a.Answer)
		}
		if !slices.ContainsFunc(a.Basis, func(b SupervisorBasis) bool { return counts(b.Path) }) {
			return fmt.Errorf("問い %d に、根拠として数えられる場所がない", i+1)
		}
	}
	return nil
}
