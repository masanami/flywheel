package core

// 分類の判断点の fits_one_slice・区切りを分ける記録・計画の対象外と、区切りを分ける判断点の呼び出し・入力・出力の検査
// （docs/features/request-to-plan-s3.md）。ストアは実物を使う。

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
	"time"
)

const j10TestConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "app", "remote": "o/app", "default_branch": "main", "connector": "direct",
     "slots": {"provider": "worktree", "base": ".flywheel/repos/app", "count": 1}},
    {"name": "lib", "remote": "o/lib", "default_branch": "main", "connector": "direct",
     "slots": {"provider": "clone", "paths": [".flywheel/repos/lib-1"]}}
  ]
}`

func j10Connectors(t *testing.T) *ConnectorsDeclaration {
	t.Helper()
	decl, err := parseConnectorsDeclaration([]byte(j10TestConnectorsJSON))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := decl.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return decl
}

// j10Call は偽の判断の IF が受けた 1 回の呼び出し。
type j10Call struct {
	In JudgmentLaunchInput
}

// j10Invoker は判断点ごとに決めた出力を返す偽の判断の IF。
type j10Invoker struct {
	mu      sync.Mutex
	calls   []j10Call
	outputs map[JudgmentPoint][]byte
	cost    *float64
}

func (f *j10Invoker) Available(context.Context) error { return nil }

func (f *j10Invoker) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, j10Call{In: in})
	out := succeeded(f.outputs[in.Judgment])
	out.ReportedTotalCostUSD = f.cost
	return out, nil
}

func (f *j10Invoker) callsFor(j JudgmentPoint) []JudgmentLaunchInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []JudgmentLaunchInput
	for _, c := range f.calls {
		if c.In.Judgment == j {
			out = append(out, c.In)
		}
	}
	return out
}

type j10Fixture struct {
	s    *Store
	decl *AgentDeclaration
	conn *ConnectorsDeclaration
}

func newJ10Fixture(t *testing.T) *j10Fixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return &j10Fixture{s: s, decl: newAgentDeclForJ1Test(t, s, "J10-POSITION-MARKER"), conn: j10Connectors(t)}
}

// ingested は o/app#<n> を取り込んだ形の未分類の課題を作る。
func (f *j10Fixture) ingested(t *testing.T) string {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{
		Title: "J10-TITLE", Description: "J10-DESCRIPTION", DoneCriteria: "J10-DONE", Urgency: strPtr("高"),
	})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	cid := mustChallengeInternalID(t, ch.ID)
	f.write(t, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE challenge SET origin = ? WHERE id = ?`, string(ChallengeOriginIngest), cid); err != nil {
			return err
		}
		_, err := insertSourceBinding(context.Background(), tx, time.Now(), cid, createSourceBindingInput{
			SourceID: "src", ExternalKey: "o/app#" + ch.ID[2:], URL: "https://github.com/o/app/issues/" + ch.ID[2:],
			Fingerprint: Fingerprint("body"), UpstreamState: upstreamStateOpen, PolicyState: policyStateInPolicy,
		})
		return err
	})
	return ch.ID
}

func (f *j10Fixture) write(t *testing.T, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := f.s.db.Write(context.Background(), fn); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (f *j10Fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (f *j10Fixture) detail(t *testing.T, id string) *ChallengeDetail {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	return d
}

func (f *j10Fixture) splitState(t *testing.T, id string) SliceSplitState {
	t.Helper()
	d := f.detail(t, id)
	if d.SliceSplit == nil {
		return ""
	}
	return d.SliceSplit.State
}

func (f *j10Fixture) setSplitState(t *testing.T, id string, state SliceSplitState) {
	t.Helper()
	cid := mustChallengeInternalID(t, id)
	f.write(t, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE slice_split SET state = ? WHERE challenge_id = ?`, string(state), cid)
		return err
	})
}

// classify は ID を省略した classify --auto を 1 回行う。conn が nil なら区切りを分ける判断点は呼ばれない。
func (f *j10Fixture) classify(t *testing.T, inv JudgmentInvoker, conn *ConnectorsDeclaration, id *string) *J1AutoResult {
	t.Helper()
	res, err := f.s.ClassifyAutoJ1(context.Background(), J1AutoInput{
		ChallengeID: id, AgentDecl: f.decl, ConnDecl: conn, Invoker: inv, CycleID: beginJ1Cycle(t, f.s, 300),
	})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	return res
}

// pending は区切りを分ける記録が pending の分類済の課題を、分類の判断点の写しで作る。
func (f *j10Fixture) pending(t *testing.T) string {
	t.Helper()
	id := f.ingested(t)
	f.classify(t, &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ1: j1Fits(t, J1VerdictMine, false)}}, nil, &id)
	if got := f.splitState(t, id); got != SliceSplitStatePending {
		t.Fatalf("slice_split.state = %q, want pending", got)
	}
	return id
}

// j1Fits は判定 verdict・fits_one_slice が fits の分類の判断点の出力。
func j1Fits(t *testing.T, verdict J1Verdict, fits bool) []byte {
	t.Helper()
	return j1Output(t, j1RawOutput{Verdict: string(verdict), FitsOneSlice: &fits, Priority: strPtr("P1"),
		Reason: "J1-CLASSIFY-REASON", Question: strPtr("q?"), Headline: strPtr("h"), Context: strPtr("c")})
}

func j10TestSlice(title string, deps ...int) map[string]any {
	if deps == nil {
		deps = []int{}
	}
	return map[string]any{"title": title, "headline": "h", "context": "c", "summary": "s", "scope": "sc", "repo": "app", "depends_on": deps}
}

// j10TestOutput は検査を通る split の出力（mut で 1 つずつ壊す）。
func j10TestOutput(t *testing.T, mut func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"verdict": "split", "reason": "r",
		"slices":   []any{j10TestSlice("a"), j10TestSlice("b", 1)},
		"evidence": []any{map[string]any{"path": ".flywheel/repos/app/docs/a.md", "note": "n"}},
	}
	if mut != nil {
		mut(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func j10SingleOutput(t *testing.T) []byte {
	return j10TestOutput(t, func(m map[string]any) { m["verdict"] = "single"; m["slices"] = nil })
}

// --- 分類の判断点の出力 ---

// AC-1〜4
func TestValidateJ1Output_FitsOneSliceIsRequiredOnlyForMine(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		data string
		ok   bool
	}{
		"mine で無い":        {`{"verdict":"mine","priority":"P1","reason":"r","headline":"h","context":"c"}`, false},
		"mine で null":     {`{"verdict":"mine","fits_one_slice":null,"priority":"P1","reason":"r","headline":"h","context":"c"}`, false},
		"mine で偽":         {`{"verdict":"mine","fits_one_slice":false,"priority":"P1","reason":"r","headline":"h","context":"c"}`, true},
		"not_mine で無い":    {`{"verdict":"not_mine","reason":"r"}`, true},
		"uncertain で無い":   {`{"verdict":"uncertain","reason":"r","question":"q"}`, true},
		"not_mine で null": {`{"verdict":"not_mine","fits_one_slice":null,"reason":"r"}`, true},
	} {
		if _, ok := validateJ1Output([]byte(c.data)); ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", name, ok, c.ok)
		}
	}
}

// AC-1・AC-2: 実行の結果が invalid_output になる。
func TestClassifyAutoJ1_MineWithoutFitsOneSlice_IsInvalidOutput(t *testing.T) {
	t.Parallel()
	for name, fits := range map[string]string{"無い": "", "null": `"fits_one_slice":null,`} {
		f := newJ10Fixture(t)
		id := f.ingested(t)
		data := []byte(`{"verdict":"mine",` + fits + `"priority":"P1","reason":"r","headline":"h","context":"c"}`)
		res := f.classify(t, succeededInvoker(data), f.conn, &id)
		if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
			t.Errorf("%s: items = %+v, want one invalid_output", name, res.Items)
		}
		if got := f.detail(t, id).Status; got != StatusUnclassified {
			t.Errorf("%s: status = %s, want unclassified", name, got)
		}
	}
}

// AC-7
func TestJ1OutputSchema_HasNullableBooleanFitsOneSlice(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(j1OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(schema.Properties["fits_one_slice"].Type)
	if string(got) != `["boolean","null"]` {
		t.Errorf("fits_one_slice type = %s, want [\"boolean\",\"null\"]", got)
	}
}

// 出力の型の各項目の description は、指示文の項目名と同じ言葉である（判定〔verdict〕は指示文に項目名が無い）。
// 全項目に description があり、判定のほかは指示文の中に同じ言葉（箇条の項目名はその形）がある。
func TestJudgmentOutputSchemas_DescribeFieldsWithPromptNames(t *testing.T) {
	t.Parallel()
	type props map[string]struct {
		Description string `json:"description"`
		Items       struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"items"`
	}
	descriptions := func(schema []byte) map[string]string {
		var s struct {
			Properties props `json:"properties"`
		}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for name, p := range s.Properties {
			got[name] = p.Description
			for sub, q := range p.Items.Properties {
				got[name+"."+sub] = q.Description
			}
		}
		return got
	}
	prompt := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "invoker", "prompts", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	j10Want := map[string]string{
		"verdict": "判定", "reason": "理由", "evidence": "根拠", "slices": "区切り",
		"evidence.path": "場所", "evidence.note": "手がかり",
		"slices.title": "題名", "slices.headline": "一言で", "slices.context": "位置づけ", "slices.summary": "要旨",
		"slices.scope": "範囲", "slices.repo": "リポジトリ", "slices.depends_on": "前に済ませる区切り",
	}
	j10Got := descriptions(j10OutputSchema())
	if len(j10Got) != len(j10Want) {
		t.Errorf("j10 schema fields = %v, want exactly %v", j10Got, j10Want)
	}
	j10Prompt := prompt("j10.md")
	for name, want := range j10Want {
		if got := j10Got[name]; got != want {
			t.Errorf("j10 %s description = %q, want %q", name, got, want)
		}
		// 指示文で箇条の項目名（「- 理由:」の形）になっている項目は、その形で照合する。
		// 区切り・場所・手がかりは箇条の項目名ではなく本文の語なので、語として照合する。
		switch name {
		case "verdict":
		case "slices", "evidence.path", "evidence.note":
			if !strings.Contains(j10Prompt, want) {
				t.Errorf("j10 %s description %q does not appear in prompts/j10.md", name, want)
			}
		default:
			if !strings.Contains(j10Prompt, "\n- "+want+":") {
				t.Errorf("j10 %s description %q is not an item %q in prompts/j10.md", name, want, "- "+want+":")
			}
		}
	}

	const fits = "区切り 1 つに収まるか"
	if got := descriptions(j1OutputSchema())["fits_one_slice"]; got != fits {
		t.Errorf("j1 fits_one_slice description = %q, want %q", got, fits)
	}
	if !strings.Contains(prompt("j1.md"), "## "+fits) {
		t.Errorf("prompts/j1.md has no section %q", "## "+fits)
	}
}

// --- 区切りを分ける記録の作成 ---

// AC-8・AC-9・AC-10・AC-29
func TestClassifyAutoJ1_NotOneSlice_ClassifiesAndRequestsSliceSplit(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	cid := mustChallengeInternalID(t, id)
	if got := f.detail(t, id).Status; got != StatusClassified {
		t.Errorf("status = %s, want classified", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'slice_split_requested' AND run_id IS NOT NULL`, cid); n != 1 {
		t.Errorf("slice_split_requested activities = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM slice_split WHERE challenge_id = ? AND classify_reason = 'J1-CLASSIFY-REASON'`, cid); n != 1 {
		t.Errorf("slice_split with the classify reason = %d, want 1", n)
	}
}

// AC-5・AC-6・AC-11
func TestClassifyAutoJ1_NoSliceSplitUnlessMineSaysItDoesNotFit(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		verdict J1Verdict
		fits    bool
	}{
		"not_mine で偽":  {J1VerdictNotMine, false},
		"uncertain で偽": {J1VerdictUncertain, false},
		"mine で真":      {J1VerdictMine, true},
	} {
		f := newJ10Fixture(t)
		id := f.ingested(t)
		f.classify(t, succeededInvoker(j1Fits(t, c.verdict, c.fits)), nil, &id)
		if d := f.detail(t, id); d.SliceSplit != nil {
			t.Errorf("%s: slice_split = %+v, want nil", name, d.SliceSplit)
		}
	}
}

// AC-12・AC-13・AC-14
func TestClassifyAutoJ1_ChallengesOutOfScopeIgnoreNotOneSlice(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, f *j10Fixture) string{
		"flywheel create の課題": func(t *testing.T, f *j10Fixture) string { return newUnclassified(t, f.s) },
		"取り込み元の子を持つ課題": func(t *testing.T, f *j10Fixture) string {
			id := f.ingested(t)
			f.write(t, func(tx *sql.Tx) error {
				return insertSourceChild(context.Background(), tx, time.Now(), mustChallengeInternalID(t, id), "src",
					UpstreamIssue{ExternalKey: "o/app#99", URL: "https://github.com/o/app/issues/99", Title: "child"}, "")
			})
			return id
		},
		"要求の記録を指す課題": func(t *testing.T, f *j10Fixture) string {
			id := f.ingested(t)
			req, err := f.s.CreateRequirement(context.Background(), ChannelCLI, CreateRequirementInput{Title: "req"})
			if err != nil {
				t.Fatalf("CreateRequirement: %v", err)
			}
			rid, _ := parseRequirementID(req.ID)
			f.write(t, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE challenge SET requirement_id = ? WHERE id = ?`, rid, mustChallengeInternalID(t, id))
				return err
			})
			return id
		},
	}
	for name, setup := range cases {
		f := newJ10Fixture(t)
		id := setup(t, f)
		f.classify(t, succeededInvoker(j1Fits(t, J1VerdictMine, false)), nil, &id)
		d := f.detail(t, id)
		if d.Status != StatusClassified || d.SliceSplit != nil {
			t.Errorf("%s: status=%s slice_split=%+v, want classified without slice_split", name, d.Status, d.SliceSplit)
		}
	}
}

// --- 計画の対象 ---

// AC-15・AC-16・AC-20
func TestSelectJ2AutoTargets_ExcludesOpenSliceSplit(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	for state, want := range map[SliceSplitState]bool{
		SliceSplitStatePending: false, SliceSplitStateAwaitingOwner: false, SliceSplitStateSingle: true,
	} {
		f.setSplitState(t, id, state)
		var targets []int64
		var excluded []NotStarted
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			targets, excluded, err = selectJ2AutoTargets(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(targets, mustChallengeInternalID(t, id)); got != want || len(excluded) != 0 {
			t.Errorf("%s: selected=%v excluded=%v, want selected=%v and nothing listed", state, got, excluded, want)
		}
	}
}

// AC-17・AC-18・AC-19
func TestPlanAutoJ2_ExplicitIDWithOpenSliceSplit_DoesNotInvokeAndReportsReason(t *testing.T) {
	t.Parallel()
	for _, state := range []SliceSplitState{SliceSplitStatePending, SliceSplitStateAwaitingOwner} {
		f := newJ10Fixture(t)
		id := f.pending(t)
		f.setSplitState(t, id, state)
		inv := &j10Invoker{}
		res, err := f.s.PlanAutoJ2(context.Background(), J2AutoInput{
			ChallengeID: &id, AgentDecl: f.decl, ConnDecl: f.conn, Invoker: inv, Upstream: newFakeUpstreamThreads(),
			CycleID: beginJ1Cycle(t, f.s, 300),
		})
		if err != nil {
			t.Fatalf("%s: PlanAutoJ2: %v", state, err)
		}
		if len(inv.calls) != 0 || len(res.Items) != 0 {
			t.Errorf("%s: calls=%d items=%v, want none", state, len(inv.calls), res.Items)
		}
		want := []NotStarted{{ChallengeID: id, Reason: "slice_split_pending"}}
		if !slices.Equal(res.NotStarted, want) {
			t.Errorf("%s: NotStarted = %+v, want %+v", state, res.NotStarted, want)
		}
	}
}

// --- 区切りを分ける判断点の呼び出し ---

// AC-21: サイクルの分類の段で、pending の課題ごとに 1 回呼ぶ。AC-23: 同じ段で pending になった課題も呼ぶ。
func TestRunCycle_ClassifyPhaseInvokesJ10OncePerPendingChallenge(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	before := f.pending(t)
	fresh := f.ingested(t)
	inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{
		JudgmentJ1: j1Fits(t, J1VerdictMine, false), JudgmentJ10: j10SingleOutput(t),
	}}
	cf := &cycleFixture{s: f.s, agent: f.decl, conn: f.conn, upstream: newFakeUpstreamThreads()}
	jc := NewJudgmentCycle(beginJ1Cycle(t, f.s, 300))
	phases, err := f.s.runAdvancePhases(context.Background(), cf.input(inv), jc.cycleID, jc, &[]ReleaseExecItem{})
	if err != nil {
		t.Fatalf("runAdvancePhases: %v", err)
	}
	var got []string
	for _, p := range phases {
		if p.Phase != CyclePhaseClassify {
			continue
		}
		for _, it := range p.Items {
			got = append(got, it.ChallengeID+":"+it.Outcome)
		}
	}
	want := []string{fresh + ":mine", before + ":single", fresh + ":single"}
	if !slices.Equal(got, want) {
		t.Errorf("classify items = %v, want %v", got, want)
	}
	if n := len(inv.callsFor(JudgmentJ10)); n != 2 {
		t.Errorf("J10 calls = %d, want 2", n)
	}
	if n := len(inv.callsFor(JudgmentJ2)); n != 0 {
		t.Errorf("J2 calls = %d, want 0 while the slice split is pending", n)
	}
}

// AC-22・AC-23: classify --auto（ID の省略・指定）。
func TestClassifyAutoJ1_InvokesJ10ForPendingChallenges(t *testing.T) {
	t.Parallel()
	for name, explicit := range map[string]bool{"ID を省略": false, "ID を指定": true} {
		f := newJ10Fixture(t)
		id := f.ingested(t)
		var arg *string
		if explicit {
			arg = &id
		}
		inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{
			JudgmentJ1: j1Fits(t, J1VerdictMine, false), JudgmentJ10: j10TestOutput(t, nil),
		}}
		res := f.classify(t, inv, f.conn, arg)
		if n := len(inv.callsFor(JudgmentJ10)); n != 1 {
			t.Errorf("%s: J10 calls = %d, want 1", name, n)
		}
		if len(res.Items) != 2 || res.Items[1].Outcome != "split" || res.Items[1].Result != RunResultSucceeded {
			t.Errorf("%s: items = %+v, want J1 then J10 split", name, res.Items)
		}
		// すでに pending の課題にも 1 回呼ぶ。
		old := f.pending(t)
		if explicit {
			arg = &old
		}
		again := &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ10: j10SingleOutput(t)}}
		f.classify(t, again, f.conn, arg)
		if n := len(again.callsFor(JudgmentJ10)); n != 1 {
			t.Errorf("%s: J10 calls for the already pending challenge = %d, want 1", name, n)
		}
	}
}

func TestClassifyAutoJ1_WithoutConnectors_DoesNotInvokeJ10(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	inv := &j10Invoker{}
	f.classify(t, inv, nil, nil)
	if len(inv.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(inv.calls))
	}
	_, err := f.s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: f.decl, Invoker: inv, CycleID: beginJ1Cycle(t, f.s, 300)})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("explicit ID without connectors: err = %v, want ErrInvalidTransition", err)
	}
}

// pending だけが残るワークスペースも、サイクルを回すと進むものがあると数える。
func TestCountCycleWork_CountsPendingSliceSplitAsClassify(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	f.pending(t)
	work, err := f.s.CountCycleWork(context.Background(), f.decl, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	round, err := f.s.CountRoundWork(context.Background(), f.decl, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if work.Stages.Classify != 1 || work.Stages.Plan != 0 || round.Stages.Classify != 1 {
		t.Errorf("work = %+v round = %+v, want classify 1 and plan 0", work.Stages, round.Stages)
	}
}

// j10Once は pending の課題 1 件に区切りを分ける判断点を 1 回呼び、その起動の入力を返す。
func j10Once(t *testing.T, f *j10Fixture) JudgmentLaunchInput {
	t.Helper()
	inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ10: j10SingleOutput(t)}}
	f.classify(t, inv, f.conn, nil)
	calls := inv.callsFor(JudgmentJ10)
	if len(calls) != 1 {
		t.Fatalf("J10 calls = %d, want 1", len(calls))
	}
	return calls[0]
}

// AC-24・AC-26〜28・AC-30〜32・AC-46〜50
func TestJ10Launch_WorkspaceSectionsAndDefaultBudget(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	in := j10Once(t, f)

	if in.Workspace != f.s.Workspace() || in.WorkDir != "" || len(in.AddDirs) != 0 {
		t.Errorf("Workspace=%q WorkDir=%q AddDirs=%v, want the workspace as the working directory", in.Workspace, in.WorkDir, in.AddDirs)
	}
	if in.MaxBudgetUSD != 3 {
		t.Errorf("MaxBudgetUSD = %v, want 3", in.MaxBudgetUSD)
	}
	var labels []string
	content := map[string]string{}
	for _, s := range in.Sections {
		labels = append(labels, s.Label)
		content[s.Label] = s.Content
	}
	if want := []string{"課題", "取り込み元の対応", "分類の理由", "リポジトリ", "ポジション定義"}; !slices.Equal(labels, want) {
		t.Fatalf("section labels = %v, want %v", labels, want)
	}
	for label, phrases := range map[string][]string{
		"課題":       {"J10-TITLE", "J10-DESCRIPTION", "J10-DONE", "緊急度: 高", "起票者: alice"},
		"取り込み元の対応": {"URL: https://github.com/o/app/issues/" + id[2:], "外部キー: o/app#" + id[2:]},
		"分類の理由":    {"J1-CLASSIFY-REASON"},
		"ポジション定義":  {"J10-POSITION-MARKER"},
	} {
		for _, p := range phrases {
			if !strings.Contains(content[label], p) {
				t.Errorf("section %q = %q, want it to contain %q", label, content[label], p)
			}
		}
	}
	wantRepos := "名前: app\nremote: o/app\n元のクローンの場所: .flywheel/repos/app\n\n名前: lib\nremote: o/lib\n元のクローンの場所: なし"
	if content["リポジトリ"] != wantRepos {
		t.Errorf("section リポジトリ = %q, want %q", content["リポジトリ"], wantRepos)
	}
	if string(in.OutputSchema) != string(j10OutputSchema()) {
		t.Errorf("OutputSchema is not the J10 schema: %s", in.OutputSchema)
	}
}

// AC-33
func TestJ10Launch_UsesDeclaredBudget(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	f.pending(t)
	dir := writeAgentJSON(t, `{"position_file": "position.md", "judgment_budget_usd": {"J10": 7.5}}`)
	declared, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration: %v", err)
	}
	f.decl = declared
	if in := j10Once(t, f); in.MaxBudgetUSD != 7.5 {
		t.Errorf("MaxBudgetUSD = %v, want 7.5", in.MaxBudgetUSD)
	}
}

// AC-34
func TestJ10Run_CostCountsInTheJudgmentBucketAndTheCycle(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	f.pending(t)
	cost := 1.25
	cycleID := beginJ1Cycle(t, f.s, 300)
	inv := &j10Invoker{cost: &cost, outputs: map[JudgmentPoint][]byte{JudgmentJ10: j10SingleOutput(t)}}
	if _, err := f.s.ClassifyAutoJ1(context.Background(), J1AutoInput{AgentDecl: f.decl, ConnDecl: f.conn, Invoker: inv, CycleID: cycleID}); err != nil {
		t.Fatal(err)
	}
	cyid, _ := parseCycleID(cycleID)
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J10' AND budget_bucket = 'judgment' AND cost_usd = 1250000 AND cycle_id = ?`, cyid); n != 1 {
		t.Errorf("J10 runs in the judgment bucket of the cycle = %d, want 1", n)
	}
	cyc, err := f.s.EndCycle(context.Background(), cycleID, CycleResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if cyc.SpentUSD != 1.25 {
		t.Errorf("cycle SpentUSD = %v, want 1.25", cyc.SpentUSD)
	}
}

// AC-35・AC-64: 失敗が続くと連続失敗の上限で judgment_failed の保留になり、記録は pending のまま。
func TestJ10Run_ConsecutiveFailuresHoldAsJudgmentFailed(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	cid := mustChallengeInternalID(t, id)
	inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{JudgmentJ10: []byte(`{"verdict":"split"}`)}}
	for i := range f.decl.FailureLimit {
		res := f.classify(t, inv, f.conn, nil)
		if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput || res.Items[0].Outcome != "" {
			t.Fatalf("run %d: items = %+v, want one invalid_output", i, res.Items)
		}
		if got := f.splitState(t, id); got != SliceSplitStatePending {
			t.Fatalf("run %d: slice_split.state = %q, want pending", i, got)
		}
	}
	res := f.classify(t, inv, f.conn, nil)
	if n := len(inv.callsFor(JudgmentJ10)); n != f.decl.FailureLimit {
		t.Errorf("J10 calls = %d, want %d", n, f.decl.FailureLimit)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Errorf("NotStarted = %+v, want failure_limit", res.NotStarted)
	}
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND stop_kind = 'judgment_failed'`, cid); n != 1 {
		t.Errorf("judgment_failed holds = %d, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM occurrence WHERE challenge_id = ? AND kind = 'judgment_failed'`, cid); n < f.decl.FailureLimit {
		t.Errorf("judgment_failed occurrences = %d, want at least %d", n, f.decl.FailureLimit)
	}
}

// --- 出力の検査 ---

// AC-51〜63
func TestValidateJ10Output(t *testing.T) {
	t.Parallel()
	repos := []string{"app", "lib"}
	setSlices := func(n int) func(map[string]any) {
		return func(m map[string]any) {
			s := []any{}
			for range n {
				s = append(s, j10TestSlice("t"))
			}
			m["slices"] = s
		}
	}
	// second は 2 つ目の区切りの欄 key を v にする。
	second := func(key string, v any) func(map[string]any) {
		return func(m map[string]any) { m["slices"].([]any)[1].(map[string]any)[key] = v }
	}
	set := func(key string, v any) func(map[string]any) {
		return func(m map[string]any) { m[key] = v }
	}
	evidence := func(path string) func(map[string]any) {
		return set("evidence", []any{map[string]any{"path": path, "note": "n"}})
	}
	long := func(n int) string { return strings.Repeat("あ", n) }

	type tc struct {
		mut func(map[string]any)
		ok  bool
	}
	cases := map[string]tc{
		"そのまま":                   {nil, true},
		"slices 1 件":             {setSlices(1), false},
		"slices 2 件":             {setSlices(2), true},
		"slices 8 件":             {setSlices(8), true},
		"slices 9 件":             {setSlices(9), false},
		"題名が空白":                  {second("title", " \n　"), false},
		"題名 200 文字":              {second("title", long(200)), true},
		"題名 201 文字":              {second("title", long(201)), false},
		"一言でが空白":                 {second("headline", " "), false},
		"一言でが上限超え":               {second("headline", long(MaxHeadlineRunes+1)), false},
		"位置づけが空白":                {second("context", " "), false},
		"位置づけが上限超え":              {second("context", long(MaxContextRunes+1)), false},
		"要旨が空白":                  {second("summary", " "), false},
		"要旨 2000 文字":             {second("summary", long(2000)), true},
		"要旨 2001 文字":             {second("summary", long(2001)), false},
		"範囲が空白":                  {second("scope", " "), false},
		"範囲 2000 文字":             {second("scope", long(2000)), true},
		"範囲 2001 文字":             {second("scope", long(2001)), false},
		"宣言に無いリポジトリ":             {second("repo", "other"), false},
		"区切りの欄が無い":               {func(m map[string]any) { delete(m["slices"].([]any)[1].(map[string]any), "depends_on") }, false},
		"depends_on 自分":          {second("depends_on", []int{2}), false},
		"depends_on 後ろ":          {func(m map[string]any) { m["slices"].([]any)[0].(map[string]any)["depends_on"] = []int{2} }, false},
		"depends_on 0":           {second("depends_on", []int{0}), false},
		"depends_on 一覧に無い":       {second("depends_on", []int{9}), false},
		"depends_on 負":           {second("depends_on", []int{-1}), false},
		"depends_on 重複":          {second("depends_on", []int{1, 1}), false},
		"reason が無い":             {func(m map[string]any) { delete(m, "reason") }, false},
		"reason が空白":             {set("reason", " "), false},
		"reason 2000 文字":         {set("reason", long(2000)), true},
		"reason 2001 文字":         {set("reason", long(2001)), false},
		"evidence が空":            {set("evidence", []any{}), false},
		"evidence が無い":           {func(m map[string]any) { delete(m, "evidence") }, false},
		"evidence の note が空白":    {set("evidence", []any{map[string]any{"path": "a", "note": " "}}), false},
		"evidence の path が空":     {evidence(""), false},
		"evidence が絶対パス":         {evidence("/etc/passwd"), false},
		"evidence が親を指す":         {evidence("../a.md"), false},
		"evidence の途中が親を指す":      {evidence("docs/../../a.md"), false},
		"evidence の名前に点が 2 つ":    {evidence("docs/a..b.md"), true},
		"未知の判定":                  {set("verdict", "maybe"), false},
		"single で slices が空":     {func(m map[string]any) { m["verdict"] = "single"; m["slices"] = []any{} }, true},
		"single で slices が null": {func(m map[string]any) { m["verdict"] = "single"; m["slices"] = nil }, true},
		"single で slices が 1 件":  {func(m map[string]any) { m["verdict"] = "single"; m["slices"] = []any{j10TestSlice("a")} }, false},
		"single で evidence が空":   {func(m map[string]any) { m["verdict"] = "single"; m["slices"] = nil; m["evidence"] = []any{} }, false},
		"single で reason が空白":    {func(m map[string]any) { m["verdict"] = "single"; m["slices"] = nil; m["reason"] = "" }, false},
	}
	for name, c := range cases {
		_, err := validateJ10Output(j10TestOutput(t, c.mut), repos)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
}

// AC-51〜64: 検査に落ちた出力は、実行の結果が invalid_output になり、記録は pending のまま。
func TestJ10Run_InvalidOutputIsRecordedAndStaysPending(t *testing.T) {
	t.Parallel()
	f := newJ10Fixture(t)
	id := f.pending(t)
	inv := &j10Invoker{outputs: map[JudgmentPoint][]byte{
		JudgmentJ10: j10TestOutput(t, func(m map[string]any) { m["slices"] = []any{j10TestSlice("a")} }),
	}}
	res := f.classify(t, inv, f.conn, nil)
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v, want one invalid_output", res.Items)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J10' AND result = 'invalid_output' AND error LIKE '%J10: slices%'`); n != 1 {
		t.Errorf("invalid_output J10 runs with the reason = %d, want 1", n)
	}
	if got := f.splitState(t, id); got != SliceSplitStatePending {
		t.Errorf("slice_split.state = %q, want pending", got)
	}
}
