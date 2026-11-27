package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// このファイルは、下書きの問いをスーパーバイザーへ回す経路を、実ストアと偽の判断の IF で確かめる。
// テストの名前と注記の AC の番号は、Issue #1382 の受け持ち（親 #1374 の受入基準の並び順）。

const (
	spqRepoBase  = ".flywheel/repos/app"
	spqOtherBase = ".flywheel/repos/other"
	spqDraftOnly = "DRAFT-ONLY-MARKER"
	spqDraftDoc  = "# 下書き\n\n" + spqDraftOnly + "\n\n## 受入基準\n- [ ] 下書きの基準\n"
)

// spqSV は判断点の偽物。下書きの問いの呼び出しを覚え、out を返す。during は呼び出しの最中に行う操作。
type spqSV struct {
	mu          sync.Mutex
	unavailable error
	out         JudgmentLaunchOutput
	during      func()
	calls       []JudgmentLaunchInput
}

func (v *spqSV) Available(context.Context) error { return v.unavailable }

func (v *spqSV) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	if in.Judgment != JudgmentSV {
		return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF-MARKER")}, nil
	}
	v.mu.Lock()
	v.calls = append(v.calls, in)
	out, during := v.out, v.during
	v.mu.Unlock()
	if during != nil {
		during()
	}
	return out, nil
}

type spqFixture struct {
	*sdsFixture
	sv      *spqSV
	id      string
	cid     int64
	draft   string
	repoDir string
}

// spqBase は、対象のリポジトリ app（元のクローンは base）と、対象でないリポジトリ other を宣言した足場を作る。
func spqBase(t *testing.T, base, otherBase string) *spqFixture {
	t.Helper()
	f := &spqFixture{sdsFixture: newSDSFixture(t), sv: &spqSV{}}
	f.agent.Supervisor = true
	app := &f.conn.Repos[len(f.conn.Repos)-1]
	app.Name, app.Slots.Base = "app", base
	other := *app
	other.Name, other.Remote, other.Slots.Base = "other", "o/other", otherBase
	f.conn.Repos = append(f.conn.Repos, other)
	f.repoDir = filepath.Join(f.s.workspace, base)
	f.git.changedPaths = map[string][]string{}
	for rel, body := range map[string]string{
		base + "/docs/a.md": "a", base + "/.git/config": "c", otherBase + "/README.md": "r", "memory/a.md": "m",
		"docs/spec.md": "# 仕様\n\n## 受入基準\n- [ ] 基準\n",
	} {
		writeWorkspaceFile(t, f.s.workspace, rel, body)
	}
	f.sv.out = spqAnswers(spqAnswer("a", base+"/docs/a.md"), spqAnswer("d", "memory/a.md"))
	return f
}

// spqCreate は分類済の課題と、実行待ちの下書きづくりの記録（d の回数・答えを使う）を作る。
func (f *spqFixture) spqCreate(t *testing.T, d SpecDraft) {
	t.Helper()
	f.id = f.classified(t, "spq-title")
	f.cid = mustChallengeID(t, f.id)
	f.draft = specDraftPathFor(f.cid)
	d.Path, d.Repo, d.Reason = f.draft, "app", needsSpecReason
	f.write(t, func(ctx context.Context, tx *sql.Tx) error {
		if err := insertSpecDraft(ctx, tx, f.cid, d, f.s.currentTime()); err != nil {
			return err
		}
		return updateSpecDraft(ctx, tx, f.cid, specDraftUpdate{State: SpecDraftStatePending, Attempts: d.Attempts, Answers: d.Answers}, f.s.currentTime())
	})
}

// start は下書きづくりを始め、下書きを置き、次の状態の確認が問い 2 件の成功を返すようにする。
func (f *spqFixture) start(t *testing.T) {
	t.Helper()
	f.delegate(t, 100)
	writeWorkspaceFile(t, f.s.workspace, f.draft, spqDraftDoc)
	f.client.last = sfOut(ContractStateSucceeded, spqQuestions())
}

func newSPQFixture(t *testing.T, d ...SpecDraft) *spqFixture {
	t.Helper()
	f := spqBase(t, spqRepoBase, spqOtherBase)
	var rec SpecDraft
	if len(d) > 0 {
		rec = d[0]
	}
	f.spqCreate(t, rec)
	f.start(t)
	return f
}

func spqQuestions() string {
	return sfList(sfQuestion("問いの一", []string{"a", "b"}, 2, "理由の一"), sfQuestion("問いの二", []string{"c", "d"}, 1, "理由の二"))
}

func (f *spqFixture) pollInput(cycleID string) ContractPollInput {
	return ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, Git: f.git, Judgment: f.sv, CycleID: cycleID}
}

// poll は新しいサイクル（費用の上限 budget）で状態の確認を 1 回行い、サイクルの ID を返す。
func (f *spqFixture) poll(t *testing.T, mut ...func(*ContractPollInput)) string {
	t.Helper()
	c := f.cycle(t, 100)
	in := f.pollInput(c)
	for _, m := range mut {
		m(&in)
	}
	if _, err := f.s.PollContractRuns(context.Background(), in); err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
	return c
}

func spqAnswer(answer string, paths ...string) map[string]any {
	basis := []any{}
	for _, p := range paths {
		basis = append(basis, map[string]any{"path": p, "locator": "LOC-" + p})
	}
	return map[string]any{"answer": answer, "basis": basis}
}

func spqOutput(hand string, answers []any) JudgmentLaunchOutput {
	b, err := json.Marshal(map[string]any{"hand": hand, "findings": "FINDINGS", "note": "", "basis": []any{},
		"recommendation": "RECOMMEND", "options": []any{}, "conditions": []any{}, "answers": answers})
	if err != nil {
		panic(err)
	}
	return svJSON(string(b))
}

// spqAnswers は手 answer の出力（nil の引数は null の要素）。
func spqAnswers(answers ...any) JudgmentLaunchOutput { return spqOutput("answer", answers) }

func (f *spqFixture) record(t *testing.T) *SpecDraft {
	t.Helper()
	return f.detail(t, f.id).SpecDraft
}

func (f *spqFixture) openHold(t *testing.T) *Hold {
	t.Helper()
	for _, h := range f.detail(t, f.id).Holds {
		if h.Open() {
			return &h
		}
	}
	return nil
}

func (f *spqFixture) actions(t *testing.T) []SupervisorAction {
	t.Helper()
	out, err := f.s.ListSupervisorActionsByChallenge(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *spqFixture) requireCalls(t *testing.T, n int) {
	t.Helper()
	if len(f.sv.calls) != n {
		t.Fatalf("supervisor calls = %d, want %d", len(f.sv.calls), n)
	}
}

// requireQuestionHold は、問い n 件を持つ理由 spec_question の保留になったことを確かめる。
func (f *spqFixture) requireQuestionHold(t *testing.T, n int) *Hold {
	t.Helper()
	d := f.detail(t, f.id)
	sd := d.SpecDraft
	if d.Status != StatusAwaitingHuman || sd.State != SpecDraftStateAwaitingOwner || sd.HoldReason == nil ||
		*sd.HoldReason != SpecDraftHoldQuestion || len(sd.Questions) != n {
		t.Fatalf("status = %s record = %+v, want a spec_question hold with %d questions", d.Status, sd, n)
	}
	if k := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE kind = 'spec' AND ended_at IS NULL`); k != 0 {
		t.Fatalf("open spec runs = %d", k)
	}
	return f.openHold(t)
}

// requireSuperviseLog は作業ログ supervise が 1 件で、after の件数が合うことを確かめる。
func (f *spqFixture) requireSuperviseLog(t *testing.T, answered, escalated float64) {
	t.Helper()
	logs := saLogs(t, f.s, f.id, ActivityActionSupervise)
	if len(logs) != 1 || logs[0]["answered"] != answered || logs[0]["escalated"] != escalated || logs[0]["stop_kind"] != "spec_question" {
		t.Fatalf("supervise logs = %+v, want one with answered %v escalated %v", logs, answered, escalated)
	}
}

func (f *spqFixture) requireNoSuperviseTrace(t *testing.T) {
	t.Helper()
	if a, l := f.actions(t), saLogs(t, f.s, f.id, ActivityActionSupervise); len(a) != 0 || len(l) != 0 {
		t.Fatalf("actions = %+v logs = %+v, want none", a, l)
	}
}

func TestSpecSupervise_CallAndCost(t *testing.T) { // AC-15・AC-16・AC-17・AC-18・AC-28・AC-64・AC-65・AC-66・AC-67・AC-68
	t.Parallel()
	f := newSPQFixture(t)
	if d := f.detail(t, f.id); d.Status != StatusClassified || len(d.Tasks) != 0 {
		t.Fatalf("status = %s tasks = %d, want classified without tasks", d.Status, len(d.Tasks))
	}
	before := f.detail(t, f.id).SpecCostUSD
	c := f.poll(t)
	f.requireCalls(t, 1)
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'SV' AND budget_bucket = 'judgment' AND cycle_id = ?`, cycleDBIDOf(c)); n != 1 {
		t.Errorf("SV runs in the judgment bucket = %d, want 1", n)
	}
	d := f.detail(t, f.id)
	if d.SpecCostUSD != before {
		t.Errorf("spec_cost_usd = %v, want %v", d.SpecCostUSD, before)
	}
	var spent int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		spent, err = cycleSpentMicros(context.Background(), tx, cycleDBIDOf(c))
		return err
	}); err != nil || spent != usdToMicros(0.5) {
		t.Errorf("cycle spent = %d %v, want 0.5 USD", spent, err)
	}
	phases := []CyclePhaseResult{{Phase: CyclePhaseSupervise}}
	if err := f.s.addChildQuestionSuperviseCounts(context.Background(), c, phases); err != nil {
		t.Fatal(err)
	}
	if phases[0].SuperviseProcessed != 1 || phases[0].SuperviseHands[HandAnswer] != 1 {
		t.Errorf("supervise phase = %+v", phases[0])
	}
	if d.SupervisorHandsTaken[HandAnswer] != 1 || d.SupervisorQuestions == nil || len(d.SupervisorQuestions) != 0 {
		t.Errorf("hands taken = %v, supervisor questions = %#v", d.SupervisorHandsTaken, d.SupervisorQuestions)
	}
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		qs, err := executedSupervisorQuestions(context.Background(), tx, f.cid) // 完了の承認の要約が読む口
		if len(qs) != 0 {
			t.Errorf("approval supervisor questions = %+v", qs)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSpecSupervise_NotCalled(t *testing.T) {
	t.Parallel()
	noCall := func(t *testing.T, f *spqFixture, mut ...func(*ContractPollInput)) {
		t.Helper()
		f.poll(t, mut...)
		f.requireCalls(t, 0)
		f.requireNoSuperviseTrace(t)
	}
	t.Run("AC-19・AC-20 supervisor が偽", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.agent.Supervisor = false
		noCall(t, f)
		f.requireQuestionHold(t, 2)
	})
	t.Run("AC-21 attempts が 3", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t, SpecDraft{Attempts: 2})
		noCall(t, f)
		if sd := f.record(t); sd.Attempts != 3 || sd.HoldReason == nil || *sd.HoldReason != SpecDraftHoldAttemptLimit {
			t.Fatalf("record = %+v", sd)
		}
	})
	t.Run("AC-22 実行が失敗", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.client.last = sfOut(ContractStateFailed, spqQuestions())
		noCall(t, f)
		f.requireSpecFailed(t, f.id, 1)
	})
	t.Run("AC-23 下書きが無い", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		if err := os.Remove(filepath.Join(f.s.workspace, f.draft)); err != nil {
			t.Fatal(err)
		}
		noCall(t, f)
		f.requireSpecFailed(t, f.id, 1)
	})
	t.Run("AC-24 元のクローンが書き換えられた", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.git.changedPaths = map[string][]string{f.repoDir: {"new.txt"}}
		noCall(t, f)
		f.requireSpecFailed(t, f.id, 1)
	})
	t.Run("AC-25 問いが 0 件", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.client.last = sfOut(ContractStateSucceeded, "[]")
		noCall(t, f)
		if sd := f.record(t); sd.State != SpecDraftStateDone {
			t.Fatalf("record = %+v", sd)
		}
	})
	t.Run("AC-26 判断点を起動する口が無い", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		noCall(t, f, func(in *ContractPollInput) { in.Judgment = nil })
		f.requireQuestionHold(t, 2)
	})
	t.Run("AC-27 サイクルの ID が無い", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		noCall(t, f, func(in *ContractPollInput) { in.CycleID = "" })
		f.requireQuestionHold(t, 2)
	})
}

func spqLabels(sections []JudgmentDataSection) []string {
	var out []string
	for _, s := range sections {
		if s.Label != "記憶の索引" && !strings.HasPrefix(s.Label, "記憶") {
			out = append(out, s.Label)
		}
	}
	return out
}

func spqSection(sections []JudgmentDataSection, label string) string {
	for _, s := range sections {
		if s.Label == label {
			return s.Content
		}
	}
	return ""
}

func TestSpecSupervise_Sections(t *testing.T) { // AC-29・AC-31・AC-34・AC-36〜AC-47・AC-49
	t.Parallel()
	f := newSPQFixture(t)
	f.poll(t)
	f.requireCalls(t, 1)
	sections := f.sv.calls[0].Sections
	if got, want := spqLabels(sections), []string{"止まり方", "課題", "下書き", "残った問い", "選べる手の一覧"}; !slices.Equal(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for label, wants := range map[string][]string{
		"止まり方": {"spec_question"},
		"課題":   {"spq-title", "DESC-spq-title"},
		"下書き":  {".flywheel/specs/" + strings.ToLower(f.id) + ".md", "対象のリポジトリ: app", "元のクローンの場所: .flywheel/repos/app"},
		"残った問い": {"Q1. 問いの一\n選択肢:\n1. a\n2. b\n推奨: 2\n推奨の理由: 理由の一",
			"Q2. 問いの二\n選択肢:\n1. c\n2. d\n推奨: 1\n推奨の理由: 理由の二"},
	} {
		got := spqSection(sections, label)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("section %s lacks %q:\n%s", label, want, got)
			}
		}
	}
	all := sectionsText(sections)
	if strings.Contains(all, spqDraftOnly) {
		t.Error("the draft body is in the input")
	}
	if q := spqSection(sections, "残った問い"); strings.Contains(q, "（推奨）") {
		t.Errorf("the recommended mark is in the questions:\n%s", q)
	}
	hands := strings.Split(spqSection(sections, "選べる手の一覧"), "\n")
	if len(hands) != 2 || !strings.HasPrefix(hands[0], "- answer: ") || !strings.HasPrefix(hands[1], "- escalate: ") ||
		strings.Contains(hands[0], "委譲先") {
		t.Errorf("hands = %q", hands)
	}
}

func TestSpecSupervise_SectionsWithAnswersAndNotes(t *testing.T) {
	t.Parallel()
	answer := SpecDraftAnswer{Kind: SpecDraftAnswerKindAnswer, Attempt: 1, Question: "前の問い", Answer: "前の答え", By: "owner", Basis: []SpecDraftBasis{}}
	note := SpecDraftAnswer{Kind: SpecDraftAnswerKindNote, Attempt: 1, Answer: "添え書きの文", By: "owner", Basis: []SpecDraftBasis{}}
	t.Run("AC-30・AC-33・AC-35・AC-48 答えと添え書き", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t, SpecDraft{Attempts: 1, Answers: []SpecDraftAnswer{answer, note}})
		f.poll(t)
		f.requireCalls(t, 1)
		sections := f.sv.calls[0].Sections
		want := []string{"止まり方", "課題", "下書き", "残った問い", "これまでの答え", "オーナーの添え書き", "選べる手の一覧"}
		if got := spqLabels(sections); !slices.Equal(got, want) {
			t.Fatalf("labels = %v", got)
		}
		past := spqSection(sections, "これまでの答え")
		for _, w := range []string{"前の問い", "前の答え", "答えた者: owner"} {
			if !strings.Contains(past, w) {
				t.Errorf("past answers lack %q:\n%s", w, past)
			}
		}
		if strings.Contains(past, "添え書きの文") || !strings.Contains(spqSection(sections, "オーナーの添え書き"), "添え書きの文") {
			t.Errorf("note is in the wrong section:\n%s", sectionsText(sections))
		}
	})
	t.Run("AC-32 添え書きだけ", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t, SpecDraft{Attempts: 1, Answers: []SpecDraftAnswer{note}})
		f.poll(t)
		f.requireCalls(t, 1)
		want := []string{"止まり方", "課題", "下書き", "残った問い", "オーナーの添え書き", "選べる手の一覧"}
		if got := spqLabels(f.sv.calls[0].Sections); !slices.Equal(got, want) {
			t.Fatalf("labels = %v", got)
		}
	})
}

func TestSpecSupervise_AnswerLimitIsOnlyForChildQuestions(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T, f *spqFixture, kind StopKind) {
		t.Helper()
		c := f.cycle(t, 100)
		for range supervisorAnswerLimit {
			if _, err := f.s.recordSupervisorAction(context.Background(), SupervisorActionInput{CycleID: c, ChallengeID: f.id,
				StopKind: kind, Hand: HandAnswer, Findings: "f", Result: ActionExecuted}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.s.EndCycle(context.Background(), c, CycleResultCompleted); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("AC-50 下書きの問いの対処は再開の上限に数えない", func(t *testing.T) {
		t.Parallel()
		f := childReportFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		c := f.cycle(t, 100)
		for range supervisorAnswerLimit {
			if _, err := f.s.recordSupervisorAction(context.Background(), SupervisorActionInput{CycleID: c, ChallengeID: id,
				StopKind: StopKindSpecQuestion, Hand: HandAnswer, Findings: "f", Result: ActionExecuted}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.s.EndCycle(context.Background(), c, CycleResultCompleted); err != nil {
			t.Fatal(err)
		}
		f.deleg.result = minorQuestionsOut()
		r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
		f.runWithSV(t, r, id)
		if len(r.svSeen) != 1 {
			t.Fatalf("SV calls = %d, want 1", len(r.svSeen))
		}
	})
	t.Run("AC-51 委譲先の問いの上限に達しても、下書きの問いでは呼ぶ", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		seed(t, f, StopKindChildQuestion)
		f.poll(t)
		f.requireCalls(t, 1)
	})
}

func TestSpecSupervise_Interrupted(t *testing.T) { // AC-52・AC-53・AC-54
	t.Parallel()
	f := newSPQFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answered := f.sv.out
	f.sv.out = JudgmentLaunchOutput{Result: RunResultInterrupted}
	f.sv.during = cancel
	if _, err := f.s.PollContractRuns(ctx, f.pollInput(f.cycle(t, 100))); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("PollContractRuns: %v", err)
	}
	f.requireCalls(t, 1)
	if sd := f.record(t); sd.State != SpecDraftStateRunning || f.openHold(t) != nil {
		t.Fatalf("record = %+v hold = %+v", sd, f.openHold(t))
	}
	f.requireNoSuperviseTrace(t)
	f.sv.out, f.sv.during = answered, nil
	f.poll(t)
	f.requireCalls(t, 2)
	if sd := f.record(t); sd.State != SpecDraftStatePending {
		t.Fatalf("record = %+v", sd)
	}
}

func TestSpecSupervise_NotStartedRaisesAllQuestionsWithoutAnAction(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, f *spqFixture) {
		t.Helper()
		f.requireQuestionHold(t, 2)
		f.requireNoSuperviseTrace(t)
	}
	t.Run("AC-55・AC-56 サイクルの費用の上限", func(t *testing.T) {
		t.Parallel()
		for _, shared := range []bool{false, true} {
			f := newSPQFixture(t)
			c := f.cycle(t, 0.001)
			in := f.pollInput(c)
			if shared {
				in.Cycle = NewJudgmentCycle(c)
			}
			if _, err := f.s.PollContractRuns(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			f.requireCalls(t, 0)
			check(t, f)
		}
	})
	t.Run("AC-57・AC-58 起動しなかった（このサイクルは利用上限に当たっている）", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.poll(t, func(in *ContractPollInput) {
			in.Cycle = NewJudgmentCycle(in.CycleID)
			in.Cycle.rateLimited.Store(true)
		})
		f.requireCalls(t, 0)
		check(t, f)
	})
	t.Run("AC-57・AC-58 起動する口が使えない", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.sv.unavailable = errors.New("claude not found")
		f.poll(t)
		f.requireCalls(t, 0)
		check(t, f)
	})
	t.Run("AC-59・AC-60 利用上限", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.sv.out = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
		f.poll(t)
		f.requireCalls(t, 1)
		check(t, f)
	})
}

func TestSpecSupervise_FailedRunAndEscalate(t *testing.T) {
	t.Parallel()
	t.Run("AC-61・AC-62・AC-63 時間切れ", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.sv.out = JudgmentLaunchOutput{Result: RunResultTimedOut}
		f.poll(t)
		f.requireQuestionHold(t, 2)
		if a := f.actions(t); len(a) != 1 || a[0].Result != ActionFailed || a[0].Hand != nil {
			t.Fatalf("actions = %+v", a)
		}
		f.requireSuperviseLog(t, 0, 2)
	})
	t.Run("AC-124〜AC-127・AC-137・AC-160 上げる", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.sv.out = spqOutput("escalate", []any{})
		f.poll(t)
		h := f.requireQuestionHold(t, 2)
		if sd := f.record(t); len(sd.Answers) != 0 {
			t.Errorf("answers = %+v", sd.Answers)
		}
		if a := f.actions(t); len(a) != 1 || a[0].Hand == nil || *a[0].Hand != HandEscalate || a[0].Result != ActionExecuted || a[0].Questions != nil {
			t.Fatalf("actions = %+v", a)
		}
		f.requireSuperviseLog(t, 0, 2)
		for _, line := range strings.Split(h.Question, "\n") {
			if strings.HasPrefix(line, "スーパーバイザーが") {
				t.Errorf("question has %q", line)
			}
		}
	})
}

func TestSpecSupervise_AllAnswered(t *testing.T) { // AC-104〜AC-116・AC-128〜AC-135・AC-148・AC-149・AC-152
	t.Parallel()
	f := newSPQFixture(t)
	notices, err := f.s.ListNotices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	version := f.detail(t, f.id).Version
	starts := len(f.client.starts)
	// 状態の確認を含む委譲の段 1 回の中では、実行待ちへ戻した下書きづくりを始めない。
	in := f.input(f.cycle(t, 100), nil)
	in.Contract, in.Judgment = f.client, f.sv
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	f.requireCalls(t, 1)
	d := f.detail(t, f.id)
	sd := d.SpecDraft
	if len(d.Holds) != 0 || d.Status != StatusClassified || d.Version != version || sd.State != SpecDraftStatePending ||
		len(sd.Questions) != 0 || sd.Attempts != 1 || len(f.client.starts) != starts {
		t.Fatalf("holds = %+v status = %s record = %+v starts = %d", d.Holds, d.Status, sd, len(f.client.starts))
	}
	want := []SpecDraftAnswer{
		{Kind: SpecDraftAnswerKindAnswer, Attempt: 1, Question: "問いの一", Answer: "a", By: "supervisor",
			Basis: []SpecDraftBasis{{Path: spqRepoBase + "/docs/a.md", Locator: "LOC-" + spqRepoBase + "/docs/a.md"}}},
		{Kind: SpecDraftAnswerKindAnswer, Attempt: 1, Question: "問いの二", Answer: "d", By: "supervisor",
			Basis: []SpecDraftBasis{{Path: "memory/a.md", Locator: "LOC-memory/a.md"}}},
	}
	if got, w := spqJSON(t, sd.Answers), spqJSON(t, want); got != w {
		t.Errorf("answers = %s\nwant %s", got, w)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE kind = 'spec' AND ended_at IS NULL`); n != 0 {
		t.Errorf("open spec runs = %d", n)
	}
	a := f.actions(t)
	if len(a) != 1 || a[0].StopKind != StopKindSpecQuestion || a[0].Hand == nil || *a[0].Hand != HandAnswer ||
		a[0].Result != ActionExecuted || a[0].HoldID != nil || a[0].TaskID != nil || a[0].RunID == nil ||
		len(a[0].Questions) != 2 || a[0].Questions[1].Answer != "d" || a[0].Questions[1].Basis[0].Path != "memory/a.md" {
		t.Fatalf("actions = %+v", a)
	}
	f.requireSuperviseLog(t, 2, 0)
	after, err := f.s.ListNotices(context.Background())
	if err != nil || len(after) != len(notices) {
		t.Errorf("notices = %d %v, want %d", len(after), err, len(notices))
	}
	ov, err := f.s.GetOverview(context.Background())
	if err != nil || containsChallengeID(ov.NeedsHumanChallenges, f.id) {
		t.Errorf("needs_human = %+v %v", ov.NeedsHumanChallenges, err)
	}
	if got := sdsRead(t, filepath.Join(f.s.workspace, f.draft)); got != spqDraftDoc {
		t.Errorf("draft was rewritten:\n%s", got)
	}
	// 下書きの問いに答えた判断点の実行は、委譲先へ答えを返す再開の元にしない。
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		ids, err := answeredSVRunIDs(context.Background(), tx, f.cid)
		if len(ids) != 0 {
			t.Errorf("answeredSVRunIDs = %v, want none", ids)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	f.client.last = sfOut(ContractStateRunning, "")
	f.delegate(t, 100)
	f.requireRecord(t, f.id, SpecDraftStateRunning, 2)
	if len(f.client.starts) != starts+1 {
		t.Fatalf("starts = %d", len(f.client.starts))
	}
	briefs, err := filepath.Glob(filepath.Join(f.s.workspace, ".flywheel", "runs", "*", specBriefFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(briefs, func(p string) bool { return strings.Contains(sdsRead(t, p), "- 答えた者: supervisor") }) {
		t.Error("no brief carries the supervisor answers")
	}
}

func TestSpecSupervise_ThirdRunIsNotAsked(t *testing.T) { // AC-117
	t.Parallel()
	f := newSPQFixture(t, SpecDraft{Attempts: 1})
	f.poll(t)
	f.requireRecord(t, f.id, SpecDraftStatePending, 2)
	f.client.last = sfOut(ContractStateRunning, "")
	f.delegate(t, 100)
	f.requireRecord(t, f.id, SpecDraftStateRunning, 3)
	f.client.last = sfOut(ContractStateSucceeded, spqQuestions(), func(st *ContractStatus) { st.RunID = "ext-2" })
	f.poll(t)
	f.requireCalls(t, 1)
	if sd := f.record(t); sd.HoldReason == nil || *sd.HoldReason != SpecDraftHoldAttemptLimit {
		t.Fatalf("record = %+v", sd)
	}
}

// AC-118〜AC-123・AC-132・AC-134・AC-136・AC-150・AC-151・AC-159
func TestSpecSupervise_PartlyAnswered(t *testing.T) {
	t.Parallel()
	f := newSPQFixture(t)
	f.sv.out = spqAnswers(spqAnswer("a", "memory/a.md"), nil)
	f.poll(t)
	h := f.requireQuestionHold(t, 1)
	sd := f.record(t)
	if sd.Questions[0].Question != "問いの二" || len(sd.Answers) != 1 || sd.Answers[0].By != "supervisor" || sd.Answers[0].Question != "問いの一" {
		t.Fatalf("record = %+v", sd)
	}
	want := "仕様の下書きに、決めないと進まない問いが 1 件残りました。下書き: " + f.draft + "\nスーパーバイザーが 1 件に答えました。"
	if h.Question != want {
		t.Errorf("question = %q, want %q", h.Question, want)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND stop_kind IS NULL AND supervisor_state IS NULL`, f.cid); n != 1 {
		t.Errorf("holds without stop kind and supervisor state = %d, want 1", n)
	}
	if a := f.actions(t); len(a) != 1 || a[0].HoldID != nil || len(a[0].Questions) != 1 || a[0].Questions[0].Question != "問いの一" {
		t.Fatalf("actions = %+v", a)
	}
	f.requireSuperviseLog(t, 1, 1)

	choice := []PlanAnswerItem{saChoice(1, 2)}
	prev, err := f.s.PrepareAnswerWithItems(context.Background(), f.id, "", "", "answer", false, choice)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Option: "answer",
		Items: choice, HoldID: prev.HoldID, HoldQuestion: prev.Question, HoldOptions: prev.Options, HoldOptionRequired: prev.OptionRequired,
	}, verifiedAttestationForTest(t, f.id)); err != nil {
		t.Fatal(err)
	}
	if sd = f.record(t); len(sd.Answers) != 2 || sd.Answers[1].Answer != "d" || sd.Answers[1].By != "owner" {
		t.Fatalf("answers = %+v", sd.Answers)
	}
	f.client.last = sfOut(ContractStateRunning, "")
	f.delegate(t, 100)
	briefs, err := filepath.Glob(filepath.Join(f.s.workspace, ".flywheel", "runs", "*", specBriefFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(briefs, func(p string) bool {
		text := sdsRead(t, p)
		return strings.Contains(text, "- 答えた者: supervisor") && strings.Contains(text, "- 答えた者: owner")
	}) {
		t.Error("no brief carries both answers")
	}
}

func TestSpecSupervise_BadOutputsDiscardEveryAnswer(t *testing.T) {
	t.Parallel()
	ok1 := func() map[string]any { return spqAnswer("a", "memory/a.md") }
	ok2 := func() map[string]any { return spqAnswer("c", "memory/a.md") }
	basisCase := func(paths ...string) []any { return []any{spqAnswer("a", paths...), ok2()} }
	cases := map[string][]any{
		"AC-75 1 件":             {ok1()},
		"AC-76 3 件":             {ok1(), ok2(), ok2()},
		"AC-77 全部 null":         {nil, nil},
		"AC-78 見出しに無い答え":        {spqAnswer("z", "memory/a.md"), ok2()},
		"AC-79 ほかの問いの見出し":       {spqAnswer("c", "memory/a.md"), ok2()},
		"AC-80 根拠が空":            basisCase(),
		"AC-81 下書きだけ":           basisCase("DRAFT"),
		"AC-82 存在しない":           basisCase("memory/none.md"),
		"AC-83 ワークスペースの外":       basisCase("OUTSIDE"),
		"AC-84 外を指すリンク":         basisCase("memory/outside-link.md"),
		"AC-85 .git の下":         basisCase(spqRepoBase + "/.git/config"),
		"AC-86 対象でないリポジトリ":      basisCase(spqOtherBase + "/README.md"),
		"AC-90 タスクの作業ツリー":       basisCase(".flywheel/worktrees/app/wt/docs/a.md"),
		"AC-91 slots.paths の下":  basisCase("work/app-1/a.md"),
		"AC-92 推奨の印つき":          {spqAnswer("a（推奨）", "memory/a.md"), ok2()},
		"AC-93 実行の記録の置き場":       basisCase(".flywheel/runs/R-1/spec-brief.md"),
		"AC-94 ディレクトリ":          basisCase(spqRepoBase + "/docs"),
		"AC-98 Q1 は通り Q2 の根拠が空": {ok1(), spqAnswer("c")},
	}
	for name, answers := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSPQFixture(t)
			app, _ := f.conn.findConnectorRepo("other")
			app.Slots.Paths = []string{"work/app-1"}
			outside := filepath.Join(t.TempDir(), "o.md")
			if err := os.WriteFile(outside, []byte("o"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(f.s.workspace, "memory", "outside-link.md")); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, f.s.workspace, ".flywheel/worktrees/app/wt/docs/a.md", "w")
			writeWorkspaceFile(t, f.s.workspace, "work/app-1/a.md", "w")
			writeWorkspaceFile(t, f.s.workspace, ".flywheel/runs/R-1/spec-brief.md", "b")
			for _, a := range answers {
				if m, ok := a.(map[string]any); ok {
					for _, b := range m["basis"].([]any) {
						place := b.(map[string]any)
						switch place["path"] {
						case "DRAFT":
							place["path"] = f.draft
						case "OUTSIDE":
							place["path"] = outside
						}
					}
				}
			}
			f.sv.out = spqAnswers(answers...)
			f.poll(t)
			f.requireQuestionHold(t, 2) // AC-103: spec_failed にならず、回答待ちになる
			if sd := f.record(t); len(sd.Answers) != 0 {
				t.Errorf("answers = %+v", sd.Answers)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'SV' AND result = 'invalid_output'`); n != 1 { // AC-99
				t.Errorf("invalid_output SV runs = %d", n)
			}
			if a := f.actions(t); len(a) != 1 || a[0].Result != ActionFailed || a[0].Questions != nil { // AC-100・AC-101
				t.Errorf("actions = %+v", a)
			}
			f.requireSuperviseLog(t, 0, 2) // AC-102
		})
	}
}

func TestSpecSupervise_CountableBasis(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		base, other string
		paths       []string
		ok          bool
	}{
		"AC-87 置き場の外の元のクローン":     {"repos/app", "repos/other", []string{"repos/app/docs/a.md"}, true},
		"AC-88 置き場の外の .git":      {"repos/app", "repos/other", []string{"repos/app/.git/config"}, false},
		"AC-89 置き場の外の対象でないリポジトリ": {"repos/app", "repos/other", []string{"repos/other/README.md"}, false},
		"AC-95 元のクローンの下のファイル":    {spqRepoBase, spqOtherBase, []string{spqRepoBase + "/docs/a.md"}, true},
		"AC-96 ワークスペースの文書":       {spqRepoBase, spqOtherBase, []string{"memory/a.md"}, true},
		"AC-97 1 件が数えられる":        {spqRepoBase, spqOtherBase, []string{"memory/none.md", "memory/a.md"}, true},
		// 大文字と小文字を区別しないファイルシステムでだけ、書き方を変えた場所が在る。
		"大文字と小文字を変えた .GIT の下":      {spqRepoBase, spqOtherBase, []string{spqRepoBase + "/.GIT/config"}, false},
		"大文字と小文字を変えた対象でないリポジトリの場所": {"repos/app", "repos/other", []string{"REPOS/Other/README.md"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := spqBase(t, c.base, c.other)
			if strings.HasPrefix(name, "大文字と小文字") {
				if _, err := os.Stat(filepath.Join(f.s.workspace, c.paths[0])); err != nil {
					t.Skip("大文字と小文字を区別するファイルシステムでは、書き方を変えた場所が無い")
				}
			}
			f.spqCreate(t, SpecDraft{})
			f.start(t)
			f.sv.out = spqAnswers(spqAnswer("a", c.paths...), nil)
			f.poll(t)
			f.requireCalls(t, 1)
			if !c.ok {
				f.requireQuestionHold(t, 2)
				return
			}
			f.requireQuestionHold(t, 1)
			if sd := f.record(t); len(sd.Answers) != 1 || sd.Answers[0].Answer != "a" || len(sd.Answers[0].Basis) != len(c.paths) {
				t.Errorf("answers = %+v", sd.Answers)
			}
		})
	}
}

// AC-69〜AC-74・AC-138〜AC-144
func TestSpecSupervise_StateChangedDuringTheCall(t *testing.T) {
	t.Parallel()
	requireDiscarded := func(t *testing.T, f *spqFixture) {
		t.Helper()
		if sd := f.record(t); len(sd.Answers) != 0 {
			t.Errorf("answers = %+v", sd.Answers)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, f.cid); n != 0 {
			t.Errorf("holds = %d", n)
		}
		a := f.actions(t)
		if len(a) != 1 || a[0].Result != ActionRejected || a[0].RejectReason == nil || *a[0].RejectReason != RejectStateChanged || a[0].Questions != nil {
			t.Fatalf("actions = %+v", a)
		}
		f.requireSuperviseLog(t, 0, 0)
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE kind = 'spec' AND ended_at IS NULL`); n != 0 {
			t.Errorf("open spec runs = %d", n)
		}
	}
	for name, answers := range map[string][]any{"全部に答える": nil, "一部に答える": {spqAnswer("a", "memory/a.md"), nil}} {
		t.Run("edit --spec・"+name, func(t *testing.T) {
			t.Parallel()
			f := newSPQFixture(t)
			if answers != nil {
				f.sv.out = spqAnswers(answers...)
			}
			spec := "docs/spec.md"
			f.sv.during = func() {
				if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, f.id, EditInput{Spec: &spec,
					SpecDraftCancel: func() (ContractCancelInput, error) {
						return ContractCancelInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}, nil
					}}); err != nil {
					t.Errorf("edit --spec: %v", err)
				}
			}
			f.poll(t)
			f.requireCalls(t, 1)
			if len(f.client.cancels) != 0 {
				t.Errorf("cancels = %v", f.client.cancels)
			}
			if sd := f.record(t); sd.State != SpecDraftStateSkipped {
				t.Errorf("state = %s", sd.State)
			}
			requireDiscarded(t, f)
		})
	}
	t.Run("取り下げ", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		f.sv.during = func() {
			if _, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: f.id, AgentDecl: f.agent,
				ConnDecl: f.conn, Client: f.client}); err != nil {
				t.Errorf("cancel: %v", err)
			}
			if _, err := withdrawForTest(t, f.s, f.id, "やめる"); err != nil {
				t.Errorf("withdraw: %v", err)
			}
		}
		f.poll(t)
		f.requireCalls(t, 1)
		if len(f.client.cancels) != 0 {
			t.Errorf("cancels = %v", f.client.cancels)
		}
		requireDiscarded(t, f)
	})
}

// AC-145・AC-146・AC-147
func TestSpecSupervise_ActionWriteFailureChangesNothing(t *testing.T) {
	t.Parallel()
	requireUnchanged := func(t *testing.T, f *spqFixture) {
		t.Helper()
		sd := f.record(t)
		if sd.State != SpecDraftStateRunning || len(sd.Answers) != 0 || len(sd.Questions) != 0 || f.openHold(t) != nil {
			t.Fatalf("record = %+v hold = %+v", sd, f.openHold(t))
		}
		f.requireNoSuperviseTrace(t)
	}
	t.Run("ストアに無いサイクルの ID で状態の確認を呼ぶ", func(t *testing.T) {
		t.Parallel()
		f := newSPQFixture(t)
		_, _ = f.s.PollContractRuns(context.Background(), f.pollInput("Y-999"))
		requireUnchanged(t, f)
	})
	// 上の呼び方は状態の確認の記録から失敗するので、判断点の実行は共有のサイクルに記録させ、対処の記録だけを失敗させる。
	for name, answers := range map[string][]any{
		"全部に答える": {spqAnswer("a", "memory/a.md"), spqAnswer("c", "memory/a.md")},
		"一部に答える": {spqAnswer("a", "memory/a.md"), nil},
	} {
		t.Run("対処の記録の書き込みが誤り・"+name, func(t *testing.T) {
			t.Parallel()
			f := newSPQFixture(t)
			// 実行の終わりを読んだ状態（終わりの状態で開いたままの実行）を、中断で作る。
			f.sv.out = JudgmentLaunchOutput{Result: RunResultInterrupted}
			f.poll(t)
			requireUnchanged(t, f)
			var rowID, startRun int64
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT id, start_run_id FROM contract_run WHERE ended_at IS NULL`).Scan(&rowID, &startRun)
			}); err != nil {
				t.Fatal(err)
			}
			f.sv.out = spqAnswers(answers...)
			in := f.pollInput("Y-999")
			in.Cycle = NewJudgmentCycle(f.cycle(t, 100))
			if _, _, err := f.s.finishSpecDraftRun(context.Background(), in, formatRunID(startRun), rowID, ""); err == nil {
				t.Fatal("finishSpecDraftRun succeeded, want the action write to fail")
			}
			f.requireCalls(t, 2)
			requireUnchanged(t, f)
			if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL`); n != 1 {
				t.Errorf("open runs = %d, want 1", n)
			}
		})
	}
}

// 引き継ぎの 3: 下書きの問いの止まり方は、保留の入口が断る。
func TestSpecSupervise_HoldEntranceRefusesSpecQuestion(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	_, err := holdViaEntrance(t, s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindSpecQuestion, SupervisorEnabled: true})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM hold`).Scan(&n) }); err != nil || n != 0 {
		t.Fatalf("holds = %d %v", n, err)
	}
}

// 引き継ぎの 4: null を含む委譲先の問いの答えは、断るだけでなく、答えの対処の記録も作業ログも書かない。
func TestSpecSupervise_RefusedNullAnswerWritesNoActionNorLog(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindDelegateStopped)
	cid := mustChallengeID(t, id)
	req := supervisorHandRequest{CycleID: f.cycle, Target: supervisorTarget{ChallengeID: cid, StopKind: StopKindChildQuestion},
		Output: SVOutput{Hand: HandAnswer, Findings: "f", Answers: []*SVAnswer{{Answer: "x"}, nil}}}
	_, _, err := f.s.recordSupervisorAnswer(context.Background(), req, []ChildQuestion{{Question: "q1"}, {Question: "q2"}})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	actions, err := f.s.ListSupervisorActionsByChallenge(context.Background(), id)
	if err != nil || len(actions) != 0 || len(saLogs(t, f.s, id, ActivityActionSupervise)) != 0 {
		t.Fatalf("actions = %+v %v logs = %v", actions, err, saLogs(t, f.s, id, ActivityActionSupervise))
	}
}

func spqJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
