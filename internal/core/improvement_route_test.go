package core

import "testing"

func TestImprovementRoute(t *testing.T) {
	decl := &ConnectorsDeclaration{Repos: []ConnectorRepo{
		{Name: "fw", Remote: "MasaNami/Flywheel"},
		{Name: "ws", Remote: "acme/workspace"},
	}}
	noFW := &ConnectorsDeclaration{Repos: []ConnectorRepo{{Name: "ws", Remote: "acme/workspace"}}}
	const issue = "成果物の経路: Issue"
	cases := []struct {
		name   string
		layer  OccurrenceLayer
		decl   *ConnectorsDeclaration
		origin string
		want   string
	}{
		{"AC-366 prompt PR", OccurrenceLayerPrompt, decl, "", "成果物の経路: PR（対象のリポジトリ: MasaNami/Flywheel）"},
		{"AC-366 entry_skill PR", OccurrenceLayerEntrySkill, decl, "", "成果物の経路: PR（対象のリポジトリ: MasaNami/Flywheel）"},
		{"AC-367 prompt flywheel 無し", OccurrenceLayerPrompt, noFW, "", issue},
		{"AC-367 entry_skill flywheel 無し", OccurrenceLayerEntrySkill, noFW, "", issue},
		{"AC-368 connector_skill", OccurrenceLayerConnectorSkill, decl, "", issue},
		{"AC-369 knowledge 一致 https", OccurrenceLayerKnowledge, decl, "https://github.com/acme/workspace.git", "成果物の経路: ワークスペースの変更（対象のリポジトリ: acme/workspace）"},
		{"AC-369 knowledge 一致 ssh", OccurrenceLayerKnowledge, decl, "git@github.com:Acme/Workspace.git", "成果物の経路: ワークスペースの変更（対象のリポジトリ: acme/workspace）"},
		{"AC-369 knowledge 不一致", OccurrenceLayerKnowledge, decl, "https://github.com/other/x.git", issue},
		{"AC-369 knowledge origin 空", OccurrenceLayerKnowledge, decl, "", issue},
		{"AC-370 nil prompt", OccurrenceLayerPrompt, nil, "", issue},
		{"AC-370 nil knowledge", OccurrenceLayerKnowledge, nil, "https://github.com/acme/workspace.git", issue},
		{"層 none", OccurrenceLayerNone, decl, "", issue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := improvementRoute(c.layer, c.decl, c.origin); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}
