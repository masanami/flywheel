package core

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
)

func j2DepOut(deps any, mut func(m map[string]any)) []byte {
	return j2OutputFor(false, func(m map[string]any) {
		if deps != nil {
			m["depends_on"] = deps
		}
		if mut != nil {
			mut(m)
		}
	})
}

func j2DepIDs(t *testing.T, ids ...string) []int64 {
	t.Helper()
	var out []int64
	for _, id := range ids {
		n, ok := parseChallengeID(id)
		if !ok {
			t.Fatalf("bad id %q", id)
		}
		out = append(out, n)
	}
	return out
}

func TestJ2OutputSchema_DependsOn(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatal(err)
	}
	p, ok := decoded["properties"].(map[string]any)["depends_on"].(map[string]any)
	if !ok || p["type"] != "array" {
		t.Fatalf("depends_on schema = %v", p)
	}
	for _, r := range decoded["required"].([]any) {
		if r == "depends_on" {
			t.Errorf("depends_on must be optional")
		}
	}
}

func TestValidateJ2Output_DependsOnSchema(t *testing.T) {
	vc := j2TestContext(t, true)
	if _, ok := validateJ2Output(j2DepOut("C-1", nil), vc); ok {
		t.Errorf("string depends_on must be invalid")
	}
	if _, ok := validateJ2Output(j2DepOut([]any{1}, nil), vc); ok {
		t.Errorf("non-string element must be invalid")
	}
	v, ok := validateJ2Output(j2DepOut([]string{"C-1", "C-2"}, nil), vc)
	if !ok || !reflect.DeepEqual(v.DependsOn, []string{"C-1", "C-2"}) {
		t.Errorf("valid depends_on: ok=%v v=%+v", ok, v)
	}
	// plan 以外の判定でも型は検証する。
	if _, ok := validateJ2Output([]byte(`{"verdict":"uncertain","question":"q","depends_on":"x"}`), vc); ok {
		t.Errorf("uncertain with bad depends_on must be invalid")
	}
	if _, ok := validateJ2Output([]byte(`{"verdict":"uncertain","question":"q","depends_on":["C-1"]}`), vc); !ok {
		t.Errorf("uncertain with depends_on must be valid")
	}
	issues := `{"verdict":"issues","issues":[` + `{"question":"q","options":[{"label":"a"},{"label":"b"}],"recommended":1},`
	issues += `{"question":"q","options":[{"label":"a"},{"label":"b"}],"recommended":1},{"question":"q","options":[{"label":"a"},{"label":"b"}],"recommended":1}]`
	if _, ok := validateJ2Output([]byte(issues+`,"depends_on":5}`), vc); ok {
		t.Errorf("issues with bad depends_on must be invalid")
	}
	if _, ok := validateJ2Output([]byte(issues+`,"depends_on":["C-1"]}`), vc); !ok {
		t.Errorf("issues with depends_on must be valid")
	}
}

// AC-187: 存在する ID なら計画の登録と同じトランザクションで依存を足す。
func TestPlanAutoJ2_DependsOn_AddsDependenciesWithPlan(t *testing.T) {
	f := newJ2Fixture(t)
	a := f.newClassified(t, "a", "P1")
	b := f.newClassified(t, "b", "P1")
	ch := f.newClassified(t, "c", "P1")
	before := f.detail(t, ch.ID)
	res, err := f.run(t, succeededInvoker(j2DepOut([]string{a.ID, b.ID, a.ID}, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("result = %v", res.Items[0].Result)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 {
		t.Fatalf("plan not registered: %+v", d.Challenge)
	}
	if d.Version != before.Version+1 {
		t.Errorf("version = %d, want %d (bumped once)", d.Version, before.Version+1)
	}
	cid := j2DepIDs(t, ch.ID)[0]
	if got, want := depEdges(t, f.s)[cid], j2DepIDs(t, a.ID, b.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
	var depAdds int
	for _, act := range depActivities(t, f.s, ch.ID) {
		if act.Action == ActivityActionDepAdd {
			depAdds++
			if act.Entity != "challenge" {
				t.Errorf("entity = %s", act.Entity)
			}
			var after map[string]any
			if err := json.Unmarshal(act.After, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after["depends_on"], []any{a.ID, b.ID}) {
				t.Errorf("after = %s", act.After)
			}
			if after["version"] != float64(d.Version) {
				t.Errorf("dep_add version = %v, want %d", after["version"], d.Version)
			}
		}
	}
	if depAdds != 1 {
		t.Errorf("dep_add entries = %d, want 1", depAdds)
	}
}

// AC-188〜AC-191: 存在しない ID・自己参照・循環（完了済みを通る輪を含む）は計画も依存も書かない。
func TestPlanAutoJ2_DependsOn_InvalidWritesNothingAndIsInvalidOutput(t *testing.T) {
	cases := []struct {
		name string
		deps func(ch, other *Challenge) []string
		pre  func(t *testing.T, f *j2Fixture, ch, other *Challenge)
	}{
		{"nonexistent", func(_, other *Challenge) []string { return []string{other.ID, "C-9999"} }, nil},
		{"malformed", func(_, _ *Challenge) []string { return []string{"zzz"} }, nil},
		{"self", func(ch, other *Challenge) []string { return []string{other.ID, ch.ID} }, nil},
		{"cycle", func(_, other *Challenge) []string { return []string{other.ID} },
			func(t *testing.T, f *j2Fixture, ch, other *Challenge) {
				insertDepEdge(t, f.s, j2DepIDs(t, other.ID)[0], j2DepIDs(t, ch.ID)[0])
			}},
	}
	for _, c := range cases {
		f := newJ2Fixture(t)
		other := f.newClassified(t, "other", "P1")
		ch := f.newClassified(t, "target", "P1")
		if c.pre != nil {
			c.pre(t, f, ch, other)
		}
		edgesBefore := depEdges(t, f.s)
		before := f.detail(t, ch.ID)
		actsBefore := len(depActivities(t, f.s, ch.ID))
		res, err := f.run(t, succeededInvoker(j2DepOut(c.deps(ch, other), nil)), idPtr(ch.ID))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.Items[0].Result != RunResultInvalidOutput {
			t.Errorf("%s: result = %v, want invalid_output", c.name, res.Items[0].Result)
		}
		after := f.detail(t, ch.ID)
		if after.Status != StatusClassified || after.Version != before.Version || len(after.Plans) != 0 {
			t.Errorf("%s: challenge changed: %+v", c.name, after.Challenge)
		}
		if !reflect.DeepEqual(depEdges(t, f.s), edgesBefore) {
			t.Errorf("%s: edges changed", c.name)
		}
		if n := len(depActivities(t, f.s, ch.ID)); n != actsBefore {
			t.Errorf("%s: activities %d -> %d", c.name, actsBefore, n)
		}
	}
}

// 循環の検査は課題の状態によらず、すべての辺（完了した課題を通る輪も）で行う。
func TestPlanAutoJ2_DependsOn_CycleThroughDoneChallengeIsRejected(t *testing.T) {
	f := newJ2Fixture(t)
	mid := f.newClassified(t, "mid", "P1")
	ch := f.newClassified(t, "target", "P1")
	insertDepEdge(t, f.s, j2DepIDs(t, mid.ID)[0], j2DepIDs(t, ch.ID)[0])
	if err := f.s.db.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'done' WHERE id = ?`, j2DepIDs(t, mid.ID)[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := f.detail(t, ch.ID)
	edgesBefore := depEdges(t, f.s)
	res, err := f.run(t, succeededInvoker(j2DepOut([]string{mid.ID}, nil)), idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Errorf("result = %v", res.Items[0].Result)
	}
	after := f.detail(t, ch.ID)
	if after.Status != StatusClassified || after.Version != before.Version || len(after.Plans) != 0 || !reflect.DeepEqual(depEdges(t, f.s), edgesBefore) {
		t.Errorf("challenge or edges changed: %+v", after.Challenge)
	}
}

// AC-192: depends_on が無い出力は既存の依存を変えない（空配列も同じ）。
func TestPlanAutoJ2_DependsOn_AbsentKeepsExistingDependencies(t *testing.T) {
	for _, deps := range []any{nil, []string{}, "null"} {
		f := newJ2Fixture(t)
		a := f.newClassified(t, "a", "P1")
		ch := f.newClassified(t, "c", "P1")
		insertDepEdge(t, f.s, j2DepIDs(t, ch.ID)[0], j2DepIDs(t, a.ID)[0])
		before := f.detail(t, ch.ID)
		out := j2DepOut(deps, nil)
		if deps == "null" {
			out = j2DepOut(nil, func(m map[string]any) { m["depends_on"] = nil })
		}
		if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, ch.ID)
		if d.Status != StatusAwaitingPlanApproval || d.Version != before.Version+1 {
			t.Errorf("deps=%v: %+v", deps, d.Challenge)
		}
		if got, want := depEdges(t, f.s)[j2DepIDs(t, ch.ID)[0]], j2DepIDs(t, a.ID); !reflect.DeepEqual(got, want) {
			t.Errorf("deps=%v: edges = %v, want %v", deps, got, want)
		}
		for _, act := range depActivities(t, f.s, ch.ID) {
			if act.Action == ActivityActionDepAdd {
				t.Errorf("unexpected dep_add")
			}
		}
	}
}

// 既にある依存だけを書いた出力は、足すものが無いので dep_add を残さない。
func TestPlanAutoJ2_DependsOn_AlreadyPresentLogsNothing(t *testing.T) {
	f := newJ2Fixture(t)
	a := f.newClassified(t, "a", "P1")
	ch := f.newClassified(t, "c", "P1")
	insertDepEdge(t, f.s, j2DepIDs(t, ch.ID)[0], j2DepIDs(t, a.ID)[0])
	if _, err := f.run(t, succeededInvoker(j2DepOut([]string{a.ID}, nil)), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	for _, act := range depActivities(t, f.s, ch.ID) {
		if act.Action == ActivityActionDepAdd {
			t.Errorf("unexpected dep_add")
		}
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s", d.Status)
	}
}

// 計画を登録しない判定（予想が空での保留・uncertain）では依存を書かない（無効な ID でも invalid_output にしない）。
func TestPlanAutoJ2_DependsOn_NotWrittenWhenPlanIsNotRegistered(t *testing.T) {
	f := newJ2Fixture(t)
	a := f.newClassified(t, "a", "P1")
	ch := f.newClassified(t, "held", "P1")
	f.recordIssues(t, ch.ID)
	out := j2DepOut([]string{a.ID, "C-9999"}, func(m map[string]any) { m["size"] = "M"; delete(m, "expected") })
	res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("result = %v", res.Items[0].Result)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingHuman || len(d.Plans) != 0 {
		t.Fatalf("not held: %+v", d.Challenge)
	}
	if len(depEdges(t, f.s)) != 0 {
		t.Errorf("edges written for held plan")
	}

	g := newJ2Fixture(t)
	c2 := g.newClassified(t, "unc", "P1")
	unc := []byte(`{"verdict":"uncertain","question":"q?","depends_on":["C-9999"]}`)
	res, err = g.run(t, succeededInvoker(unc), idPtr(c2.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultSucceeded {
		t.Errorf("uncertain result = %v", res.Items[0].Result)
	}
	if len(depEdges(t, g.s)) != 0 {
		t.Errorf("edges written for uncertain")
	}
}
