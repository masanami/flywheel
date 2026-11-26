package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、仕様の下書きをリポジトリへ入れるタスク（lands_spec）の、計画の入力の区画・出力の型・検査・
// 一覧の書き足し・タスクの記録・計画の本文（docs/features/request-to-plan.md の S2a）を確かめる。
// テストの名前の AC の番号は、受入基準の節の並び順。

const (
	landDraftPath = ".flywheel/specs/example.md"
	landBasePath  = "docs/features/example.md"
)

var landAllIDs = []string{covIDA, covID80, covID81}

// landTask はテストの計画のタスク 1 件。lands は nil なら項目を書かない（json の null は landNull）。
type landTask struct {
	lands any
	deps  []string
}

type landJSONNull struct{}

var landNull = landJSONNull{}

// landPlan は鍵 a・b・c の順のタスクを持つ計画の出力。先頭のタスクがすべての基準を受け持つ。
func landPlan(specs ...landTask) func(m map[string]any) {
	return func(m map[string]any) {
		covers := make([][]string, len(specs))
		covers[0] = landAllIDs
		covPlan(nil, covers...)(m)
		for i, spec := range specs {
			task := m["tasks"].([]any)[i].(map[string]any)
			switch v := spec.lands.(type) {
			case nil:
			case landJSONNull:
				task["lands_spec"] = nil
			default:
				task["lands_spec"] = v
			}
			if spec.deps != nil {
				task["depends_on"] = spec.deps
			}
		}
		if len(specs) == 1 {
			m["uses_integration"] = false
		}
	}
}

func landCriteria(draft bool) *planAcceptanceCriteria {
	c := covCriteria()
	if draft {
		c.Path, c.Draft = landDraftPath, true
	}
	return c
}

func TestValidateJ2Output_SpecLanding(t *testing.T) {
	t.Parallel()
	yes, dep := landTask{lands: true}, []string{"a"}
	doneTitle := "済みのタスク"
	for _, tc := range []struct {
		name     string
		criteria *planAcceptanceCriteria
		done     *string
		mut      func(map[string]any)
		want     string
	}{
		{"AC-35 入れるタスクが 1 件", landCriteria(true), nil, landPlan(yes), ""},
		{"AC-36 入れるタスクが無い", landCriteria(true), nil, landPlan(landTask{}), "spec landing missing"},
		{"AC-36 偽だけ", landCriteria(true), nil, landPlan(landTask{lands: false}), "spec landing missing"},
		{"AC-37 入れるタスクが 2 件", landCriteria(true), nil, landPlan(yes, landTask{lands: true, deps: dep}), "duplicate spec landing"},
		{"AC-38 2 件が入れるタスクを前提に持つ", landCriteria(true), nil, landPlan(yes, landTask{deps: dep}, landTask{deps: dep}), ""},
		{"AC-38 入れるタスクが後ろに書かれている", landCriteria(true), nil, landPlan(landTask{deps: []string{"b"}}, yes), ""},
		{"AC-39 別のタスクを介して届く", landCriteria(true), nil, landPlan(yes, landTask{deps: dep}, landTask{deps: []string{"b"}}), ""},
		{"AC-39 並びと逆の向きに 2 段たどって届く", landCriteria(true), nil, landPlan(landTask{deps: []string{"b"}}, landTask{deps: []string{"c"}}, yes), ""},
		{"AC-40 前提の無いタスクがある", landCriteria(true), nil, landPlan(yes, landTask{}), "spec landing not first"},
		{"AC-40 入れるタスクのほうが後", landCriteria(true), nil, landPlan(landTask{}, landTask{lands: true, deps: dep}), "spec landing not first"},
		{"AC-41 下書きでない仕様の文書", landCriteria(false), nil, landPlan(yes), "unexpected spec landing"},
		{"AC-42 仕様の文書の場所が無い", nil, nil, func(m map[string]any) {
			landPlan(yes)(m)
			m["tasks"].([]any)[0].(map[string]any)["covers"] = []string{}
		}, "unexpected spec landing"},
		{"AC-43 下書きでなく、どのタスクも書かない", landCriteria(false), nil, landPlan(landTask{}, landTask{}), ""},
		{"AC-44 null は偽（下書きでない）", landCriteria(false), nil, landPlan(landTask{lands: landNull}), ""},
		{"AC-44 null は偽（下書き）", landCriteria(true), nil, landPlan(landTask{lands: landNull}), "spec landing missing"},
		{"AC-44 null は偽（2 件目）", landCriteria(true), nil, landPlan(yes, landTask{lands: landNull, deps: dep}), ""},
		{"AC-45 済みが入れていれば順も問わない", landCriteria(true), &doneTitle, landPlan(landTask{}, landTask{}), ""},
		{"AC-47 済みが入れていて新しいタスクも入れる", landCriteria(true), &doneTitle, landPlan(yes), "duplicate spec landing"},
		{"AC-50 網羅の漏れは入れるタスクがあっても不正", landCriteria(true), nil, func(m map[string]any) {
			landPlan(yes)(m)
			m["tasks"].([]any)[0].(map[string]any)["covers"] = []string{covIDA}
		}, "criteria not covered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vc := j2TestContext(t, false)
			vc.CheckCriteria, vc.Criteria, vc.DoneSpecLanding = true, tc.criteria, tc.done
			_, reason := validateJ2OutputReason(j2PlanOutput(tc.mut), vc)
			switch {
			case tc.want == "" && reason != "":
				t.Fatalf("want valid, got %q", reason)
			case tc.want != "" && !strings.Contains(reason, tc.want):
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.want)
			}
		})
	}
}

func TestJ2OutputSchema_LandsSpec(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string][]byte{
		"plan":            j2OutputSchema(),
		"expand":          j2ExpandOutputSchema(),
		"expand followup": j2ExpandOutputSchemaFor(TaskAdditionOriginFollowup),
	} {
		lands, ok := j2SchemaTaskProps(t, schema)["lands_spec"].(map[string]any)
		if !ok {
			t.Fatalf("%s: tasks lack lands_spec", name)
		}
		if got := mustJSON(t, lands["type"]); got != `["boolean","null"]` {
			t.Errorf("%s: lands_spec type = %s", name, got)
		}
		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatal(err)
		}
		items := decoded["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
		if strings.Contains(mustJSON(t, items["required"]), "lands_spec") {
			t.Errorf("%s: lands_spec must not be required", name)
		}
	}
}

// draftChallenge は仕様の文書の場所 spec（空は場所なし）と置き換える元 base（空はなし）を持つ分類済の課題。文書は書かない。
func draftChallenge(t *testing.T, spec, base string) (*j2Fixture, string) {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "landing", "P1")
	if spec != "" {
		f.setSpec(t, ch.ID, spec)
	}
	if base != "" {
		setSpecBase(t, f.s, ch.ID, base)
	}
	return f, ch.ID
}

func setSpecBase(t *testing.T, s *Store, id, base string) {
	t.Helper()
	if _, err := s.EditChallenge(ctx0, ChannelCLI, id, EditInput{SpecBase: &base}); err != nil {
		t.Fatalf("EditChallenge(spec base): %v", err)
	}
}

// landRegister は文書を置いた課題に計画の呼び出しを 1 回行い、登録されたことを確かめる。
func landRegister(t *testing.T, spec, base string, out []byte) (*j2Fixture, string) {
	t.Helper()
	f, id := draftChallenge(t, spec, base)
	writeWorkspaceFile(t, f.s.workspace, spec, covDoc)
	res, err := f.run(t, succeededInvoker(out), idPtr(id))
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "plan" {
		runs, _ := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
		t.Fatalf("PlanAutoJ2: %+v %v (runs %+v)", res, err, runs)
	}
	return f, id
}

func TestPlanAutoJ2_DraftWithoutFileDoesNotInvokeJ2(t *testing.T) { // AC-12
	t.Parallel()
	f, id := draftChallenge(t, landDraftPath, "")
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(JudgmentLaunchInput) { t.Error("J2 must not be invoked") }
	res, err := f.run(t, inv, idPtr(id))
	if err != nil || len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSpecUnreadable {
		t.Fatalf("result = %+v, %v, want spec_unreadable", res, err)
	}
}

func TestPlanAutoJ2_SpecSectionForDraft(t *testing.T) {
	t.Parallel()
	section := func(t *testing.T, spec, base string) string {
		t.Helper()
		f, id := draftChallenge(t, spec, base)
		writeWorkspaceFile(t, f.s.workspace, spec, covDoc)
		content, ok := j2SectionText(planInput(t, f, id), AcceptanceCriteriaSectionLabel)
		if !ok {
			t.Fatal("no section 受入基準")
		}
		return content
	}
	t.Run("AC-31・AC-32 下書きで置き換える元を持つ", func(t *testing.T) {
		t.Parallel()
		got := section(t, landDraftPath, landBasePath)
		requireContains(t, got, "下書き（リポジトリにまだ入っていない）")
		requireContains(t, got, "\n置き換える元: "+landBasePath+"\n")
		requireAbsent(t, got, "新しい文書")
	})
	t.Run("AC-31・AC-33 下書きで置き換える元を持たない", func(t *testing.T) {
		t.Parallel()
		got := section(t, landDraftPath, "")
		requireContains(t, got, "下書き（リポジトリにまだ入っていない）")
		requireContains(t, got, "新しい文書")
		requireAbsent(t, got, "置き換える元")
	})
	t.Run("AC-34 下書きでなければ S1 の形のまま", func(t *testing.T) {
		t.Parallel()
		line := func(id, text string) string {
			excerpt, _ := truncateRunes(text, acceptanceExcerptRunes)
			return "\n- " + id + ": " + excerpt
		}
		want := "仕様の文書: " + criteriaDocPath + "\n件数: 3\n" + line(covIDA, covCritA) + line(covID80, covCrit80) + line(covID81, covCrit81)
		if got := section(t, criteriaDocPath, landBasePath); got != want {
			t.Errorf("section = %q, want %q", got, want)
		}
	})
}

func landListOf(t *testing.T, f *j2Fixture, id string, plan int) map[string]json.RawMessage {
	t.Helper()
	d := f.detail(t, id)
	latest := d.Plans[0]
	for _, p := range d.Plans {
		if p.Version > latest.Version {
			latest = p
		}
	}
	if len(d.Plans) != plan || latest.Spec == nil {
		t.Fatalf("plans = %+v, want %d", d.Plans, plan)
	}
	var spec struct {
		AcceptanceCriteria map[string]json.RawMessage `json:"acceptance_criteria"`
	}
	if err := json.Unmarshal([]byte(*latest.Spec), &spec); err != nil {
		t.Fatal(err)
	}
	return spec.AcceptanceCriteria
}

func TestPlanAutoJ2_AcceptanceCriteriaListHasDraftAndBasePath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, spec, base, draft, basePath string
		lands                             bool
	}{
		{"AC-57・AC-59 下書きで置き換える元を持つ", landDraftPath, landBasePath, "true", `"` + landBasePath + `"`, true},
		{"AC-57・AC-60 下書きで置き換える元を持たない", landDraftPath, "", "true", "null", true},
		{"AC-58・AC-61 下書きでなく置き換える元を持つ", criteriaDocPath, landBasePath, "false", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, id := landRegister(t, tc.spec, tc.base, j2OutputFor(false, landPlan(landTask{lands: tc.lands})))
			list := landListOf(t, f, id, 1)
			if got := string(list["draft"]); got != tc.draft {
				t.Errorf("draft = %s, want %s", got, tc.draft)
			}
			if got, has := list["base_path"]; !has || string(got) != tc.basePath {
				t.Errorf("base_path = %s (present %v), want %s", got, has, tc.basePath)
			}
		})
	}
}

func TestReplanJ2_EditedSpecBaseIsInTheNewList(t *testing.T) { // AC-25
	t.Parallel()
	out := j2OutputFor(false, landPlan(landTask{lands: true}))
	f, id := landRegister(t, landDraftPath, "docs/old.md", out)
	if err := approvePlanForTest(t, f.s, id, ttyAtt(t, id)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.HoldChallenge(ctx0, ChannelCLI, id, HoldInput{Question: "直す"}); err != nil {
		t.Fatal(err)
	}
	pre, err := f.s.PrepareAnswer(ctx0, id, "作り直す")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(ctx0, AnswerRequest{ChallengeID: id, ExpectedVersion: pre.Version, Answer: "作り直す", Replan: true}, relayedAttestation(t, id, "s", "q")); err != nil {
		t.Fatal(err)
	}
	setSpecBase(t, f.s, id, "docs/new.md")
	res, err := f.run(t, succeededInvoker(out), idPtr(id))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("replan: %+v %v", res, err)
	}
	if got := string(landListOf(t, f, id, 2)["base_path"]); got != `"docs/new.md"` {
		t.Errorf("base_path = %s", got)
	}
}

func TestPlanApproval_RecordsLandsSpecOnTheTasks(t *testing.T) { // AC-55・AC-56
	t.Parallel()
	f, id := landRegister(t, landDraftPath, "", j2OutputFor(false, landPlan(landTask{lands: true}, landTask{deps: []string{"a"}})))
	if err := approvePlanForTest(t, f.s, id, ttyAtt(t, id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	tasks := f.detail(t, id).Tasks
	if len(tasks) != 2 || !tasks[0].LandsSpec || tasks[1].LandsSpec {
		t.Fatalf("tasks = %+v, want only the first to land the spec", tasks)
	}
}

const landTaskLine = "   - 仕様の文書をリポジトリへ入れる"

// landSection は計画の本文の節「受入基準の受け持ち」（見出しから次の見出しの前まで）。
func landSection(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, "## "+AcceptanceCoverageHeading+"\n\n")
	if !ok {
		t.Fatalf("no coverage section:\n%s", body)
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

func TestPlanAutoJ2_PlanBodyShowsTheSpecLanding(t *testing.T) {
	t.Parallel()
	out := j2OutputFor(false, landPlan(landTask{lands: true}, landTask{deps: []string{"a"}}))
	t.Run("AC-62・AC-63 タスクの行", func(t *testing.T) {
		t.Parallel()
		f, id := landRegister(t, landDraftPath, "", out)
		body, _ := covLatestPlan(t, f, id)
		requireContains(t, body, "1. タスク a\n"+landTaskLine+"\n")
		if n := strings.Count(body, "仕様の文書をリポジトリへ入れる"); n != 1 {
			t.Errorf("the line appears %d times, want 1:\n%s", n, body)
		}
	})
	t.Run("AC-64・AC-65・AC-66 下書きで置き換える元を持つ", func(t *testing.T) {
		t.Parallel()
		f, id := landRegister(t, landDraftPath, landBasePath, out)
		body, _ := covLatestPlan(t, f, id)
		want := "- 仕様の文書: " + landDraftPath + "（下書き。リポジトリへ入れるのは「タスク a」）\n" +
			"- 置き換える元: " + landBasePath + "\n- 受入基準: 3 件\n- 受け持たない基準: なし\n"
		if got := landSection(t, body); got != want {
			t.Errorf("section = %q, want %q", got, want)
		}
	})
	t.Run("AC-67 下書きで置き換える元を持たない", func(t *testing.T) {
		t.Parallel()
		f, id := landRegister(t, landDraftPath, "", out)
		body, _ := covLatestPlan(t, f, id)
		requireAbsent(t, body, "置き換える元")
	})
	t.Run("AC-68 下書きでなければ S1 の形のまま", func(t *testing.T) {
		t.Parallel()
		f, id := landRegister(t, criteriaDocPath, landBasePath, j2OutputFor(false, landPlan(landTask{}, landTask{})))
		body, _ := covLatestPlan(t, f, id)
		want := "- 仕様の文書: " + criteriaDocPath + "\n- 受入基準: 3 件\n- 受け持たない基準: なし\n"
		if got := landSection(t, body); got != want {
			t.Errorf("section = %q, want %q", got, want)
		}
		requireAbsent(t, body, "仕様の文書をリポジトリへ入れる")
	})
}

const (
	landCritDone = "済みのタスクが受け持った基準"
	landCritNew  = "新しい計画の基準"
)

// landReplan は、済みのタスク a が基準を受け持って下書きを入れた記録を持つ、作り直し前の下書きの課題。
func landReplan(t *testing.T) *paFixture {
	t.Helper()
	f := newReplanJ2Fixture(t, nil)
	writeWorkspaceFile(t, f.s.workspace, landDraftPath, "## 受入基準\n- [ ] "+landCritDone+"\n- [ ] "+landCritNew+"\n")
	f.setSpec(t, f.id, landDraftPath)
	setTaskCovers(t, f.s, f.cid(), "a", mustJSON(t, []string{acceptanceCriterionID(landCritDone)}))
	coretest.Exec(t, f.s.Workspace(), `UPDATE task SET lands_spec = 1 WHERE challenge_id = ? AND key = 'a'`, f.cid())
	return f
}

// landReplanOut は作り直しの出力（タスク n 件。前提を持たない）。先頭のタスクが covers を受け持ち、lands なら下書きを入れる。
func landReplanOut(t *testing.T, n int, integration, lands bool, covers ...string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(paPlanOut(n, integration), &m); err != nil {
		t.Fatal(err)
	}
	for _, task := range m["tasks"].([]any) {
		task.(map[string]any)["depends_on"] = []string{}
	}
	first := m["tasks"].([]any)[0].(map[string]any)
	ids := make([]string, len(covers))
	for i, c := range covers {
		ids[i] = acceptanceCriterionID(c)
	}
	first["covers"], first["lands_spec"] = ids, lands
	return []byte(mustJSON(t, m))
}

func requireLandReplan(t *testing.T, f *paFixture, out []byte, wantReason string) {
	t.Helper()
	_, res := f.j2Input(t, out)
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v", res.Items)
	}
	if wantReason == "" {
		if res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "plan" {
			runs, _ := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &f.id})
			t.Fatalf("item = %+v, want a registered plan (runs %+v)", res.Items[0], runs)
		}
		return
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("item = %+v, want invalid_output", res.Items[0])
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND error LIKE ?`, f.cid(), "%"+wantReason+"%"); n != 1 {
		t.Errorf("runs whose reason has %q = %d, want 1", wantReason, n)
	}
}

func TestReplanJ2_SpecLanding(t *testing.T) {
	t.Parallel()
	t.Run("AC-45 統合先を使い、済みが入れていれば前提の無いタスクも通る", func(t *testing.T) {
		t.Parallel()
		f := landReplan(t)
		requireLandReplan(t, f, landReplanOut(t, 2, true, false, landCritNew), "")
		body := f.detail(t, f.id).Plans
		latest := body[0]
		for _, p := range body {
			if p.Version > latest.Version {
				latest = p
			}
		}
		requireContains(t, latest.Body, "（下書き。リポジトリへ入れるのは「TASK-a」）")
	})
	t.Run("AC-46 統合先を使わず、済みが入れていれば通る", func(t *testing.T) {
		t.Parallel()
		f := landReplan(t)
		coretest.Exec(t, f.s.Workspace(), `UPDATE challenge SET uses_integration = 0 WHERE id = ?`, f.cid())
		requireLandReplan(t, f, landReplanOut(t, 1, false, false, landCritDone, landCritNew), "")
	})
	t.Run("AC-47 済みが入れていて新しいタスクも入れる", func(t *testing.T) {
		t.Parallel()
		requireLandReplan(t, landReplan(t), landReplanOut(t, 1, true, true, landCritNew), "duplicate spec landing")
	})
	t.Run("AC-48 入れるタスクが取りやめ", func(t *testing.T) {
		t.Parallel()
		f := landReplan(t)
		setTaskStatusRaw(t, f.s, f.cid(), "a", TaskStatusCancelled)
		requireLandReplan(t, f, landReplanOut(t, 1, true, false, landCritDone, landCritNew), "spec landing missing")
	})
}

// landDraftList は計画の出力の一覧を下書きのものにする。
func landDraftList(list planAcceptanceCriteria) planAcceptanceCriteria {
	list.Path, list.Draft = landDraftPath, true
	return list
}

// 承認した計画の検査し直しの 3 つの入口（指示文だけの形の起動・委譲の対象を決める所・契約の形の起動）は、入れるタスクを検査しない。
func TestRecheckOfApprovedPlan_DoesNotCheckSpecLanding(t *testing.T) { // AC-51・AC-52・AC-53
	t.Parallel()
	for _, tc := range []struct {
		name  string
		draft bool
		lands bool
	}{
		{"下書きで入れるタスクが無い", true, false},
		{"下書きでないのに入れるタスクがある", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", handoffSpec("main-repo", "impl", func(m map[string]any) {
				if tc.draft {
					m["acceptance_criteria"] = landDraftList(m["acceptance_criteria"].(planAcceptanceCriteria))
				}
				if tc.lands {
					m["tasks"].([]any)[0].(map[string]any)["lands_spec"] = true
				}
			}))
			cid, _ := parseChallengeID(id)
			in := f.input(f.cycle(t, 300), &id)
			if _, err := f.s.loadDelegationContext(ctx0, in, f.detail(t, id).Challenge); err != nil {
				t.Errorf("AC-51 loadDelegationContext: %v", err)
			}
			if _, ok, reason, err := f.s.loadPlanCandidates(ctx0, in, cid); err != nil || !ok {
				t.Errorf("AC-52 loadPlanCandidates: ok=%v reason=%q err=%v", ok, reason, err)
			}
			if _, ok, err := f.s.contractRepoWorkDir(ctx0, f.agent, f.conn, cid, "", ""); err != nil || !ok {
				t.Errorf("AC-53 contractRepoWorkDir: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestHoldAnswerTaskAddition_RecheckDoesNotCheckSpecLanding(t *testing.T) { // AC-54
	t.Parallel()
	f := overLimitHeldWithCovers(t)
	list := landDraftList(planAcceptanceCriteria{Items: []acceptanceCriterion{{ID: handoffID1, Text: handoffCritFirst}}})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET spec = json_set(spec, '$.acceptance_criteria', json(?)) WHERE challenge_id = ?`, mustJSON(t, list), f.cid())
		return err
	})
	f.setSpec(t, f.id, landDraftPath)
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatal(err)
	}
	if task := f.taskByKey(t, "c"); task == nil || task.LandsSpec {
		t.Fatalf("task c = %+v", task)
	}
}

func TestExpand_LandsSpec(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lands any
		ok    bool
	}{
		{"AC-49 真は不正", true, false},
		{"偽は足せる", false, true},
		{"null は足せる", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := expandDoneFixture(t)
			out := expandAddOutput(func(ts []map[string]any) { ts[0]["lands_spec"] = tc.lands })
			res := f.expand(t, &expandRecorder{out: []byte(out)})
			if len(res.Items) != 1 || (res.Items[0].Result == RunResultSucceeded) != tc.ok {
				t.Fatalf("items = %+v, want ok=%v", res.Items, tc.ok)
			}
			if added := f.taskByKey(t, "c") != nil; added != tc.ok {
				t.Errorf("task added = %v, want %v", added, tc.ok)
			}
			if !tc.ok {
				if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND error LIKE '%unexpected spec landing%'`, f.cid()); n != 1 {
					t.Errorf("runs with the reason = %d, want 1", n)
				}
			}
		})
	}
}
