package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// このファイルは、計画を作る判断点がフローを選ぶ口（入力の操作の行・flow_reason の検査・planned_flow・計画の本文）を検証する。

const j2FlowConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "tool", "form": "flow", "permission_mode": "auto", "operations": [
      {"id": "impl", "flow": "ticket", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "light", "flow": "ticket-light", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]},
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "flow-repo", "remote": "o/flow", "default_branch": "main", "connector": "tool"},
    {"name": "brief-repo", "remote": "o/brief", "default_branch": "main", "connector": "direct"}
  ]
}`

func j2FlowContext(t *testing.T) j2ValidationContext {
	t.Helper()
	decl, err := parseConnectorsDeclaration([]byte(j2FlowConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := decl.validate(); err != nil {
		t.Fatal(err)
	}
	return j2ValidationContext{Agent: defaultAgentDeclaration(), Conn: decl}
}

// flowPlanOutput は 1 タスクの plan の出力。mut はタスクの欄を書き換える。
func flowPlanOutput(repo, op string, mut func(task map[string]any)) []byte {
	return j2PlanOutput(func(m map[string]any) {
		task := j2TestTask("a", repo, op)
		if mut != nil {
			mut(task)
		}
		m["tasks"] = []any{task}
	})
}

func TestJ2FlowReason_FlowOperationRequiresAUsableReason(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	valid := func(task map[string]any) {
		task["flow_reason"] = "文書だけの変更なので簡略なフローで足りる"
	}
	if _, reason := validateJ2OutputReason(flowPlanOutput("flow-repo", "light", valid), vc); reason != "" {
		t.Fatalf("valid output rejected: %s", reason)
	}
	for name, mut := range map[string]func(map[string]any){
		"AC-43 flow_reason が無い":       nil,
		"AC-44 flow_reason が null":    func(m map[string]any) { m["flow_reason"] = nil },
		"AC-45 flow_reason が空白だけ":     func(m map[string]any) { m["flow_reason"] = " \n\t　" },
		"flow_reason が空":              func(m map[string]any) { m["flow_reason"] = "" },
		"AC-46 flow_reason が 2001 文字": func(m map[string]any) { m["flow_reason"] = strings.Repeat("あ", 2001) },
	} {
		if _, reason := validateJ2OutputReason(flowPlanOutput("flow-repo", "impl", mut), vc); reason == "" {
			t.Errorf("%s: want invalid", name)
		}
	}
	// AC-46: 2000 文字は通る。
	ok := func(m map[string]any) { m["flow_reason"] = strings.Repeat("あ", 2000) }
	if _, reason := validateJ2OutputReason(flowPlanOutput("flow-repo", "impl", ok), vc); reason != "" {
		t.Errorf("2000 characters rejected: %s", reason)
	}
}

func TestJ2FlowReason_NonFlowOperation(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	// AC-47: 空でない flow_reason は不正。
	set := func(m map[string]any) { m["flow_reason"] = "理由" }
	if _, reason := validateJ2OutputReason(flowPlanOutput("brief-repo", "brief", set), vc); !strings.Contains(reason, "flow_reason") {
		t.Errorf("non-empty flow_reason: reason = %q, want a flow_reason error", reason)
	}
	// AC-48: 無い・null・空の文字列は通る。
	for name, mut := range map[string]func(map[string]any){
		"無い":   nil,
		"null": func(m map[string]any) { m["flow_reason"] = nil },
		"空":    func(m map[string]any) { m["flow_reason"] = "" },
	} {
		if _, reason := validateJ2OutputReason(flowPlanOutput("brief-repo", "brief", mut), vc); strings.Contains(reason, "flow_reason") {
			t.Errorf("%s: flow_reason rejected: %s", name, reason)
		}
	}
}

// AC-49・AC-50: 続きのタスクを足す判断とフォローアップのタスク化も、同じ検査を通る（足す形は validateJ2TaskFields を共有する）。
func TestJ2FlowReason_ExpandAndFollowupRequireItToo(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	for _, origin := range []TaskAdditionOrigin{TaskAdditionOriginExpand, TaskAdditionOriginFollowup} {
		for _, withReason := range []bool{false, true} {
			task := j2TestTask("b", "flow-repo", "impl")
			task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
			if withReason {
				task["flow_reason"] = "標準で進める"
			}
			data, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": []any{task}})
			ec := j2ExpandValidationContext{
				Agent: vc.Agent, Conn: vc.Conn, Repos: []string{"flow-repo"}, TakenKeys: map[string]bool{}, DepKeys: map[string]bool{}, Origin: origin,
			}
			_, reason := validateJ2ExpandOutputReason(data, ec)
			if withReason && reason != "" {
				t.Errorf("origin %v: valid add rejected: %s", origin, reason)
			}
			if !withReason && !strings.Contains(reason, "flow_reason") {
				t.Errorf("origin %v: reason = %q, want a flow_reason error", origin, reason)
			}
		}
	}
}

// AC-51: 出力の型のタスクの欄は、変更の前の一覧に flow_reason を足したものだけ（planned_flow・モデル・effort は型に無い）。
func TestJ2FlowReason_SchemaAddsOnlyFlowReason(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties struct {
			Tasks struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"tasks"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(j2OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema.Properties.Tasks.Items.Properties
	if _, ok := props["flow_reason"]; !ok {
		t.Error("flow_reason is missing")
	}
	if _, ok := props["planned_flow"]; ok {
		t.Error("planned_flow must not be in the model output type")
	}
	if len(props) != 14 {
		t.Errorf("task fields = %d, want the 12 before plus flow_reason and covers", len(props))
	}
}

// AC-41・AC-42: 入力の操作の行は、flow の形の操作だけ「｜フロー: <名前>（<識別子>）」で終わる。
func TestJ2FlowReason_InputOperationLines(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	out := formatJ2DeclarationSection(vc.Conn, false, vc.Agent)
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "- id: "); i >= 0 {
			lines[strings.Fields(l[i+6:])[0]] = l
		}
	}
	for id, suffix := range map[string]string{"impl": "｜フロー: 標準の実装（ticket）", "light": "｜フロー: 簡略な実装（ticket-light）"} {
		found := false
		for k, l := range lines {
			if strings.HasPrefix(k, id) && strings.HasSuffix(l, suffix) {
				found = true
			}
		}
		if !found {
			t.Errorf("operation %s: no line ends with %q in\n%s", id, suffix, out)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "id: brief") && strings.Contains(l, "フロー:") {
			t.Errorf("a non-flow operation line has a flow: %s", l)
		}
	}
	if got := flowLabel("something-else"); got != "something-else" {
		t.Errorf("flowLabel(unknown) = %q, want the id only", got)
	}
	if got := flowLabel("reflect"); got != "統合ブランチを上げる（reflect）" {
		t.Errorf("flowLabel(reflect) = %q", got)
	}
}

// AC-52・AC-53・AC-54: 計画の本文のフローと理由の行は、flow の形のタスクだけにある。
func TestJ2FlowReason_PlanBodyLines(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	out := j2PlanOutput(func(m map[string]any) {
		a := j2TestTask("a", "flow-repo", "light")
		a["flow_reason"] = "文書だけの変更"
		a["budget_impl_usd"], a["budget_review_usd"] = 2.0, 1.0
		b := j2TestTask("b", "flow-repo", "impl")
		b["flow_reason"] = "権限に触れる"
		b["budget_impl_usd"], b["budget_review_usd"] = 2.0, 1.0
		m["tasks"] = []any{a, b}
		m["uses_integration"] = true
	})
	v, reason := validateJ2OutputReason(out, vc)
	if reason != "" {
		t.Fatal(reason)
	}
	body := formatJ2PlanBody(v)
	for _, want := range []string{
		"   - 操作: light\n   - フロー: 簡略な実装（ticket-light）\n   - フローを選んだ理由: 文書だけの変更\n",
		"   - 操作: impl\n   - フロー: 標準の実装（ticket）\n   - フローを選んだ理由: 権限に触れる\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	nv, reason := validateJ2OutputReason(flowPlanOutput("brief-repo", "brief", nil), vc)
	if reason != "" {
		t.Fatal(reason)
	}
	if nb := formatJ2PlanBody(nv); strings.Contains(nb, "フロー:") || strings.Contains(nb, "フローを選んだ理由:") {
		t.Errorf("a non-flow task has flow lines:\n%s", nb)
	}
}

// 検査で spec の tasks[] の flow の形の要素へ planned_flow が足され、ほかの欄は出力のまま残る（flow_reason を含む）。
func TestJ2FlowReason_SpecKeepsOutputAndAddsPlannedFlow(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	out := flowPlanOutput("flow-repo", "light", func(m map[string]any) { m["flow_reason"] = "理由 <&>" })
	v, reason := validateJ2OutputReason(out, vc)
	if reason != "" {
		t.Fatal(reason)
	}
	var got struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(v.Spec, &got); err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0]["planned_flow"] != "ticket-light" || got.Tasks[0]["flow_reason"] != "理由 <&>" {
		t.Errorf("task = %v", got.Tasks[0])
	}
	var want any
	_ = json.Unmarshal(out, &want)
	var gotAny any
	raw := strings.Replace(string(v.Spec), `,"planned_flow":"ticket-light"`, "", 1)
	_ = json.Unmarshal([]byte(raw), &gotAny)
	if !jsonEqual(gotAny, want) {
		t.Errorf("spec without planned_flow = %s, want the output", raw)
	}
	// 再検査（委譲の段。SkipFlowReason）も通り、planned_flow を二重に足さない。
	vc.SkipFlowReason = true
	rv, reason := validateJ2OutputReason(v.Spec, vc)
	if reason != "" {
		t.Errorf("revalidation: %s", reason)
	} else if strings.Count(string(rv.Spec), "planned_flow") != 1 {
		t.Errorf("revalidated spec = %s, want one planned_flow", rv.Spec)
	}
}

// AC-58: 検査の後に宣言の操作の flow を変えても、承認の前の計画の読み取りの planned_flow は検査したときの値のまま。
func TestJ2FlowReason_PlannedFlowIsFrozenAtValidation(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	decl, err := parseConnectorsDeclaration([]byte(j2FlowConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = decl
	ch := f.newClassified(t, "flow plan", "P1")
	out := flowPlanOutput("flow-repo", "light", func(m map[string]any) { m["flow_reason"] = "文書だけ" })
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	for i := range decl.Connectors {
		for j := range decl.Connectors[i].Operations {
			if decl.Connectors[i].Operations[j].ID == "light" {
				decl.Connectors[i].Operations[j].Flow = "ticket"
			}
		}
	}
	pv, err := f.s.GetPlanView(t.Context(), ch.ID, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Tasks) != 1 || pv.Tasks[0].PlannedFlow != "ticket-light" {
		t.Errorf("tasks = %+v, want planned_flow ticket-light", pv.Tasks)
	}
}

// S2 より前に承認した計画（flow_reason が無い spec）は、委譲の段の検査し直しでは flow の形の操作でも通り、planned_flow を足さない。
func TestJ2FlowReason_RecheckOfStoredSpecDoesNotRequireIt(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	out := flowPlanOutput("flow-repo", "impl", nil)
	if _, reason := validateJ2OutputReason(out, vc); reason == "" {
		t.Fatal("fresh output without flow_reason must be invalid")
	}
	vc.SkipFlowReason = true
	v, reason := validateJ2OutputReason(out, vc)
	if reason != "" {
		t.Fatalf("recheck rejected a legacy spec: %s", reason)
	}
	if strings.Contains(string(v.Spec), "planned_flow") {
		t.Errorf("recheck must not add planned_flow: %s", v.Spec)
	}
}

// 足す依頼の保存済みの spec の検査し直し（SkipFlowReason）は、flow_reason の無いタスクも通す。
func TestJ2FlowReason_ExpandRecheckDoesNotRequireIt(t *testing.T) {
	t.Parallel()
	vc := j2FlowContext(t)
	task := j2TestTask("b", "flow-repo", "impl")
	task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
	data, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": []any{task}})
	ec := j2ExpandValidationContext{Agent: vc.Agent, Conn: vc.Conn, Repos: []string{"flow-repo"}, TakenKeys: map[string]bool{}, DepKeys: map[string]bool{}}
	if _, reason := validateJ2ExpandOutputReason(data, ec); reason == "" {
		t.Fatal("fresh add without flow_reason must be invalid")
	}
	ec.SkipFlowReason = true
	if _, reason := validateJ2ExpandOutputReason(data, ec); reason != "" {
		t.Errorf("recheck rejected: %s", reason)
	}
}

// S2 より前に承認した計画（flow_reason が無い spec）も、flow の形の委譲先で委譲を始められる。
func TestFlowLaunch_LegacyApprovedPlanWithoutFlowReasonStarts(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 1)
	conn, err := parseConnectorsDeclaration([]byte(flowMultiConnectorsJSON()))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	f.newInProgress(t, "legacy", "P1", multiPlan(false, multiTask{key: "a"}))
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 1 || f.client.starts[0][1] != "flow" {
		t.Fatalf("starts = %q, want one flow start", f.client.starts)
	}
}

// AC-139・AC-140: 承認の前の計画の読み取りは、計画の出力の記録の planned_flow・flow_reason を出す。flow の形でない操作は空。
func TestPlanView_PreApprovalFlowAndReasonFromSpec_AC139_AC140(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	decl, err := parseConnectorsDeclaration([]byte(j2FlowConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = decl
	ch := f.newClassified(t, "flow plan", "P1")
	out := flowPlanOutput("flow-repo", "light", func(m map[string]any) { m["flow_reason"] = "文書だけ" })
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	pv, err := f.s.GetPlanView(t.Context(), ch.ID, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Tasks) != 1 || pv.Tasks[0].PlannedFlow != "ticket-light" || pv.Tasks[0].FlowReason != "文書だけ" {
		t.Errorf("tasks = %+v, want ticket-light and 文書だけ", pv.Tasks)
	}
}
