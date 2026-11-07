package core

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// このファイルは #450（計画を作る判断点 J2 が、サイズ M・L で論点の記録の無い計画を
// 返したときの扱い）を、実ストア・偽の判断の IF で検証する。
//
//   - 1 回目の写像が「論点の記録の無い中以上の計画」なら、同じ周の中で 1 回だけ問い直す
//   - 問い直しでも同じなら、人間待ちの保留に置く
//   - 決まっていることごとに中身・引用元の種別・引用を持つ「論点不要」は、論点の記録として扱う

// scriptedJ2Invoker は呼ばれた順に outputs を返す偽の判断の IF（尽きたら最後を返す）。
type scriptedJ2Invoker struct {
	outputs [][]byte
	cost    *float64
	calls   []JudgmentLaunchInput
	// onCall が非 nil なら、i 回目（0 始まり）の起動の中で呼ぶ（起動と写像の間に起きる並行の変化を作る）。
	onCall func(i int)
}

func (f *scriptedJ2Invoker) Available(context.Context) error { return nil }

func (f *scriptedJ2Invoker) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	i := len(f.calls)
	f.calls = append(f.calls, in)
	if f.onCall != nil {
		f.onCall(i)
	}
	if i >= len(f.outputs) {
		i = len(f.outputs) - 1
	}
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: f.outputs[i], ReportedTotalCostUSD: f.cost}, nil
}

// j2BigNoIssues は論点の記録も論点不要も無い、サイズ M の計画（summary でどの出力かを区別する）。
func j2BigNoIssues(summary string) []byte {
	return j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		m["summary"] = summary
	})
}

// j2NoIssuesDecision は有効な論点不要の決定 1 件。
func j2NoIssuesDecision(content, kind, quote string) map[string]any {
	return map[string]any{"content": content, "source_kind": kind, "quote": quote}
}

func j2BigWithNoIssuesNeeded(decisions any) []byte {
	return j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		m["no_issues_needed"] = decisions
	})
}

func validNoIssuesNeeded() []any {
	return []any{
		j2NoIssuesDecision("DECISION-ONE 方式は A に決まっている", "upstream_comment", "QUOTE-ONE A 方式で進める"),
		j2NoIssuesDecision("DECISION-TWO 対象は CLI だけ", "challenge", "QUOTE-TWO CLI のみ"),
	}
}

func validIssuesOutput() []byte {
	return j2IssuesOutput(nil)
}

// --- 出力の型 ---

func TestJ2SourceKind_ClosedSetIsInAllClosedValuesAndSchema(t *testing.T) {
	want := []string{"challenge", "upstream_issue", "upstream_comment", "referenced_issue", "hold_record"}
	var got []string
	for _, k := range J2SourceKindValues {
		got = append(got, string(k))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("J2SourceKindValues = %v, want %v", got, want)
	}
	all := map[string]bool{}
	for _, v := range AllJudgmentOutputClosedValues() {
		all[v] = true
	}
	for _, w := range want {
		if !all[w] {
			t.Errorf("AllJudgmentOutputClosedValues() lacks the source kind %q", w)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatal(err)
	}
	prop, ok := decoded["properties"].(map[string]any)["no_issues_needed"].(map[string]any)
	if !ok {
		t.Fatal("the J2 schema lacks no_issues_needed")
	}
	if !reflect.DeepEqual(prop["type"], []any{"array", "null"}) {
		t.Errorf("no_issues_needed type = %v, want [array null]", prop["type"])
	}
	if _, has := prop["minItems"]; has {
		t.Error("the schema must not force minItems (core checks it)")
	}
	items := prop["items"].(map[string]any)
	props := items["properties"].(map[string]any)
	for _, k := range []string{"content", "source_kind", "quote"} {
		p, ok := props[k].(map[string]any)
		if !ok {
			t.Fatalf("items lack %q", k)
		}
		if _, has := p["minLength"]; has {
			t.Errorf("%s: the schema must not force minLength (core checks it)", k)
		}
	}
	var enum []string
	for _, e := range props["source_kind"].(map[string]any)["enum"].([]any) {
		enum = append(enum, e.(string))
	}
	if !reflect.DeepEqual(enum, want) {
		t.Errorf("source_kind enum = %v, want %v", enum, want)
	}
	if !reflect.DeepEqual(items["required"], []any{"content", "source_kind", "quote"}) {
		t.Errorf("items required = %v", items["required"])
	}
}

func TestValidateJ2Output_NoIssuesNeeded(t *testing.T) {
	vc := j2TestContext(t, true)
	v, ok := validateJ2Output(j2BigWithNoIssuesNeeded(validNoIssuesNeeded()), vc)
	if !ok || len(v.NoIssuesNeeded) != 2 {
		t.Fatalf("valid: ok=%v v=%+v", ok, v)
	}
	if v.NoIssuesNeeded[0].SourceKind != J2SourceKindUpstreamComment || v.NoIssuesNeeded[1].Quote != "QUOTE-TWO CLI のみ" {
		t.Errorf("decisions = %+v", v.NoIssuesNeeded)
	}

	// 空・欠け・閉集合の外は、出力全体を不正にせず、論点不要としては無効（nil）になる。
	good := j2NoIssuesDecision("c", "challenge", "q")
	cases := map[string]any{
		"null":              nil,
		"empty array":       []any{},
		"empty content":     []any{j2NoIssuesDecision("  ", "challenge", "q")},
		"empty quote":       []any{j2NoIssuesDecision("c", "challenge", "")},
		"blank quote":       []any{j2NoIssuesDecision("c", "challenge", " \n\t ")},
		"empty kind":        []any{j2NoIssuesDecision("c", "", "q")},
		"unknown kind":      []any{j2NoIssuesDecision("c", "memory", "q")},
		"one bad of many":   []any{good, j2NoIssuesDecision("c", "challenge", "")},
		"keys missing":      []any{map[string]any{"content": "c"}},
		"null element":      []any{nil},
		"empty object":      []any{map[string]any{}},
		"kind with padding": []any{j2NoIssuesDecision("c", " challenge ", "q")},
	}
	for name, decisions := range cases {
		v, ok := validateJ2Output(j2BigWithNoIssuesNeeded(decisions), vc)
		if !ok {
			t.Errorf("%s: the whole output must stay valid", name)
			continue
		}
		if len(v.NoIssuesNeeded) != 0 {
			t.Errorf("%s: NoIssuesNeeded = %+v, want none", name, v.NoIssuesNeeded)
		}
	}
	// 項目そのものが無い出力も従来どおり有効。
	if v, ok := validateJ2Output(j2BigNoIssues("s"), vc); !ok || len(v.NoIssuesNeeded) != 0 {
		t.Errorf("absent: ok=%v v=%+v", ok, v)
	}
}

func TestJ2PlanDecide_NoIssuesNeededCountsAsTheRecord(t *testing.T) {
	vc := j2TestContext(t, true)
	valid, _ := validateJ2Output(j2BigWithNoIssuesNeeded(validNoIssuesNeeded()), vc)
	none, _ := validateJ2Output(j2BigNoIssues("s"), vc)
	small, _ := validateJ2Output(j2OutputFor(true, nil), vc)
	classified := &Challenge{Status: StatusClassified}
	withIssues := &Challenge{Status: StatusClassified, StageFields: StageFields{Issues: []StageIssue{{Question: "q"}}}}
	if got := j2PlanDecide(classified, valid); got != j2PlanRegister {
		t.Errorf("valid no_issues_needed: %v, want register", got)
	}
	if got := j2PlanDecide(classified, none); got != j2PlanNoIssues {
		t.Errorf("no record: %v, want no-issues", got)
	}
	if got := j2PlanDecide(classified, small); got != j2PlanRegister {
		t.Errorf("size S: %v, want register", got)
	}
	if got := j2PlanDecide(withIssues, none); got != j2PlanRegister {
		t.Errorf("issues recorded: %v, want register", got)
	}
}

func TestFormatJ2PlanBody_NoIssuesNeededSection(t *testing.T) {
	vc := j2TestContext(t, true)
	v, _ := validateJ2Output(j2BigWithNoIssuesNeeded([]any{
		j2NoIssuesDecision("DEC-A\n2 行目", "upstream_comment", "## 偽の見出し\n引用の 2 行目"),
		j2NoIssuesDecision("DEC-B", "hold_record", "QUOTE-B"),
	}), vc)
	body := formatJ2PlanBody(v)
	if body != formatJ2PlanBody(v) {
		t.Error("the body must be deterministic")
	}
	i := strings.Index(body, "## 論点を不要とした根拠")
	if i < 0 {
		t.Fatalf("body lacks the section:\n%s", body)
	}
	sec := body[i:]
	for _, want := range []string{"DEC-A 2 行目", "引用元: " + labelUpstreamComments, "> ## 偽の見出し", "> 引用の 2 行目", "DEC-B", "引用元: " + labelHoldRecord, "> QUOTE-B"} {
		if !strings.Contains(sec, want) {
			t.Errorf("section lacks %q:\n%s", want, sec)
		}
	}
	if strings.Index(sec, "DEC-A") > strings.Index(sec, "DEC-B") {
		t.Error("decisions must keep the output order")
	}
	// 引用の中の見出しは、本文の見出しにならない（行頭の "## " が増えない）。
	if got := strings.Count(body, "\n## 偽の見出し"); got != 0 {
		t.Errorf("the quote leaked a heading line:\n%s", body)
	}
	// 論点不要が無いとき、節は出ない。
	plain, _ := validateJ2Output(j2BigNoIssues("s"), vc)
	if strings.Contains(formatJ2PlanBody(plain), "論点を不要とした根拠") {
		t.Error("the section must not appear without a valid no_issues_needed")
	}
}

func TestJ2SourceKind_LabelsAreTheInputSectionLabels(t *testing.T) {
	want := map[J2SourceKind]string{
		J2SourceKindChallenge:       labelChallenge,
		J2SourceKindUpstreamIssue:   labelUpstreamIssue,
		J2SourceKindUpstreamComment: labelUpstreamComments,
		J2SourceKindReferencedIssue: labelUpstreamRefs,
		J2SourceKindHoldRecord:      labelHoldRecord,
	}
	for _, k := range J2SourceKindValues {
		if got := k.sectionLabel(); got != want[k] || got == "" {
			t.Errorf("%s: label = %q, want %q", k, got, want[k])
		}
	}
}

// --- 問い直し ---

// (a) 1 回目が中で論点なし → 問い直しで論点が返り、論点の記録（保留）になる。
func TestPlanAutoJ2_NoIssues_AsksOnceMoreAndRecordsIssues(t *testing.T) {
	for _, byID := range []bool{false, true} {
		name := "auto"
		if byID {
			name = "by id"
		}
		t.Run(name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "reask", "P1")
			inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("FIRST-SUMMARY-MARKER"), validIssuesOutput()}}
			var id *string
			if byID {
				id = idPtr(ch.ID)
			}
			res, err := f.run(t, inv, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.calls) != 2 {
				t.Fatalf("invoker calls = %d, want 2", len(inv.calls))
			}
			first, second := allSectionsText(inv.calls[0].Sections), allSectionsText(inv.calls[1].Sections)
			if strings.Contains(first, "問い直し") {
				t.Errorf("the first call must not carry the re-ask section:\n%s", first)
			}
			for _, want := range []string{"問い直し", "FIRST-SUMMARY-MARKER", "サイズ: M", "DESC-reask"} {
				if !strings.Contains(second, want) {
					t.Errorf("the second call lacks %q", want)
				}
			}
			if len(res.Items) != 2 || len(res.NotStarted) != 0 {
				t.Fatalf("result = %+v, want 2 items", res)
			}
			if res.Items[0].Result != RunResultInvalidOutput || res.Items[0].Outcome != "" || res.Items[0].Status != nil {
				t.Errorf("first item = %+v, want invalid_output", res.Items[0])
			}
			s := res.Items[1]
			if s.Result != RunResultSucceeded || s.Outcome != string(J2VerdictIssues) || s.Status == nil || *s.Status != string(StatusAwaitingHuman) {
				t.Errorf("second item = %+v", s)
			}
			if s.RunID == res.Items[0].RunID || s.ChallengeID != ch.ID {
				t.Errorf("the two items must be two runs of the challenge: %+v", res.Items)
			}
			d := f.detail(t, ch.ID)
			if d.Status != StatusAwaitingHuman || d.Issues == nil || d.IssuesHoldID == nil || len(d.Plans) != 0 {
				t.Errorf("challenge = %+v", d.Challenge)
			}
			got := f.runResults(t, ch.ID)
			sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
			if !reflect.DeepEqual(got, []RunResult{RunResultInvalidOutput, RunResultSucceeded}) {
				t.Errorf("run results = %v", got)
			}
		})
	}
}

// 問い直しの結果が有効な計画（論点不要つき）・判断できない、のときも通常どおり写す。
func TestPlanAutoJ2_NoIssues_ReaskResultIsMappedAsUsual(t *testing.T) {
	t.Run("plan with no_issues_needed", func(t *testing.T) {
		f := newJ2Fixture(t)
		ch := f.newClassified(t, "reask-plan", "P1")
		inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("first"), j2BigWithNoIssuesNeeded(validNoIssuesNeeded())}}
		res, err := f.run(t, inv, idPtr(ch.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.calls) != 2 || len(res.Items) != 2 || res.Items[1].Outcome != "plan" {
			t.Fatalf("calls=%d items=%+v", len(inv.calls), res.Items)
		}
		d := f.detail(t, ch.ID)
		if d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 || !strings.Contains(d.Plans[0].Body, "QUOTE-ONE") {
			t.Errorf("status=%s plans=%+v", d.Status, d.Plans)
		}
	})
	t.Run("uncertain", func(t *testing.T) {
		f := newJ2Fixture(t)
		ch := f.newClassified(t, "reask-unc", "P1")
		unc := j2OutputFor(false, func(m map[string]any) { m["verdict"] = "uncertain"; m["question"] = "UNC-Q" })
		inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("first"), unc}}
		res, err := f.run(t, inv, idPtr(ch.ID))
		if err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, ch.ID)
		if len(inv.calls) != 2 || res.Items[1].Outcome != "uncertain" || d.Status != StatusAwaitingHuman || d.Holds[len(d.Holds)-1].Question != "UNC-Q" {
			t.Errorf("calls=%d items=%+v holds=%+v", len(inv.calls), res.Items, d.Holds)
		}
	})
}

// (b) 問い直しでも論点なし → 人間待ちの保留に置かれ、問いにサイズと要約が入る。2 回しか呼ばない。
func TestPlanAutoJ2_NoIssues_SecondTimeHoldsForAHuman(t *testing.T) {
	for _, byID := range []bool{false, true} {
		name := "auto"
		if byID {
			name = "by id"
		}
		t.Run(name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "hold", "P1")
			before := f.detail(t, ch.ID)
			inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("FIRST-SUMMARY-MARKER"), j2BigNoIssues("SECOND-SUMMARY-MARKER")}}
			var id *string
			if byID {
				id = idPtr(ch.ID)
			}
			res, err := f.run(t, inv, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.calls) != 2 {
				t.Fatalf("invoker calls = %d, want exactly 2 (no third call)", len(inv.calls))
			}
			if len(res.Items) != 2 || res.Items[0].Result != RunResultInvalidOutput {
				t.Fatalf("items = %+v", res.Items)
			}
			s := res.Items[1]
			if s.Result != RunResultSucceeded || s.Outcome != string(J2VerdictUncertain) || s.Status == nil || *s.Status != string(StatusAwaitingHuman) {
				t.Errorf("second item = %+v, want succeeded/uncertain/awaiting_human", s)
			}
			d := f.detail(t, ch.ID)
			if d.Status != StatusAwaitingHuman || len(d.Plans) != 0 || d.Issues != nil || d.Version != before.Version+1 {
				t.Fatalf("challenge = %+v plans=%d", d.Challenge, len(d.Plans))
			}
			last := d.Holds[len(d.Holds)-1]
			if last.FromStatus != StatusClassified || !strings.HasPrefix(last.Question, NoIssuesHoldPreamble) {
				t.Errorf("hold = %+v", last)
			}
			for _, want := range []string{"SECOND-SUMMARY-MARKER", "FIRST-SUMMARY-MARKER", "1 回目に捨てた計画: サイズ M", "2 回目に捨てた計画: サイズ M"} {
				if !strings.Contains(last.Question, want) {
					t.Errorf("hold question lacks %q: %s", want, last.Question)
				}
			}
			if last.RunID == nil || *last.RunID != s.RunID {
				t.Errorf("hold run = %v, want %s", last.RunID, s.RunID)
			}

			// 保留に置いた後の周では、J2 を再試行しない。
			if _, err := f.run(t, inv, nil); err != nil {
				t.Fatal(err)
			}
			if len(inv.calls) != 2 {
				t.Errorf("J2 was retried after the hold: calls = %d", len(inv.calls))
			}
		})
	}
}

func TestNoIssuesHoldPreamble_Wording(t *testing.T) {
	for _, want := range []string{"中以上", "論点の記録", "2 回続いた", "保留"} {
		if !strings.Contains(NoIssuesHoldPreamble, want) {
			t.Errorf("NoIssuesHoldPreamble lacks %q: %s", want, NoIssuesHoldPreamble)
		}
	}
}

// (c) 引用つきの論点不要の中の計画 → 1 回で登録され、本文に引用が残り、show・承認の画面の元になる値に出る。
func TestPlanAutoJ2_NoIssuesNeeded_RegistersWithQuotesInTheBody(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "quoted", "P1")
	out := j2BigWithNoIssuesNeeded(validNoIssuesNeeded())
	inv := &scriptedJ2Invoker{outputs: [][]byte{out}}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("calls=%d items=%+v", len(inv.calls), res.Items)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 {
		t.Fatalf("status=%s plans=%d", d.Status, len(d.Plans))
	}
	body := d.Plans[0].Body
	for _, want := range []string{"## 論点を不要とした根拠", "DECISION-ONE", "QUOTE-ONE", "引用元: " + labelUpstreamComments, "DECISION-TWO", "QUOTE-TWO", "引用元: " + labelChallenge} {
		if !strings.Contains(body, want) {
			t.Errorf("plan body (show) lacks %q:\n%s", want, body)
		}
	}
	// spec には構造化した出力のまま残る。
	if d.Plans[0].Spec == nil || !strings.Contains(*d.Plans[0].Spec, `"no_issues_needed":[{`) || !strings.Contains(*d.Plans[0].Spec, "QUOTE-TWO") {
		t.Errorf("spec = %v", d.Plans[0].Spec)
	}
	// 計画を承認する画面（CLI の approve の要約・UI の API）の元になる値にも同じ本文が出る。
	prev, err := f.s.PrepareApproval(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Kind != ApprovalKindPlan || prev.PlanBody != body || !strings.Contains(prev.PlanBody, "QUOTE-ONE") {
		t.Errorf("approval preview body = %q", prev.PlanBody)
	}
}

// (d) 引用の欠けた論点不要は、論点なしと同じ（問い直しへ回る）。
func TestPlanAutoJ2_NoIssuesNeeded_IncompleteGoesToTheReask(t *testing.T) {
	good := j2NoIssuesDecision("c", "challenge", "q")
	cases := map[string]any{
		"empty array":   []any{},
		"empty quote":   []any{j2NoIssuesDecision("c", "challenge", "")},
		"empty kind":    []any{j2NoIssuesDecision("c", "", "q")},
		"unknown kind":  []any{j2NoIssuesDecision("c", "memory", "q")},
		"empty content": []any{j2NoIssuesDecision("", "challenge", "q")},
		"one bad":       []any{good, j2NoIssuesDecision("c", "hold_record", " ")},
	}
	for name, decisions := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "incomplete", "P1")
			inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigWithNoIssuesNeeded(decisions), validIssuesOutput()}}
			res, err := f.run(t, inv, idPtr(ch.ID))
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.calls) != 2 || res.Items[0].Result != RunResultInvalidOutput || res.Items[1].Outcome != string(J2VerdictIssues) {
				t.Errorf("calls=%d items=%+v", len(inv.calls), res.Items)
			}
			if d := f.detail(t, ch.ID); len(d.Plans) != 0 {
				t.Errorf("a plan was registered: %+v", d.Plans)
			}
		})
	}
}

// (e) サイズ S は問い直さない（論点不要の有無を問わず登録する）。
func TestPlanAutoJ2_NoIssues_SizeSIsNotAskedAgain(t *testing.T) {
	for name, mut := range map[string]func(m map[string]any){
		"no field":          nil,
		"incomplete fields": func(m map[string]any) { m["no_issues_needed"] = []any{j2NoIssuesDecision("c", "challenge", "")} },
		"valid fields":      func(m map[string]any) { m["no_issues_needed"] = validNoIssuesNeeded() },
	} {
		t.Run(name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "small", "P1")
			inv := &scriptedJ2Invoker{outputs: [][]byte{j2OutputFor(false, mut)}}
			res, err := f.run(t, inv, idPtr(ch.ID))
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.calls) != 1 || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
				t.Errorf("calls=%d items=%+v", len(inv.calls), res.Items)
			}
			if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval {
				t.Errorf("status = %s", d.Status)
			}
		})
	}
}

// 論点の記録があるなら問い直さない。予想が空の保留（論点不要の検査の後）も問い直さない。
func TestPlanAutoJ2_NoIssues_OtherMappingsDoNotAskAgain(t *testing.T) {
	t.Run("expected empty with valid no_issues_needed", func(t *testing.T) {
		f := newJ2Fixture(t)
		ch := f.newClassified(t, "noexp", "P1")
		out := j2OutputFor(false, func(m map[string]any) {
			m["size"] = "M"
			m["no_issues_needed"] = validNoIssuesNeeded()
			m["expected"] = []any{}
			m["question"] = "EXP-Q"
		})
		inv := &scriptedJ2Invoker{outputs: [][]byte{out}}
		res, err := f.run(t, inv, idPtr(ch.ID))
		if err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, ch.ID)
		if len(inv.calls) != 1 || res.Items[0].Outcome != "uncertain" || d.Status != StatusAwaitingHuman || !strings.HasPrefix(d.Holds[len(d.Holds)-1].Question, ExpectedEmptyHoldPreamble) {
			t.Errorf("calls=%d items=%+v holds=%+v", len(inv.calls), res.Items, d.Holds)
		}
	})
	t.Run("issues recorded", func(t *testing.T) {
		f := newJ2Fixture(t)
		ch := f.newClassified(t, "rec", "P1")
		f.recordIssues(t, ch.ID)
		inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("s")}}
		if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
			t.Fatal(err)
		}
		if len(inv.calls) != 1 || f.detail(t, ch.ID).Status != StatusAwaitingPlanApproval {
			t.Errorf("calls=%d", len(inv.calls))
		}
	})
}

// (f) 問い直しを起動できない周（周の上限）では、1 回目の invalid_output だけが残り、次の周で通常の
// 自動の対象として再試行される。連続失敗が既存の上限（既定 2 回）に達した周では、問い直しも 1 回目と
// 同じ連続失敗の上限の検査を通るので（#469）、問い直しを起動せず保留（failure_limit）に置く。
func TestPlanAutoJ2_NoIssues_ReaskNotStartableKeepsFirstInvalidAndHitsTheFailureLimit(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "limit", "P1")
	cost := 3.0
	inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("only")}, cost: &cost}
	limit := f.agent.FailureLimit
	if limit < 2 {
		t.Fatalf("fixture failure limit = %d, this test needs 2 or more", limit)
	}
	for i := 0; i < limit-1; i++ {
		// 周の上限 6・J2 の上限 5・1 回目の費用 3 → 問い直し（3+5 > 6）は起動できない。
		res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 6), nil))
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
			t.Fatalf("cycle %d: items = %+v, want the first invalid_output only", i, res.Items)
		}
		if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget || res.NotStarted[0].ChallengeID != ch.ID {
			t.Fatalf("cycle %d: not started = %+v, want cycle_budget", i, res.NotStarted)
		}
		if st := f.detail(t, ch.ID).Status; st != StatusClassified {
			t.Fatalf("cycle %d: status = %s, want classified", i, st)
		}
	}
	if len(inv.calls) != limit-1 {
		t.Fatalf("calls = %d, want %d (the re-ask never started)", len(inv.calls), limit-1)
	}
	// 上限に達する周: 1 回目の前の検査は通り（連続失敗 limit-1 回）、1 回目の invalid_output で limit 回に
	// 達する。周の上限は十分あるが、問い直しは起動しない（#469: 起動すると上限を超える）。
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != limit {
		t.Fatalf("calls = %d, want %d (the re-ask must not start past the failure limit)", len(inv.calls), limit)
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v, want the first invalid_output only", res.Items)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit || res.NotStarted[0].ChallengeID != ch.ID {
		t.Fatalf("not started = %+v, want failure_limit", res.NotStarted)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "J2") {
		t.Errorf("status=%s holds=%+v, want a failure_limit hold", d.Status, d.Holds)
	}
	// 保留に置いた後の周では、判断を起動しない。
	if _, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), nil)); err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != limit {
		t.Errorf("calls = %d after the hold, want %d", len(inv.calls), limit)
	}
}

// ID を指定した個別の操作で、問い直しを起動できないとき、先の結果を返す（エラーにしない）。
func TestPlanAutoJ2_NoIssues_ByIDReaskNotStartableReturnsTheFirstResult(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "byid", "P1")
	cost := 3.0
	inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("only")}, cost: &cost}
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 6), idPtr(ch.ID)))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v (want the first result, not an error)", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("result = %+v", res)
	}
}

// (#469) ID を指定した個別の操作は、1 回目も連続失敗の上限の検査を通らない（§判断点の共通の規則）。
// 問い直しも同じで、上限に達していても起動する。
func TestPlanAutoJ2_NoIssues_ByIDReaskDoesNotApplyTheFailureLimit(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "byid-limit", "P1")
	if f.agent.FailureLimit != 2 {
		t.Fatalf("fixture failure limit = %d, this test assumes 2 (1 auto run + the by-id first run reach it)", f.agent.FailureLimit)
	}
	inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("first"), j2BigNoIssues("first"), validIssuesOutput()}}
	if _, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 6), nil)); err != nil {
		t.Fatal(err)
	}
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), idPtr(ch.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 3 || len(res.Items) != 2 || res.Items[1].Outcome != string(J2VerdictIssues) {
		t.Fatalf("calls=%d result=%+v, want the re-ask to run by id", len(inv.calls), res)
	}
}

// (#469) ID を省略した自動の対象では、問い直しの直前にも取り込み元の状態を読み直す（1 回目の前の
// j2StillEligibleForAuto と同じ検査）。1 回目の起動の間に取り込み元が見つからなくなったら問い直さない。
func TestPlanAutoJ2_NoIssues_AutoReaskRechecksEligibility(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "reask-elig", "P1")
	f.bind(t, ch, 7, simpleThread("UP-BODY"))
	inv := &scriptedJ2Invoker{outputs: [][]byte{j2OutputFor(true, func(m map[string]any) {
		m["size"] = "M"
		m["summary"] = "first"
	}), validIssuesOutput()}}
	inv.onCall = func(i int) {
		if i == 0 {
			setBindingStateForTest(t, f.s, ch.ID, upstreamStateMissing, policyStateInPolicy)
		}
	}
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (the re-ask must not start for an excluded challenge)", len(inv.calls))
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v", res.Items)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamMissing {
		t.Fatalf("not started = %+v, want upstream_missing", res.NotStarted)
	}
}

// (#469) 問い直しは別の run で、費用は run ごとに 1 回だけ周の消費額に入る（1 回目の費用を
// 問い直しの run に重ねて数えない）。
func TestPlanAutoJ2_NoIssues_ReaskCostIsCountedOncePerRun(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "reask-cost", "P1")
	first, second := 1.25, 2.5
	inv := &scriptedJ2Invoker{outputs: [][]byte{j2BigNoIssues("first"), validIssuesOutput()}}
	inv.onCall = func(i int) {
		if i == 0 {
			inv.cost = &first
		} else {
			inv.cost = &second
		}
	}
	cycleID := f.beginCycle(t, 300)
	res, err := f.s.PlanAutoJ2(context.Background(), f.input(inv, cycleID, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 2 || len(res.Items) != 2 {
		t.Fatalf("calls=%d items=%+v, want the re-ask to run", len(inv.calls), res.Items)
	}
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &ch.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	ended, err := f.s.EndCycle(context.Background(), cycleID, CycleResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if ended.SpentUSD != first+second {
		t.Errorf("cycle spent = %v, want %v (each run once)", ended.SpentUSD, first+second)
	}
}

// 問い直しの run が失敗したら、その結果をそのまま返す（保留に置かない）。
func TestPlanAutoJ2_NoIssues_ReaskFailureIsReturnedAsIs(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "reask-fail", "P1")
	inv := &failSecondJ2Invoker{first: j2BigNoIssues("first")}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || res.Items[1].Result != RunResultErrored {
		t.Fatalf("items = %+v", res.Items)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusClassified || len(d.Holds) != 0 {
		t.Errorf("challenge = %+v holds=%+v", d.Challenge, d.Holds)
	}
}

type failSecondJ2Invoker struct {
	first []byte
	n     int
}

func (f *failSecondJ2Invoker) Available(context.Context) error { return nil }

func (f *failSecondJ2Invoker) InvokeJudgment(context.Context, JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	f.n++
	if f.n == 1 {
		return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: f.first}, nil
	}
	return JudgmentLaunchOutput{Result: RunResultErrored}, nil
}
