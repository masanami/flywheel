package core

import (
	"context"
	"strings"
	"testing"
)

// このファイルは、計画を作る判断点の入力の操作の行に、ワークスペースの定義のフローを選ぶ目安（定義の説明）を添える口を検証する。

const j2HintConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "tool", "form": "flow", "permission_mode": "auto", "operations": [
      {"id": "impl", "flow": "ticket", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "light", "flow": "ticket-light", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "ui", "flow": "ui-only", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "docs", "flow": "docs-only", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "plain", "flow": "no-hint", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "broken", "flow": "broken-flow", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "missing", "flow": "missing-flow", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "flow-repo", "remote": "o/flow", "default_branch": "main", "connector": "tool"}
  ]
}`

func hintFlowCheck(name, description string, ok bool) *FlowCheckOutput {
	d := FlowCheckDefinition{Name: name, Source: FlowDefinitionSourceWorkspace, Kind: "new", OK: ok, Description: description}
	if ok {
		d.SHA256 = strings.Repeat("a", 64)
	}
	return &FlowCheckOutput{Result: RunResultSucceeded, Definitions: []FlowCheckDefinition{d}}
}

// j2OperationLines は区画の本文の操作の行を、操作の id ごとに返す。
func j2OperationLines(content string) map[string]string {
	lines := map[string]string{}
	for _, l := range strings.Split(content, "\n") {
		if rest, found := strings.CutPrefix(l, "    - id: "); found {
			id, _, _ := strings.Cut(rest, "｜")
			lines[id] = l
		}
	}
	return lines
}

func TestJ2FlowHint_OperationLinesInTheJ2Input(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.rejectCompletion(t, "REASON")
	f.agent.PositionFile = "pos.md"
	writeWorkspaceFile(t, f.s.workspace, f.agent.PositionFile, "POSITION")
	conn, err := parseConnectorsDeclaration([]byte(j2HintConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.validate(); err != nil {
		t.Fatal(err)
	}
	f.s.SetFlowExecutable("/bin/flywheel")
	client := &fakeContractClient{checkFor: map[string]*FlowCheckOutput{
		"ui-only":      hintFlowCheck("ui-only", "画面の見た目だけを直すタスク\n（ストアは触らない）\n", true),
		"docs-only":    hintFlowCheck("docs-only", "文書だけのタスク", true),
		"no-hint":      hintFlowCheck("no-hint", "  \n", true),
		"broken-flow":  hintFlowCheck("broken-flow", "壊れた定義の説明", false),
		"missing-flow": {Result: RunResultErrored, ErrorSummary: "boom"},
	}}
	before := f.detail(t, f.id)
	ch := before.Challenge
	prepared, ns, err := f.s.prepareJ2(context.Background(), ch, J2AutoInput{AgentDecl: f.agent, Upstream: f.upstream, ConnDecl: conn, Contract: client})
	if err != nil || ns != nil {
		t.Fatalf("prepareJ2: ns %+v err %v", ns, err)
	}
	sec, ok := sectionByLabel(prepared.Sections, "対象リポジトリと操作の宣言")
	if !ok {
		t.Fatal("no declaration section")
	}
	lines := j2OperationLines(sec.Content)
	const head = "｜対話前提: いいえ｜成果物: pr"
	for id, suffix := range map[string]string{
		"impl":    "｜フロー: 標準の実装（ticket）",
		"light":   "｜フロー: 簡略な実装（ticket-light）",
		"ui":      "｜フロー: ui-only｜目安: 画面の見た目だけを直すタスク （ストアは触らない）",
		"docs":    "｜フロー: docs-only｜目安: 文書だけのタスク",
		"plain":   "｜フロー: no-hint",
		"broken":  "｜フロー: broken-flow",
		"missing": "｜フロー: missing-flow",
	} {
		if want := "    - id: " + id + head + suffix; lines[id] != want {
			t.Errorf("operation %s: line = %q, want %q", id, lines[id], want)
		}
	}
	// 組み込みの名前は確かめない。
	for _, command := range client.checks {
		if name := command[len(command)-1]; name == "ticket" || name == "ticket-light" {
			t.Errorf("a built-in flow was checked: %v", command)
		}
	}
	// 壊れた・無い定義があっても、保留は立てず、課題の状態も変えない。
	after := f.detail(t, f.id)
	if len(after.Holds) != len(before.Holds) || after.Status != before.Status {
		t.Errorf("preparing the J2 input changed the challenge: holds %d → %d, status %s → %s",
			len(before.Holds), len(after.Holds), before.Status, after.Status)
	}
}

// 組み込みの定義（ワークスペースに置いていない名前）の確かめの結果には、説明があっても目安を付けない。
func TestJ2FlowHint_BuiltinSourceHasNoHint(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	s.SetFlowExecutable("/bin/flywheel")
	conn, err := parseConnectorsDeclaration([]byte(j2HintConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	out := hintFlowCheck("ui-only", "組み込みの説明", true)
	out.Definitions[0].Source = FlowDefinitionSourceBuiltin
	client := &fakeContractClient{checkOut: out}
	if hints := s.j2FlowHints(context.Background(), J2AutoInput{AgentDecl: defaultAgentDeclaration(), ConnDecl: conn, Contract: client}); len(hints) != 0 {
		t.Errorf("hints = %v, want none", hints)
	}
}
