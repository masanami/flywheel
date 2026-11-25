package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// docs/features/chat-session.md「印の検査と記録の「発言」」「会話を課題に紐づける」「費用」の core の口。
// 実ストア（t.TempDir() の SQLite）で確かめる。

// nextChatReply は進行中の返事があれば終え（累計 total）、次の発言を送って返事を進行中にする。
func (f *chatFix) nextChatReply(t *testing.T, total float64) {
	t.Helper()
	f.finish(t, total)
	turn, err := f.s.PostChatMessage(context.Background(), chatOwnerText, nil)
	must(t, err)
	f.turn = turn
}

// finish はいまの返事を、費用の累計 total で終える。
func (f *chatFix) finish(t *testing.T, total float64) {
	t.Helper()
	must(t, f.s.FinishChatReplyWithTotal(context.Background(), f.turn.ReplyMessageID, ChatStateDone, &total))
}

func (f *chatFix) sessionMark() *ChatSessionMark {
	return &ChatSessionMark{Session: f.turn.EngineSession}
}

// createByChat は印のある create をする。
func (f *chatFix) createByChat(t *testing.T, title string) string {
	t.Helper()
	c, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Chat: f.sessionMark()})
	must(t, err)
	return c.ID
}

type chatLinkRow struct {
	challenge int64
	action    string
	message   int64
	createdAt string
}

func chatLinks(t *testing.T, s *Store) []chatLinkRow {
	t.Helper()
	out := []chatLinkRow{}
	must(t, s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return forEachRow(context.Background(), tx, `SELECT challenge_id, action, message_id, created_at FROM chat_link ORDER BY id`, nil, func(rows *sql.Rows) error {
			var r chatLinkRow
			if err := rows.Scan(&r.challenge, &r.action, &r.message, &r.createdAt); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	}))
	return out
}

func requireOneChatLink(t *testing.T, f *chatFix, id, action string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	links := chatLinks(t, f.s)
	if len(links) != 1 || links[0].challenge != cid || links[0].action != action || links[0].message != f.turn.ReplyMessageID {
		t.Fatalf("chat_link = %+v, want one {challenge %d, %s, message %d}", links, cid, action, f.turn.ReplyMessageID)
	}
}

func specCost(t *testing.T, s *Store, id string) float64 {
	t.Helper()
	d, err := s.GetChallenge(context.Background(), id)
	must(t, err)
	return d.SpecCostUSD
}

// 受入基準: 返事の中で問いに答えていないとき、記録の「発言」は最後のオーナーの発言の文面と同じ。2 つ答えたときは、
// 発言の文面に 2 つの問いの文と答えを答えた順につなげる。許可の確認・前の返事の問いは含めない。
func TestChatQuote_AppendsTheAnsweredQuestionsInAnswerOrder(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	// 時刻を固定しても、答えた順は崩れない。
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	s.now = func() time.Time { return at }
	f := newChatFix(t, s)

	ask := func(requestID, question string) int64 {
		return mustPrompt(t, s, f.turn.ReplyMessageID, ChatPromptRequest{RequestID: requestID, Kind: ChatPromptQuestion, Tool: "AskUserQuestion", Target: "q",
			Questions: []ChatQuestion{{Question: question, Options: []ChatQuestionOption{{Label: "承認する"}}}}})
	}
	answer := func(id int64, question, a string) {
		_, err := s.AnswerChatPrompt(context.Background(), id, ChatPromptAnswer{Answers: map[string]string{question: a}})
		must(t, err)
	}
	answer(ask("old", "前の返事の問い"), "前の返事の問い", "前の答え")
	f.nextChatReply(t, 0.1)

	id := createAndAdvance(t, s, StatusUnclassified).ID
	withdraw := func(cid string) string {
		prev, err := s.PrepareWithdraw(context.Background(), cid, "x")
		must(t, err)
		_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: cid, ExpectedVersion: prev.Version, Reason: "x"}, f.mark(t, cid))
		must(t, err)
		quote, _ := lastAfter(t, s, cid)["relay_quote"].(string)
		return quote
	}
	if got := withdraw(id); got != chatOwnerText {
		t.Fatalf("quote without questions = %q, want the owner message only", got)
	}

	q1 := ask("r1", "問い A")
	q2 := ask("r2", "問い B")
	perm := mustPrompt(t, s, f.turn.ReplyMessageID, permissionRequest("p1"))
	_, err := s.AnswerChatPrompt(context.Background(), perm, ChatPromptAnswer{Decision: ChatDecisionAllow})
	must(t, err)
	ask("pending", "答えていない問い")
	answer(q2, "問い B", "答え B")
	answer(q1, "問い A", "承認する")

	want := chatOwnerText + "\n\n問い: 問い B\n答え: 答え B\n\n問い: 問い A\n答え: 承認する"
	if got := withdraw(createAndAdvance(t, s, StatusUnclassified).ID); got != want {
		t.Errorf("quote = %q, want %q", got, want)
	}
}

// 受入基準: 印のある create・ingest が成立すると、その会話と作った課題の紐づけが 1 つできる。印の検査に通らない
// 作成と取り込みは、何も書かない。
func TestChatLink_Create(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	f := newChatFix(t, s)
	id := f.createByChat(t, "欄から")
	requireOneChatLink(t, f, id, "create")

	f.finish(t, 0.1)
	for name, mark := range map[string]*ChatSessionMark{"返事が進行中でない": f.sessionMark(), "空の印": {}, "無い会話": {Session: "no-such"}} {
		_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "x", Chat: mark})
		if !errors.Is(err, ErrChatMarkInvalid) {
			t.Errorf("%s: error = %v, want ErrChatMarkInvalid", name, err)
		}
	}
	if n := countChallenges(t, s); n != 1 {
		t.Errorf("challenges = %d, want 1（何も書かない）", n)
	}
	if links := chatLinks(t, s); len(links) != 1 {
		t.Errorf("chat_link = %+v, want 1", links)
	}
	// 印の無い作成は紐づけない。
	_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "ターミナル"})
	must(t, err)
	if links := chatLinks(t, s); len(links) != 1 {
		t.Errorf("chat_link after a plain create = %+v, want 1", links)
	}
}

func TestChatLink_IngestSelected(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	f := newChatFix(t, s)
	in := IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/q#7"}, Upstream: up, Chat: f.sessionMark()}
	res, err := s.IngestSelected(context.Background(), ChannelCLI, in)
	must(t, err)
	requireOneChatLink(t, f, res.Ingested[0].ChallengeID, "ingest")

	f.finish(t, 0.1)
	in.Specs = []string{"o/r#1"}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, in); !errors.Is(err, ErrChatMarkInvalid) {
		t.Errorf("ingest with a finished reply: error = %v, want ErrChatMarkInvalid", err)
	}
	if n := countChallenges(t, s); n != 1 {
		t.Errorf("challenges = %d, want 1", n)
	}
}

// 受入基準: 印のある approve・reject が成立すると紐づく。同じ組の 2 回目からは足さず、最初の時刻と操作の種類が残る。
func TestChatLink_ApproveAndReject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		decision ApprovalDecision
		action   string
	}{{ApprovalDecisionApproved, "approve"}, {ApprovalDecisionRejected, "reject"}} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
			insertPlanRowForTest(t, s, c.ID, 1, "x")
			f := newChatFix(t, s)
			prev, err := s.PrepareApproval(context.Background(), c.ID)
			must(t, err)
			req := ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: tc.decision}
			if tc.decision == ApprovalDecisionRejected {
				reason := "no"
				req.Reason = &reason
			}
			_, _, err = s.ExecuteApproval(context.Background(), req, f.mark(t, c.ID))
			must(t, err)
			requireOneChatLink(t, f, c.ID, tc.action)
		})
	}
}

// 受入基準: 不可逆操作の承認・差し戻しも、その操作の課題に紐づく。宣言に無い種類の承認は断られ、紐づかない。
func TestChatLink_OperationApproval(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	ext := createPendingOperation(t, s, c.ID, "external_send")
	f := newChatFix(t, s)
	if err := approveOp(t, s, ext.ID, f.mark(t, ext.ID)); !errors.Is(err, ErrChatNotApprovable) {
		t.Fatalf("error = %v, want ErrChatNotApprovable", err)
	}
	if links := chatLinks(t, s); len(links) != 0 {
		t.Fatalf("chat_link after a refused approval = %+v, want none", links)
	}
	writeChatDecl(t, s, `{"chat_approvable_operations": ["external_send"]}`)
	must(t, approveOp(t, s, ext.ID, f.mark(t, ext.ID)))
	requireOneChatLink(t, f, c.ID, "approve")
}

// 受入基準: 印のある answer が成立すると紐づく（保留への回答と、遷移しない計画の問いへの回答）。
func TestChatLink_Answer(t *testing.T) {
	t.Parallel()
	t.Run("保留への回答", func(t *testing.T) {
		t.Parallel()
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, StatusVerifying)
		_, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "uncertain", Question: sp("why?")})
		must(t, err)
		f := newChatFix(t, s)
		prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
		must(t, err)
		_, _, err = s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because"}, f.mark(t, c.ID))
		must(t, err)
		requireOneChatLink(t, f, c.ID, "answer")
	})
	t.Run("計画の問いへの回答（状態を変えない）", func(t *testing.T) {
		t.Parallel()
		pa := newPAFixture(t, 2, true)
		f := newChatFix(t, pa.s)
		_, err := pa.answer(t, func(t *testing.T, id string) Attestation { return f.mark(t, id) }, choiceItem(1, 2))
		must(t, err)
		requireOneChatLink(t, f, pa.id, "answer")
	})
}

// 受入基準: 印のある withdraw・budget が成立すると紐づく。2 回目の操作では足さず最初の値が残る。
func TestChatLink_BudgetThenWithdrawKeepsTheFirstLink(t *testing.T) {
	t.Parallel()
	d := newDelegateFixture(t)
	s := d.s
	id := d.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	f := newChatFix(t, s)
	at := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	prev, err := s.PrepareBudget(context.Background(), id, d.agent, 80, nil)
	must(t, err)
	_, err = s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion, ImplUSD: 80}, f.mark(t, id))
	must(t, err)
	requireOneChatLink(t, f, id, "budget")
	first := chatLinks(t, s)[0]

	at = at.Add(time.Hour)
	lprev, err := s.PrepareBudgetLimit(context.Background(), id, d.agent, 500)
	must(t, err)
	limit := 500.0
	_, err = s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: lprev.Version, PlanVersion: lprev.PlanVersion, LimitUSD: &limit, Agent: d.agent}, f.mark(t, id))
	must(t, err)
	wprev, err := s.PrepareWithdraw(context.Background(), id, "x")
	must(t, err)
	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: id, ExpectedVersion: wprev.Version, Reason: "x"}, f.mark(t, id))
	must(t, err)
	if links := chatLinks(t, s); len(links) != 1 || links[0] != first {
		t.Errorf("chat_link = %+v, want only the first %+v", links, first)
	}
}

func TestChatLink_BudgetLimitAloneAndWithdrawAlone(t *testing.T) {
	t.Parallel()
	d := newDelegateFixture(t)
	id := d.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	f := newChatFix(t, d.s)
	limit := 500.0
	prev, err := d.s.PrepareBudgetLimit(context.Background(), id, d.agent, limit)
	must(t, err)
	_, err = d.s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion, LimitUSD: &limit, Agent: d.agent}, f.mark(t, id))
	must(t, err)
	requireOneChatLink(t, f, id, "budget")

	s := newStoreForTest(t)
	c := createAndAdvance(t, s, StatusUnclassified)
	g := newChatFix(t, s)
	wprev, err := s.PrepareWithdraw(context.Background(), c.ID, "x")
	must(t, err)
	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: c.ID, ExpectedVersion: wprev.Version, Reason: "x"}, g.mark(t, c.ID))
	must(t, err)
	requireOneChatLink(t, g, c.ID, "withdraw")
}

// 受入基準: 失敗した操作・印の検査に通らない操作・印の無い --relayed の記録は紐づけない。
func TestChatLink_FailedRefusedAndTerminalRelayedDoNotLink(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	f := newChatFix(t, s)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	must(t, err)
	// 版の食い違いで失敗する。
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version + 1, Decision: ApprovalDecisionApproved}, f.mark(t, c.ID)); err == nil {
		t.Fatal("approve with a stale version succeeded")
	}
	// 印の検査に通らない。
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		f.markOf(t, "00000000-0000-4000-8000-000000000000", c.ID)); !errors.Is(err, ErrChatMarkInvalid) {
		t.Fatalf("error = %v, want ErrChatMarkInvalid", err)
	}
	// 印の無い --relayed の記録。
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationRelayed, actor: "alice", evidence: RelayEvidence{Session: "s", Quote: "q"}}, "s", c.ID)
	must(t, err)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved}, att)
	must(t, err)
	if links := chatLinks(t, s); len(links) != 0 {
		t.Errorf("chat_link = %+v, want none", links)
	}
}

// 受入基準「費用」: 紐づけまでの返事の費用が仕様づくりの費用になり、紐づいた後の返事は次の紐づけまで数えない。
func TestChatSpecCost_IsAllocatedAtEachLink(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	f := newChatFix(t, s)
	f.nextChatReply(t, 0.10) // 1 回目 0.10
	f.nextChatReply(t, 0.30) // 2 回目 0.20
	first := f.createByChat(t, "1 つ目")
	// 紐づけを起こした返事が、後で費用を記録して終わっても、その課題に入る。
	f.nextChatReply(t, 0.70) // 3 回目 0.40
	if got := specCost(t, s, first); got != 0.70 {
		t.Fatalf("first spec cost = %v, want 0.70 (replies 1-3)", got)
	}
	f.nextChatReply(t, 1.50) // 4 回目 0.80
	if got := specCost(t, s, first); got != 0.70 {
		t.Fatalf("first spec cost after reply 4 = %v, want 0.70", got)
	}
	second := f.createByChat(t, "2 つ目")
	// 同じ返事の中の 2 つ目の紐づけには、費用を数えない。
	third := f.createByChat(t, "3 つ目")
	f.finish(t, 3.10) // 5 回目 1.60
	if got := specCost(t, s, second); got != 2.40 {
		t.Errorf("second spec cost = %v, want 2.40 (replies 4-5)", got)
	}
	if got := specCost(t, s, third); got != 0 {
		t.Errorf("third spec cost = %v, want 0", got)
	}
	if got := specCost(t, s, first); got != 0.70 {
		t.Errorf("first spec cost = %v, want unchanged 0.70", got)
	}

	// 課題の詳細の紐づいた会話: 始めた時刻・最初の発言の先頭・その課題に数えた費用。
	d, err := s.GetChallenge(context.Background(), second)
	must(t, err)
	chat, err := s.ReadChat(context.Background())
	must(t, err)
	if len(d.ChatSessions) != 1 || d.ChatSessions[0].EngineSession != f.turn.EngineSession || d.ChatSessions[0].CostUSD != 2.40 ||
		d.ChatSessions[0].FirstMessage != chatOwnerText || !d.ChatSessions[0].StartedAt.Equal(chat.Session.StartedAt) {
		t.Errorf("chat sessions = %+v", d.ChatSessions)
	}
	d, err = s.GetChallenge(context.Background(), third)
	must(t, err)
	if len(d.ChatSessions) != 1 || d.ChatSessions[0].CostUSD != 0 {
		t.Errorf("third chat sessions = %+v, want one with cost 0", d.ChatSessions)
	}
}

// 受入基準: 紐づいた後の返事で同じ課題を approve すると、その間の返事の費用も入り、紐づけは 1 つのまま。
func TestChatSpecCost_SecondOperationOnTheSamePairAllocates(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	f := newChatFix(t, s)
	id := f.createByChat(t, "t")
	f.nextChatReply(t, 0.10) // 1 回目 0.10
	f.nextChatReply(t, 0.30) // 2 回目 0.20
	cid, _ := parseChallengeID(id)
	setChallengeStatus(t, s, cid, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, id, 1, "x")
	must(t, approvePlanForTest(t, s, id, f.mark(t, id)))
	f.finish(t, 0.70) // 3 回目 0.40
	if got := specCost(t, s, id); got != 0.70 {
		t.Errorf("spec cost = %v, want 0.70", got)
	}
	if links := chatLinks(t, s); len(links) != 1 || links[0].action != "create" {
		t.Errorf("chat_link = %+v, want one create", links)
	}
}

// 受入基準: 仕様づくりの費用は、実装の枠・レビュー対応の枠・上限・見込みの計算と、サイクルの費用に入らない。
func TestChatSpecCost_IsNotCountedInBudgetsOrCycles(t *testing.T) {
	t.Parallel()
	d := newDelegateFixture(t)
	s := d.s
	ctx := context.Background()
	id := d.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	cyc := d.cycle(t, 300)
	cyid, _ := parseCycleID(cyc)
	cid, _ := parseChallengeID(id)

	type snapshot struct {
		budget      ChallengeBudget
		detail      ChallengeDetailBudget
		implLeft    float64
		reviewLeft  float64
		cycleMicros int64
	}
	read := func() snapshot {
		var out snapshot
		b, err := s.GetChallengeBudget(ctx, id, d.agent)
		must(t, err)
		out.budget = *b
		det, err := s.GetChallenge(ctx, id)
		must(t, err)
		out.detail = det.Budget
		p, err := s.PrepareBudget(ctx, id, d.agent, 50, nil)
		must(t, err)
		out.implLeft, out.reviewLeft = p.ImplRemainingUSD, p.ReviewRemainingUSD
		must(t, s.db.Read(ctx, func(tx *sql.Tx) error {
			var err error
			out.cycleMicros, err = cycleSpentMicros(ctx, tx, cyid)
			return err
		}))
		return out
	}
	before := read()

	f := newChatFix(t, s)
	// 紐づけだけを起こす（枠を変えない増額）。
	prev, err := s.PrepareBudget(ctx, id, d.agent, 50, nil)
	must(t, err)
	_, err = s.ExecuteBudget(ctx, BudgetRequest{ChallengeID: id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion, ImplUSD: 50}, f.mark(t, id))
	must(t, err)
	f.finish(t, 1000)
	if got := specCost(t, s, id); got != 1000 {
		t.Fatalf("spec cost = %v, want 1000", got)
	}
	var charged int64
	must(t, s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM chat_message WHERE charged_challenge_id = ?`, cid).Scan(&charged)
	}))
	if charged != 1 {
		t.Fatalf("charged replies = %d, want 1", charged)
	}

	after := read()
	if after.implLeft != before.implLeft || after.reviewLeft != before.reviewLeft {
		t.Errorf("remaining = %v/%v, want %v/%v", after.implLeft, after.reviewLeft, before.implLeft, before.reviewLeft)
	}
	if after.budget.LimitUSD != before.budget.LimitUSD || after.budget.ImplEstimateUSD != before.budget.ImplEstimateUSD ||
		after.budget.ReviewEstimateUSD != before.budget.ReviewEstimateUSD || after.budget.ImplSpentUSD != before.budget.ImplSpentUSD ||
		after.budget.ReviewSpentUSD != before.budget.ReviewSpentUSD || after.budget.Tasks[0].ImplRemainingUSD != before.budget.Tasks[0].ImplRemainingUSD {
		t.Errorf("budget = %+v, want %+v", after.budget, before.budget)
	}
	if *after.detail.LimitUSD != *before.detail.LimitUSD || *after.detail.EstimateUSD != *before.detail.EstimateUSD {
		t.Errorf("detail budget = %v/%v, want %v/%v", *after.detail.LimitUSD, *after.detail.EstimateUSD, *before.detail.LimitUSD, *before.detail.EstimateUSD)
	}
	if after.cycleMicros != before.cycleMicros {
		t.Errorf("cycle spent = %d, want %d", after.cycleMicros, before.cycleMicros)
	}
	ended, err := s.EndCycle(ctx, cyc, CycleResultCompleted)
	must(t, err)
	if ended.SpentUSD != 0 {
		t.Errorf("cycle spent = %v, want 0", ended.SpentUSD)
	}
}
