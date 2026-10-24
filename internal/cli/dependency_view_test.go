package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S2（#327）の show・status の依存のキーを検証する（AC-183・AC-186・AC-193・AC-194・AC-195）。

func anySlice(t *testing.T, v any, what string) []any {
	t.Helper()
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want an array", what, v)
	}
	return a
}

func TestShow_DependenciesAndDependents(t *testing.T) { // AC-193・AC-194
	ws := initializedWorkspace(t)
	a, b, c := createForCase(t, ws), createForCase(t, ws), createForCase(t, ws)
	runJSON(t, ws, "dep", "add", a, b, c)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, c), "done")
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, b), "s", "o/r#9", "https://example.invalid/9", "1:a", "closed", "in_policy", "2026-10-24T00:00:00.000Z")

	ch := runJSON(t, ws, "show", a)["challenge"].(map[string]any)
	deps := anySlice(t, ch["dependencies"], "dependencies")
	if len(deps) != 2 {
		t.Fatalf("dependencies = %#v", deps)
	}
	first := deps[0].(map[string]any)
	if first["challenge_id"] != b || first["satisfied"] != false || first["upstream_state"] != "closed" || first["policy_state"] != "in_policy" || first["status"] != "unclassified" {
		t.Errorf("dependencies[0] = %#v", first)
	}
	second := deps[1].(map[string]any)
	if second["challenge_id"] != c || second["satisfied"] != true || second["upstream_state"] != nil || second["policy_state"] != nil || second["status"] != "done" {
		t.Errorf("dependencies[1] = %#v (upstream_state/policy_state must be null without a binding)", second)
	}
	if got := anySlice(t, ch["dependents"], "dependents"); len(got) != 0 {
		t.Errorf("dependents of %s = %v, want []", a, got)
	}
	bch := runJSON(t, ws, "show", b)["challenge"].(map[string]any)
	if got := anySlice(t, bch["dependents"], "dependents"); !reflect.DeepEqual(got, []any{a}) {
		t.Errorf("dependents of %s = %v, want [%s]", b, got, a)
	}
	if got := anySlice(t, bch["dependencies"], "dependencies"); len(got) != 0 {
		t.Errorf("dependencies of %s = %v, want []", b, got)
	}
}

func TestShow_CycleDependencyIsNotSatisfied(t *testing.T) { // 親の決定: 循環は満たされない
	ws := initializedWorkspace(t)
	a, b := createForCase(t, ws), createForCase(t, ws)
	coretest.InsertDependency(t, ws, challengeIDToInternalID(t, a), challengeIDToInternalID(t, b))
	coretest.InsertDependency(t, ws, challengeIDToInternalID(t, b), challengeIDToInternalID(t, a))
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, b), "done")
	deps := anySlice(t, runJSON(t, ws, "show", a)["challenge"].(map[string]any)["dependencies"], "dependencies")
	if len(deps) != 1 || deps[0].(map[string]any)["satisfied"] != false {
		t.Errorf("dependencies = %#v, want one with satisfied=false", deps)
	}
}

func TestStatus_UnmetDependencies(t *testing.T) { // AC-183・AC-195
	ws := initializedWorkspace(t)
	a, b, c := createForCase(t, ws), createForCase(t, ws), createForCase(t, ws)
	runJSON(t, ws, "dep", "add", a, b, c)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, c), "done")
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, b), "s", "o/r#9", "https://example.invalid/9", "1:a", "missing", "out_of_policy", "2026-10-24T00:00:00.000Z")

	doc := runJSON(t, ws, "status")
	byID := map[string]map[string]any{}
	for _, v := range anySlice(t, doc["actionable"].(map[string]any)["challenges"], "actionable.challenges") {
		m := v.(map[string]any)
		byID[m["id"].(string)] = m
	}
	unmet := anySlice(t, byID[a]["unmet_dependencies"], "unmet_dependencies")
	if len(unmet) != 1 {
		t.Fatalf("unmet_dependencies of %s = %#v, want only %s (%s is satisfied)", a, unmet, b, c)
	}
	u := unmet[0].(map[string]any)
	if u["challenge_id"] != b || u["upstream_state"] != "missing" || u["policy_state"] != "out_of_policy" || u["status"] != "unclassified" {
		t.Errorf("unmet[0] = %#v", u)
	}
	// 満たされている・依存の無い課題は空の配列（null でも省略でもない）。
	for _, id := range []string{b} {
		v, ok := byID[id]["unmet_dependencies"]
		if !ok || v == nil || len(v.([]any)) != 0 {
			t.Errorf("unmet_dependencies of %s = %#v (present=%v), want []", id, v, ok)
		}
	}
	runJSON(t, ws, "dep", "rm", a, b)
	doc = runJSON(t, ws, "status")
	for _, v := range anySlice(t, doc["actionable"].(map[string]any)["challenges"], "actionable.challenges") {
		m := v.(map[string]any)
		if m["id"] == a && len(m["unmet_dependencies"].([]any)) != 0 {
			t.Errorf("after dep rm, unmet_dependencies = %#v, want []", m["unmet_dependencies"])
		}
	}
}

func TestStatus_DependencyCycleDiscrepancyOnePerRingMember(t *testing.T) { // AC-186
	ws := initializedWorkspace(t)
	a, b, c, d := createForCase(t, ws), createForCase(t, ws), createForCase(t, ws), createForCase(t, ws)
	id := func(s string) int { return challengeIDToInternalID(t, s) }
	coretest.InsertDependency(t, ws, id(a), id(b))
	coretest.InsertDependency(t, ws, id(b), id(c))
	coretest.InsertDependency(t, ws, id(c), id(a))
	coretest.InsertDependency(t, ws, id(d), id(a))
	entries := discrepancyEntries(t, runJSON(t, ws, "status"))
	if len(entries) != 3 {
		t.Fatalf("discrepancies = %#v, want 3 (a ring of three; %s only depends on the ring)", entries, d)
	}
	for i, want := range []string{a, b, c} {
		e := entries[i]
		if e["challenge_id"] != want || !reflect.DeepEqual(kindsOf(t, e), []string{"dependency_cycle"}) || !reflect.DeepEqual(e["challenge_ids"], []any{a, b, c}) {
			t.Errorf("entry[%d] = %#v", i, e)
		}
		if _, has := e["operation_id"]; has {
			t.Errorf("entry[%d] has operation_id: %#v", i, e)
		}
	}
	if findDiscrepancyEntry(entries, d) != nil {
		t.Errorf("%s is not in the ring but has an entry", d)
	}
}

func TestText_ShowAndStatusDependencyLines(t *testing.T) {
	ws := initializedWorkspace(t)
	a, b := createForCase(t, ws), createForCase(t, ws)
	runJSON(t, ws, "dep", "add", a, b)
	if out := runText(t, ws, "show", a); !strings.Contains(out, "依存先:        "+b+"(満たされていない)\n") || !strings.Contains(out, "依存元:        -\n") {
		t.Errorf("show %s text = %q", a, out)
	}
	if out := runText(t, ws, "show", b); !strings.Contains(out, "依存先:        -\n") || !strings.Contains(out, "依存元:        "+a+"\n") {
		t.Errorf("show %s text = %q", b, out)
	}
	out := runText(t, ws, "status")
	if !strings.Contains(out, "満たされていない依存先: "+b+"\n") {
		t.Errorf("status text lacks the unmet dependency of %s: %q", a, out)
	}
	if strings.Count(out, "満たされていない依存先") != 1 {
		t.Errorf("only %s has an unmet dependency: %q", a, out)
	}
}

// 依存・循環のある実出力の要素（dependencies・unmet_dependencies の要素）も、仕様の表から読んだ形と照合する。
func TestDocumentedShape_DependencyElementsAreCheckedAgainstSpec(t *testing.T) {
	doc := loadDocumentedJSON(t)
	ws := initializedWorkspace(t)
	a, b, c := createForCase(t, ws), createForCase(t, ws), createForCase(t, ws)
	runJSON(t, ws, "dep", "add", a, b)
	coretest.InsertDependency(t, ws, challengeIDToInternalID(t, b), challengeIDToInternalID(t, c))
	coretest.InsertDependency(t, ws, challengeIDToInternalID(t, c), challengeIDToInternalID(t, b))
	show := runJSON(t, ws, "show", a)
	if len(anySlice(t, show["challenge"].(map[string]any)["dependencies"], "dependencies")) != 1 {
		t.Fatal("fixture must have a dependency, or the element check runs zero times")
	}
	assertDocumentedJSON(t, doc, "show", show)
	status := runJSON(t, ws, "status")
	assertDocumentedJSON(t, doc, "status", status)
	n := 0
	for _, v := range anySlice(t, status["actionable"].(map[string]any)["challenges"], "challenges") {
		n += len(anySlice(t, v.(map[string]any)["unmet_dependencies"], "unmet_dependencies"))
	}
	if n == 0 || len(discrepancyEntries(t, status)) == 0 {
		t.Fatalf("fixture must have unmet dependencies and a cycle (unmet=%d)", n)
	}
}
