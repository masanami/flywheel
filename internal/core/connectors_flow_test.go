package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// flowConnectorJSON は form: flow の委譲先 1 つ分の JSON。extra は委譲先直下へ足すキー、opExtra は操作へ足すキー。
func flowConnectorJSON(id, permission, opExtra, extra string) string {
	return `{"id": "` + id + `", "form": "flow", "permission_mode": "` + permission + `"` + extra +
		`, "operations": [{"id": "op"` + opExtra + `}]}`
}

const flowOp = `, "flow": "ticket"`

// AC-60〜AC-68・AC-142〜AC-144（flow の形の宣言）。
func TestLoadConnectorsDeclaration_FlowForm(t *testing.T) {
	t.Parallel()
	flow := func(permission, opExtra, extra string) string {
		return declWith(flowConnectorJSON("f", permission, opExtra, extra), "")
	}
	for _, tc := range []struct {
		name    string
		json    string
		invalid bool
	}{
		{"AC-60 form: flow を読み込める", flow("auto", flowOp, ""), false},
		{"AC-60 permission_mode の default・acceptEdits", flow("default", flowOp, ""), false},
		{"AC-60 acceptEdits", flow("acceptEdits", flowOp, ""), false},
		{"AC-61 commands を書く", flow("auto", flowOp, `, "commands": {"start": ["s"], "status": ["s"], "resume": ["r"], "cancel": ["c"]}`), true},
		{"AC-62 contract_version を書く", flow("auto", flowOp, `, "contract_version": 1`), true},
		{"AC-63 操作に flow が無い", flow("auto", "", ""), true},
		{"AC-64 flow が reflect", flow("auto", `, "flow": "reflect"`, ""), true},
		{"AC-39 ticket と ticket-light の操作を並べられる", flow("auto", flowOp+`}, {"id": "op2", "flow": "ticket-light"`, ""), false},
		{"AC-40 flow が ticket-light だけでもよい", flow("auto", `, "flow": "ticket-light"`, ""), false},
		{"AC-64 flow が unknown", flow("auto", `, "flow": "unknown"`, ""), true},
		{"AC-65 cli の操作に flow を書く", declWith(`{"id": "c", "form": "cli", "permission_mode": "auto", "contract_version": 1,
			"commands": {"start": ["s"], "status": ["st"], "resume": ["r"], "cancel": ["x"]},
			"operations": [{"id": "op", "flow": "ticket"}]}`, ""), true},
		{"AC-65 plugin の操作に flow を書く", declWith(`{"id": "p", "form": "plugin", "permission_mode": "auto",
			"operations": [{"id": "op", "invocation": "/x", "flow": "ticket"}]}`, ""), true},
		{"AC-65 brief の操作に flow を書く", declWith(`{"id": "b", "form": "brief", "permission_mode": "auto",
			"operations": [{"id": "op", "flow": "ticket"}]}`, ""), true},
		{"AC-66 cli に challenge_id_prefix を書く", declWith(cliConnectorWithKey(`"challenge_id_prefix": "C"`), ""), true},
		{"AC-66 brief に challenge_id_prefix を書く", declWith(`{"id": "b", "form": "brief", "permission_mode": "auto",
			"challenge_id_prefix": "C", "operations": [{"id": "op"}]}`, ""), true},
		{"challenge_id_prefix は flow の形に書ける", flow("auto", flowOp, `, "challenge_id_prefix": "C"`), false},
		{"AC-67 permission_mode が bypassPermissions", flow("bypassPermissions", flowOp, ""), true},
		{"AC-68 required_facts を書ける", flow("auto", flowOp, `, "required_facts": {"self_review": ["complete"]}`), false},
		{"AC-116 flow の形に models を書く", flow("auto", flowOp, `, "models": {"ticket": {"V": "m"}}`), true},
		{"AC-116 flow の形に空の models を書く", flow("auto", flowOp, `, "models": {}`), true},
		{"AC-119 flow の形に efforts を書く", flow("auto", flowOp, `, "efforts": {"ticket": {"V": "low"}}`), true},
		{"AC-144 cli に models を書く", declWith(cliConnectorWithKey(`"models": {"ticket": {"V": "m"}}`), ""), true},
		{"AC-144 plugin に models を書く", declWith(`{"id": "p", "form": "plugin", "permission_mode": "auto",
			"models": {"ticket": {"V": "m"}}, "operations": [{"id": "op", "invocation": "/x"}]}`, ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, tc.json))
			if tc.invalid {
				if !errors.Is(err, ErrConfigInvalid) {
					t.Fatalf("error = %v, want ErrConfigInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if c := d.Connectors[0]; c.Commands != nil || c.ContractVersion != nil {
				t.Fatalf("flow connector Commands = %+v, ContractVersion = %v, want both nil", c.Commands, c.ContractVersion)
			}
		})
	}
}

func TestLoadConnectorsDeclaration_FlowFormFields(t *testing.T) {
	t.Parallel()
	d, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, declWith(flowConnectorJSON("f", "auto", flowOp,
		`, "challenge_id_prefix": "FLW", "required_facts": {"a": ["b"]}`), "")))
	if err != nil {
		t.Fatal(err)
	}
	c := d.Connectors[0]
	if c.Form != ConnectorFormFlow || c.Operations[0].Flow != "ticket" || c.ChallengeIDPrefix != "FLW" ||
		c.RequiredFacts["a"][0] != "b" {
		t.Fatalf("connector = %+v", c)
	}
}

// AC-69: reflect_connector が flow の形を指せる。flow の形の委譲先は、宣言に書かなくても反映の依頼の口を持つ。
func TestLoadConnectorsDeclaration_FlowReflectConnector(t *testing.T) {
	t.Parallel()
	d, r := loadFlowDecl(t, flowConnectorJSON("f", "auto", flowOp, "")+`, `+noReflectCLI("own"), repoJSON("own", `, "reflect_connector": "f"`))
	if c := d.ReflectConnectorFor(r); c == nil || c.ID != "f" {
		t.Fatalf("ReflectConnectorFor = %+v, want f", c)
	}
	d, r = loadFlowDecl(t, flowConnectorJSON("f", "auto", flowOp, ""), repoJSON("f", ""))
	if c := d.ReflectConnectorFor(r); c == nil || c.ID != "f" {
		t.Fatalf("ReflectConnectorFor (connector 自身) = %+v, want f", c)
	}
}

func TestUsesContractCommands(t *testing.T) {
	t.Parallel()
	for form, want := range map[ConnectorForm]bool{
		ConnectorFormCLI: true, ConnectorFormFlow: true, ConnectorFormPlugin: false, ConnectorFormBrief: false,
	} {
		if got := (&Connector{Form: form}).UsesContractCommands(); got != want {
			t.Errorf("form %s: got %v, want %v", form, got, want)
		}
	}
	if (*Connector)(nil).UsesContractCommands() {
		t.Error("nil connector must be false")
	}
}

// AC-145: 計画を作る判断点の出力のタスクの欄に、モデルの欄を足していない（欄の一覧が変更の前と同じ）。
func TestJ2TaskFieldsHaveNoModel(t *testing.T) {
	t.Parallel()
	var tags []string
	rt := reflect.TypeOf(j2RawTask{})
	for i := 0; i < rt.NumField(); i++ {
		tags = append(tags, rt.Field(i).Tag.Get("json"))
	}
	wantTags := []string{"key", "title", "body", "done_criteria", "non_goals", "depends_on", "repo", "operation",
		"budget_impl_usd", "budget_review_usd", "source", "expand_after"}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Fatalf("j2RawTask json tags = %v, want %v", tags, wantTags)
	}
	var names []string
	vt := reflect.TypeOf(j2Task{})
	for i := 0; i < vt.NumField(); i++ {
		names = append(names, vt.Field(i).Name)
	}
	wantNames := []string{"Key", "Title", "Body", "DoneCriteria", "NonGoals", "DependsOn", "Repo", "Operation", "Source",
		"BudgetImplUSD", "BudgetReviewUSD", "ExpandAfter"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("j2Task fields = %v, want %v", names, wantNames)
	}
	for _, n := range append(tags, names...) {
		if strings.Contains(strings.ToLower(n), "model") {
			t.Fatalf("field %q mentions model", n)
		}
	}
}

func loadFlowDecl(t *testing.T, connectors, repo string) (*ConnectorsDeclaration, ConnectorRepo) {
	t.Helper()
	d, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, declWith(connectors, repo)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return d, d.Repos[0]
}

// flow の形の委譲先は、commands が無くても契約のコマンドを使う委譲先として引ける（コマンドは起動の時に組み立てる）。
func TestFindCLIConnector_FlowIsLaunchable(t *testing.T) {
	t.Parallel()
	d, _ := loadFlowDecl(t, flowConnectorJSON("f", "auto", flowOp, ""), repoJSON("f", ""))
	c, err := findCLIConnector(d, "f")
	if err != nil || c == nil || c.Commands != nil {
		t.Fatalf("connector = %+v error = %v, want the flow connector with empty commands", c, err)
	}
}
