package core

import (
	"errors"
	"fmt"
	"testing"
)

// cliConnectorJSON は form: cli の委譲先 1 つ分の JSON。commands の各引数と、末尾に足すキーを差し替えられる。
func cliConnectorJSON(id, start, status, resume, cancel, extra string) string {
	return fmt.Sprintf(`{"id": %q, "form": "cli", "permission_mode": "auto", "contract_version": 1,
		"commands": {"start": %s, "status": %s, "resume": %s, "cancel": %s%s}}`, id, start, status, resume, cancel, extra)
}

// cliConnectorWithKey は form: cli の委譲先に、委譲先直下のキー（required_facts・slot_owner 等）を足した JSON。
func cliConnectorWithKey(key string) string {
	base := cliConnectorJSON("c", argsStart, argsStatus, argsResume, argsCancel, "")
	return base[:len(base)-1] + ", " + key + "}"
}

func declWith(connectors, repos string) string {
	return `{"version": 1, "connectors": [` + connectors + `], "repos": [` + repos + `]}`
}

func baseCLI(start, status, resume, cancel string) string {
	return declWith(cliConnectorJSON("c", start, status, resume, cancel, ""), "")
}

const (
	argsStart  = `["s"]`
	argsStatus = `["st"]`
	argsResume = `["r"]`
	argsCancel = `["x"]`
)

// AC-1〜AC-11（コマンドごとの差し込みの表・commands.reflect の任意）。1 基準 = 1 ケース。
func TestLoadConnectorsDeclaration_CommandPlaceholderTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		json    string
		invalid bool
	}{
		{"AC-1 start の {task_file}", baseCLI(`["s","{task_file}"]`, argsStatus, argsResume, argsCancel), false},
		{"AC-2 start の {task_id}", baseCLI(`["s","{task_id}"]`, argsStatus, argsResume, argsCancel), false},
		{"AC-3 start の {uses_integration}", baseCLI(`["s","{uses_integration}"]`, argsStatus, argsResume, argsCancel), false},
		{"AC-4 resume の {approved_ref}", baseCLI(argsStart, argsStatus, `["r","{approved_ref}"]`, argsCancel), false},
		{"AC-5 start の {approved_ref}", baseCLI(`["s","{approved_ref}"]`, argsStatus, argsResume, argsCancel), true},
		{"AC-6 status の {task_file}", baseCLI(argsStart, `["st","{task_file}"]`, argsResume, argsCancel), true},
		{"AC-7 start の {unknown}", baseCLI(`["s","{unknown}"]`, argsStatus, argsResume, argsCancel), true},
		{"AC-7 cancel の {unknown}", baseCLI(argsStart, argsStatus, argsResume, `["x","{unknown}"]`), true},
		{"AC-8 reflect の {artifact_ref}・{challenge_id}", declWith(cliConnectorJSON("c", argsStart, argsStatus, argsResume, argsCancel,
			`, "reflect": ["f","{artifact_ref}","{challenge_id}"]`), ""), false},
		{"AC-9 reflect の {issue_number}", declWith(cliConnectorJSON("c", argsStart, argsStatus, argsResume, argsCancel,
			`, "reflect": ["f","{issue_number}"]`), ""), true},
		{"AC-10 reflect が空の配列", declWith(cliConnectorJSON("c", argsStart, argsStatus, argsResume, argsCancel,
			`, "reflect": []`), ""), true},
		{"AC-8 reflect に null の要素", declWith(cliConnectorJSON("c", argsStart, argsStatus, argsResume, argsCancel,
			`, "reflect": [null]`), ""), true},
		{"AC-11 reflect が無い", baseCLI(argsStart, argsStatus, argsResume, argsCancel), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, tc.json))
			if tc.invalid {
				if !errors.Is(err, ErrConfigInvalid) {
					t.Fatalf("error = %v, want ErrConfigInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func reflectCLI(id string) string {
	return cliConnectorJSON(id, argsStart, argsStatus, argsResume, argsCancel, `, "reflect": ["f"]`)
}

func noReflectCLI(id string) string {
	return cliConnectorJSON(id, argsStart, argsStatus, argsResume, argsCancel, "")
}

const briefConnector = `{"id": "b", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}`

func repoJSON(connector, extra string) string {
	return fmt.Sprintf(`{"name": "r", "remote": "o/r", "default_branch": "main", "connector": %q%s}`, connector, extra)
}

// AC-12〜AC-15（repos[].reflect_connector の検査）。
func TestLoadConnectorsDeclaration_ReflectConnectorValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		json    string
		invalid bool
	}{
		{"AC-12 commands.reflect を持つ form: cli", declWith(reflectCLI("rc")+","+briefConnector, repoJSON("b", `, "reflect_connector": "rc"`)), false},
		{"AC-13 commands.reflect を持たない委譲先", declWith(noReflectCLI("rc")+","+briefConnector, repoJSON("b", `, "reflect_connector": "rc"`)), true},
		{"AC-14 form: cli でない委譲先", declWith(briefConnector, repoJSON("b", `, "reflect_connector": "b"`)), true},
		{"AC-15 宣言に無い ID", declWith(briefConnector, repoJSON("b", `, "reflect_connector": "nope"`)), true},
		{"口の無いリポジトリは止めない", declWith(briefConnector, repoJSON("b", "")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, tc.json))
			if tc.invalid != errors.Is(err, ErrConfigInvalid) || (!tc.invalid && err != nil) {
				t.Fatalf("error = %v, invalid want %v", err, tc.invalid)
			}
		})
	}
}

// AC-16〜AC-18（反映の依頼の口を決める順）。
func TestReflectConnectorFor(t *testing.T) {
	t.Parallel()
	load := func(t *testing.T, connectors, repo string) (*ConnectorsDeclaration, ConnectorRepo) {
		t.Helper()
		d, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, declWith(connectors, repo)))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return d, d.Repos[0]
	}
	t.Run("AC-16 reflect_connector が優先（connector が別の reflect を持っていても）", func(t *testing.T) {
		t.Parallel()
		d, r := load(t, reflectCLI("own")+","+reflectCLI("other"), repoJSON("own", `, "reflect_connector": "other"`))
		if c := d.ReflectConnectorFor(r); c == nil || c.ID != "other" {
			t.Fatalf("got %+v, want other", c)
		}
	})
	t.Run("AC-17 reflect_connector が無く connector が reflect を持つ", func(t *testing.T) {
		t.Parallel()
		d, r := load(t, reflectCLI("own"), repoJSON("own", ""))
		if c := d.ReflectConnectorFor(r); c == nil || c.ID != "own" {
			t.Fatalf("got %+v, want own", c)
		}
	})
	t.Run("AC-18 reflect を持たなければ口は無い", func(t *testing.T) {
		t.Parallel()
		d, r := load(t, noReflectCLI("own"), repoJSON("own", ""))
		if c := d.ReflectConnectorFor(r); c != nil {
			t.Fatalf("got %+v, want nil", c)
		}
	})
}

// AC-19〜AC-23（required_facts）。
func TestLoadConnectorsDeclaration_RequiredFacts(t *testing.T) {
	t.Parallel()
	cli := func(rf string) string {
		return declWith(cliConnectorWithKey(`"required_facts": `+rf), "")
	}
	for _, tc := range []struct {
		name    string
		json    string
		invalid bool
	}{
		{"AC-19 form: cli に書ける", cli(`{"self_review": ["complete"]}`), false},
		{"AC-20 空の対応", cli(`{}`), true},
		{"AC-21 値が空の配列", cli(`{"self_review": []}`), true},
		{"AC-22 文字列でない要素", cli(`{"self_review": ["complete", 1]}`), true},
		{"AC-22 null の要素", cli(`{"self_review": ["complete", null]}`), true},
		{"AC-23 form: plugin に書く", declWith(`{"id": "p", "form": "plugin", "permission_mode": "auto", "required_facts": {"a": ["b"]},
			"operations": [{"id": "op", "invocation": "/x {issue_number}"}]}`, ""), true},
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
			if got := d.Connectors[0].RequiredFacts["self_review"]; len(got) != 1 || got[0] != "complete" {
				t.Fatalf("RequiredFacts = %v", d.Connectors[0].RequiredFacts)
			}
		})
	}
}

// AC-26（slot_owner は足さない。知らないキーとして止まる）。
func TestLoadConnectorsDeclaration_SlotOwnerIsUnknownKey(t *testing.T) {
	t.Parallel()
	for name, j := range map[string]string{
		"委譲先":     declWith(cliConnectorWithKey(`"slot_owner": "flywheel"`), ""),
		"repos[]": declWith(briefConnector, repoJSON("b", `, "slot_owner": "flywheel"`)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadConnectorsDeclaration(writeConnectorsJSON(t, j)); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}
