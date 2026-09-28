package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeConnectorsJSON はワークスペース dir/.flywheel/connectors.json に content
// を書き、dir を返す（sources_test.go の writeSourcesJSON と同じ規則）。
func writeConnectorsJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	fwDir := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fwDir, err)
	}
	if err := os.WriteFile(filepath.Join(fwDir, "connectors.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write connectors.json: %v", err)
	}
	return dir
}

const validConnectorsExample = `{
	"version": 1,
	"human_question_kinds": [
		{"id": "distribution", "label": "配布形態"},
		{"id": "responsibility_boundary", "label": "3 リポジトリ間の責務境界"}
	],
	"connectors": [
		{
			"id": "claude-harness",
			"form": "plugin",
			"permission_mode": "auto",
			"operations": [
				{"id": "impl", "invocation": "/claude-harness:impl {issue_number}", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
				{"id": "define-feature", "invocation": "/claude-harness:define-feature {issue_url}", "interactive": true, "counterpart": "parent", "child_may_decide": false, "artifacts": "pr"}
			]
		},
		{
			"id": "direct",
			"form": "brief",
			"permission_mode": "auto",
			"operations": [{"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]
		}
	],
	"repos": [
		{"name": "flywheel", "remote": "masanami/flywheel", "default_branch": "main", "connector": "claude-harness", "slots": {"provider": "clone", "paths": [".flywheel/repos/flywheel"]}},
		{"name": "claude-harness", "remote": "masanami/claude-harness", "default_branch": "main", "connector": "direct", "slots": {"provider": "clone", "paths": [".flywheel/repos/claude-harness"]}}
	]
}`

// 仕様（§IF / API）に載っている例をそのまま解釈できることを確認する。
func TestLoadConnectorsDeclaration_ValidFullExample(t *testing.T) {
	dir := writeConnectorsJSON(t, validConnectorsExample)
	decl, err := LoadConnectorsDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadConnectorsDeclaration() error = %v, want nil", err)
	}
	if decl.Version != 1 {
		t.Errorf("Version = %d, want 1", decl.Version)
	}
	if len(decl.HumanQuestionKinds) != 2 {
		t.Fatalf("len(HumanQuestionKinds) = %d, want 2", len(decl.HumanQuestionKinds))
	}
	if len(decl.Connectors) != 2 {
		t.Fatalf("len(Connectors) = %d, want 2", len(decl.Connectors))
	}
	if len(decl.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(decl.Repos))
	}
	harness := decl.Connectors[0]
	if harness.Form != ConnectorFormPlugin {
		t.Errorf("Form = %q, want plugin", harness.Form)
	}
	if len(harness.Operations) != 2 {
		t.Fatalf("len(Operations) = %d, want 2", len(harness.Operations))
	}
	if harness.Operations[1].Counterpart != CounterpartParent {
		t.Errorf("Operations[1].Counterpart = %q, want parent", harness.Operations[1].Counterpart)
	}
	// 仕様の例の "impl" 操作は counterpart を省略している（interactive: false
	// なので対話相手は意味を持たないが、キーとしては省略）。よって
	// DefaultsUsed は真になる。
	if !decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = false, want true (例の \"impl\" 操作は counterpart を省略している)")
	}
}

// --- ファイルが無い場合: config_not_found ---

func TestLoadConnectorsDeclaration_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConnectorsDeclaration(dir)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("error = %v, want ErrConfigNotFound", err)
	}
}

// --- AC-17: 未知のキー（最上位・接続ツール・操作・リポジトリの各要素） ---

func TestLoadConnectorsDeclaration_UnknownTopLevelKey(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [], "repos": [], "unknown": true}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_UnknownConnectorKey(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "unknown": true}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_UnknownOperationKey(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op", "unknown": true}]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_UnknownRepoKey(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "unknown": true}]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- AC-18: form の閉集合 ---

func TestLoadConnectorsDeclaration_FormMustBeInClosedSet(t *testing.T) {
	for _, form := range []string{"", "bypassPermissions", "cliX", "PLUGIN"} {
		t.Run(form, func(t *testing.T) {
			dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
				{"id": "a", "form": "`+form+`", "permission_mode": "auto", "operations": [{"id": "op"}]}
			], "repos": []}`)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- AC-19: permission_mode の閉集合（bypassPermissions を含む） ---

func TestLoadConnectorsDeclaration_PermissionModeMustBeInClosedSet(t *testing.T) {
	for _, mode := range []string{"bypassPermissions", "", "Auto"} {
		t.Run(mode, func(t *testing.T) {
			dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
				{"id": "a", "form": "brief", "permission_mode": "`+mode+`", "operations": [{"id": "op"}]}
			], "repos": []}`)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- AC-20: invocation の閉集合外の差し込み ---

func TestLoadConnectorsDeclaration_InvocationDisallowedPlaceholder(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "plugin", "permission_mode": "auto", "operations": [
			{"id": "op", "invocation": "/x {title}"}
		]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_InvocationAllowedPlaceholders(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "plugin", "permission_mode": "auto", "operations": [
			{"id": "op", "invocation": "/x {issue_number} {issue_url} {external_key} {challenge_id}"}
		]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

// --- AC-21: リポジトリ名の重複 ---

func TestLoadConnectorsDeclaration_DuplicateRepoName(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [
		{"name": "r", "remote": "o/r1", "default_branch": "main", "connector": "a"},
		{"name": "r", "remote": "o/r2", "default_branch": "main", "connector": "a"}
	]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- AC-22: 接続ツールの id の重複 ---

func TestLoadConnectorsDeclaration_DuplicateConnectorID(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op1"}]},
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op2"}]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- AC-23: 同じ接続ツールの中の操作 id の重複（別の接続ツールなら許す） ---

func TestLoadConnectorsDeclaration_DuplicateOperationIDWithinSameConnector(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [
			{"id": "op"}, {"id": "op"}
		]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_SameOperationIDAcrossDifferentConnectorsIsAllowed(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]},
		{"id": "b", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

// --- AC-24: 宣言に無い接続ツールを指すリポジトリ ---

func TestLoadConnectorsDeclaration_RepoReferencesUnknownConnector(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "does-not-exist"}]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- AC-25: form: plugin で invocation の無い操作 ---

func TestLoadConnectorsDeclaration_PluginOperationWithoutInvocation(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "plugin", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_BriefOperationWithInvocationIsInvalid(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op", "invocation": "/x"}]}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- AC-26: form: cli で commands・contract_version の無い接続ツール ---

func TestLoadConnectorsDeclaration_CLIConnectorWithoutCommands(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 1}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_CLIConnectorWithoutContractVersion(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "commands": {"start": ["start"], "status": ["status"], "resume": ["resume"], "cancel": ["cancel"]}}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_CLIConnectorMissingOneCommand(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 1,
		 "commands": {"start": ["start"], "status": ["status"], "resume": ["resume"]}}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_CLIConnectorContractVersionMustBeOne(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 2,
		 "commands": {"start": ["start"], "status": ["status"], "resume": ["resume"], "cancel": ["cancel"]}}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_ValidCLIConnector(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 1,
		 "commands": {"start": ["start"], "status": ["status"], "resume": ["resume"], "cancel": ["cancel"]}}
	], "repos": []}`)
	decl, err := LoadConnectorsDeclaration(dir)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	c := decl.Connectors[0]
	if c.Commands == nil || len(c.Commands.Start) != 1 || c.Commands.Start[0] != "start" {
		t.Errorf("Commands = %+v", c.Commands)
	}
	if c.ContractVersion == nil || *c.ContractVersion != 1 {
		t.Errorf("ContractVersion = %v, want 1", c.ContractVersion)
	}
}

// --- 操作の省略時の既定（interactive=true, counterpart=parent, child_may_decide=false） ---

func TestLoadConnectorsDeclaration_OperationDefaults(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": []}`)
	decl, err := LoadConnectorsDeclaration(dir)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	op := decl.Connectors[0].Operations[0]
	if op.Interactive != true {
		t.Errorf("Interactive = %v, want true", op.Interactive)
	}
	if op.Counterpart != CounterpartParent {
		t.Errorf("Counterpart = %q, want parent", op.Counterpart)
	}
	if op.ChildMayDecide != false {
		t.Errorf("ChildMayDecide = %v, want false", op.ChildMayDecide)
	}
	if !decl.DefaultsUsed {
		t.Errorf("DefaultsUsed = false, want true")
	}
}

// --- version の省略・不正 ---

func TestLoadConnectorsDeclaration_VersionMustBeOne(t *testing.T) {
	cases := []string{
		`{"connectors": [], "repos": []}`,
		`{"version": 2, "connectors": [], "repos": []}`,
		`{"version": "1", "connectors": [], "repos": []}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeConnectorsJSON(t, doc)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- connectors・repos の省略は必須キー違反 ---

func TestLoadConnectorsDeclaration_ConnectorsAndReposAreRequired(t *testing.T) {
	cases := []string{
		`{"version": 1, "repos": []}`,
		`{"version": 1, "connectors": []}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeConnectorsJSON(t, doc)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadConnectorsDeclaration_EmptyConnectorsAndReposIsAllowed(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [], "repos": []}`)
	decl, err := LoadConnectorsDeclaration(dir)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(decl.Connectors) != 0 || len(decl.Repos) != 0 {
		t.Errorf("Connectors/Repos not empty: %+v / %+v", decl.Connectors, decl.Repos)
	}
}

// --- human_question_kinds の id 重複（判断が要る細部。fail-closed 側に倒す） ---

func TestLoadConnectorsDeclaration_DuplicateHumanQuestionKindID(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "human_question_kinds": [
		{"id": "a", "label": "x"}, {"id": "a", "label": "y"}
	], "connectors": [], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- 未知のキーへの型違い ---

func TestLoadConnectorsDeclaration_UnparseableJSON(t *testing.T) {
	dir := writeConnectorsJSON(t, `{ not json `)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// --- self-review 指摘: 明示的な JSON null は「省略」と区別し型違いとして
// 拒否する（agent_declaration_test.go の同名の指摘と同じ理由） ---

func TestLoadConnectorsDeclaration_WholeFileNull(t *testing.T) {
	dir := writeConnectorsJSON(t, `null`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_RequiredKeysRejectNull(t *testing.T) {
	cases := []string{
		`{"version": 1, "connectors": null, "repos": []}`,
		`{"version": 1, "connectors": [], "repos": null}`,
	}
	for _, doc := range cases {
		t.Run(doc, func(t *testing.T) {
			dir := writeConnectorsJSON(t, doc)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestLoadConnectorsDeclaration_OperationNullFieldsAreRejected(t *testing.T) {
	cases := []string{
		`{"id": null}`,
		`{"id": "op", "invocation": null}`,
		`{"id": "op", "interactive": null}`,
		`{"id": "op", "child_may_decide": null}`,
		`{"id": "op", "artifacts": null}`,
	}
	for _, opJSON := range cases {
		t.Run(opJSON, func(t *testing.T) {
			dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
				{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [`+opJSON+`]}
			], "repos": []}`)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- self-review 指摘: CLI commands の各配列は空を許さず、要素は invocation と
// 同じ差し込みの閉集合で検証する ---

func TestLoadConnectorsDeclaration_CLIConnectorEmptyCommandArrayIsInvalid(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 1,
		 "commands": {"start": [], "status": ["status"], "resume": ["resume"], "cancel": ["cancel"]}}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

// self-review round 2 指摘: commands の各引数に invocation と同じ差し込みの
// 閉集合（{issue_number} 等）を適用すると、start が返す run_id を
// status・resume・cancel に渡す宣言（例: "{run_id}"）を拒否してしまう。
// 仕様は commands の引数に対する差し込みの閉集合を invocation とは別に
// 定めていないため、S1 では commands の引数の内容を制限しない（構造的な
// 検証・非空だけを課す）仮定を置く。
func TestLoadConnectorsDeclaration_CLIConnectorCommandArgsAreNotRestrictedToInvocationPlaceholders(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "cli", "permission_mode": "auto", "contract_version": 1,
		 "commands": {"start": ["start"], "status": ["status", "{run_id}"], "resume": ["resume", "{run_id}"], "cancel": ["cancel", "{run_id}"]}}
	], "repos": []}`)
	if _, err := LoadConnectorsDeclaration(dir); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

// --- self-review 指摘: repos の remote・default_branch・slots.provider の検証 ---

func TestLoadConnectorsDeclaration_RepoRemoteMustMatchOwnerSlashName(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "not-a-remote", "default_branch": "main", "connector": "a"}]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_RepoDefaultBranchMustNotBeEmpty(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "o/r", "default_branch": "", "connector": "a"}]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_RepoSlotsProviderMustBeInClosedSet(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "slots": {"provider": "bogus", "paths": []}}]}`)
	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}
}

func TestLoadConnectorsDeclaration_RepoSlotsProviderCloneIsValid(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "slots": {"provider": "clone", "paths": ["x"]}}]}`)
	if _, err := LoadConnectorsDeclaration(dir); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

// self-review round 2 指摘: round 1 の null 対応（connectors・repos・
// operation の各フィールド）は網羅的ではなく、human_question_kinds・
// operations（配列全体）・commands（オブジェクト全体）・slots（オブジェクト
// 全体）・slots.provider が抜けていた。同じ理由で null を明示的に拒否する。
func TestLoadConnectorsDeclaration_MoreNullFieldsAreRejected(t *testing.T) {
	cases := map[string]string{
		"human_question_kinds": `{"version": 1, "human_question_kinds": null, "connectors": [], "repos": []}`,
		"operations": `{"version": 1, "connectors": [
			{"id": "a", "form": "brief", "permission_mode": "auto", "operations": null}
		], "repos": []}`,
		// self-review round 3 指摘: form: "cli" だと commands が無いことも
		// validateCLICommands の nil チェックで ErrConfigInvalid になるため、
		// null チェックを外しても本ケースは変わらず落ちる（修正の検証に
		// なっていない）。commands は form に関わらず parseConnectors が
		// 常に読むため、form: "brief"（commands 自体は無関係のはず）で
		// 与えると、null チェックの有無で結果が変わる（無ければ黙って
		// 受理されてしまう）。
		"commands": `{"version": 1, "connectors": [
			{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}], "commands": null}
		], "repos": []}`,
		"slots": `{"version": 1, "connectors": [
			{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
		], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "slots": null}]}`,
		"slots.provider": `{"version": 1, "connectors": [
			{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}
		], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "slots": {"provider": null, "paths": []}}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeConnectorsJSON(t, doc)
			if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// --- fail-closed: 検証の失敗はワークスペースに何も書かない ---

func TestLoadConnectorsDeclaration_InvalidDeclarationChangesNothingOnDisk(t *testing.T) {
	dir := writeConnectorsJSON(t, `{"version": 1, "connectors": [
		{"id": "a", "form": "not-a-form", "permission_mode": "auto", "operations": [{"id": "op"}]}
	], "repos": []}`)
	before := snapshotDir(t, dir)

	if _, err := LoadConnectorsDeclaration(dir); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("error = %v, want ErrConfigInvalid", err)
	}

	after := snapshotDir(t, dir)
	if before != after {
		t.Fatalf("workspace changed after a rejected declaration:\nbefore=%s\nafter=%s", before, after)
	}
}
