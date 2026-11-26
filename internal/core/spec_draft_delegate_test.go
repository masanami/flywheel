package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// このファイルは、仕様の下書きを委譲へ渡す部分（タスクの文面・起動の入力の全文・起動するタスクを選ぶときの読み直し。
// docs/features/request-to-plan.md の S2a）を確かめる。テストの名前の AC の番号は、Issue #1332 の受け持ち
// （親 #1329 の受入基準の並び順）。

const (
	sdPath = ".flywheel/specs/example.md"
	sdBase = "docs/features/example.md"
	// sdDoc は区切りの行に当たる行を含まず、末尾に改行の無い下書き。
	sdDoc = "# 下書き\n\nDRAFT-ONLY-PROSE 説明の文。\r\n\n## 受入基準\n\n- [ ] 基準いち\n- [ ] 基準に\n\n## 末尾の節\n\nDRAFT-TAIL"

	sdLandHeading   = "仕様の文書をリポジトリへ入れる"
	sdOtherHeading  = "仕様の文書"
	sdFullHeading   = "仕様の下書きの全文"
	sdFullInFile    = "\n# " + sdFullHeading + "\n\n"
	sdContractStart = `"start": ["tool", "start", "--file", "{task_file}", "--task", "{task_id}"]`
)

func sdCriteria(draft bool, base string) planAcceptanceCriteria {
	list := planAcceptanceCriteria{Path: sdPath, Draft: draft}
	for _, text := range []string{"基準いち", "基準に"} {
		list.Items = append(list.Items, acceptanceCriterion{ID: acceptanceCriterionID(text), Text: text})
	}
	if base != "" {
		list.BasePath = &base
	}
	return list
}

// sdPlan は、下書きの一覧 list を書き足した承認済みの計画の構造化した出力。tasks が 2 個以上なら統合先を使う。
// 先頭のタスクがすべての基準を受け持ち、下書きを入れる。2 個目より後は先頭の後に並ぶ。
func sdPlan(list planAcceptanceCriteria, repo, operation string, keys ...string) string {
	spec := planSpec(func(m map[string]any) {
		var tasks []any
		for i, key := range keys {
			task := j2TestTask(key, repo, operation)
			if i == 0 {
				task["lands_spec"] = list.Draft
				task["covers"] = []string{list.Items[0].ID, list.Items[1].ID}
			} else {
				task["depends_on"] = []string{keys[0]}
			}
			if len(keys) >= 2 {
				task["budget_impl_usd"], task["budget_review_usd"] = 2.0, 1.0
			}
			tasks = append(tasks, task)
		}
		m["tasks"] = tasks
		m["uses_integration"] = len(keys) >= 2
	})
	return string(addAcceptanceCriteria([]byte(spec), list))
}

// sdSetSpec は課題の仕様の文書の場所を直接書く（空は場所なし）。
func sdSetSpec(t *testing.T, s *Store, id, specPath, base string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	null := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET spec_path = ?, spec_base_path = ? WHERE id = ?`, null(specPath), null(base), cid)
		return err
	})
}

// sdNew は下書き（sdDoc）を持つ着手中の課題を作る。
func sdNew(t *testing.T, f *delegateFixture, title string, list planAcceptanceCriteria, repo, operation string, keys ...string) string {
	t.Helper()
	id := f.newInProgress(t, title, "P1", sdPlan(list, repo, operation, keys...))
	writeWorkspaceFile(t, f.s.workspace, sdPath, sdDoc)
	base := ""
	if list.BasePath != nil {
		base = *list.BasePath
	}
	sdSetSpec(t, f.s, id, sdPath, base)
	return id
}

func sdPluginRun(t *testing.T, f *delegateFixture, id string) DelegateLaunchInput {
	t.Helper()
	res, err := f.run(t, idPtr(id))
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	launched := f.deleg.launched()
	if len(launched) != 1 {
		t.Fatalf("launched = %d, result = %+v", len(launched), res)
	}
	return launched[0]
}

// sdSetTaskDone はタスク（鍵 key）を済みにする。
func sdSetTaskDone(t *testing.T, s *Store, id, key string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = 'done' WHERE challenge_id = ? AND key = ?`, cid, key)
		return err
	})
}

// sdSection は文面の「## <見出し>」の節の本文（次の見出しの前まで）。
func sdSection(doc, heading string) (string, bool) {
	_, rest, ok := strings.Cut(doc, "\n## "+heading+"\n\n")
	if !ok {
		return "", false
	}
	if i := strings.Index(rest, "\n#"); i >= 0 {
		rest = rest[:i]
	}
	return rest, true
}

func sdRequireNoSpecSections(t *testing.T, text string) {
	t.Helper()
	for _, h := range []string{sdOtherHeading, sdLandHeading, sdFullHeading} {
		if strings.Contains(text, "# "+h+"\n") || strings.Contains(text, "\n"+h+":\n") {
			t.Errorf("text must not have the section %q:\n%s", h, text)
		}
	}
}

func TestSpecDraftDelegate_PluginLandingTask(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, base string
		want       []string
	}{
		{"AC-69・AC-70・AC-71 置き換える元あり", sdBase, []string{"そのまま入れる", "受入基準の文を変えない", sdBase, "丸ごと置き換える"}},
		{"AC-69・AC-70・AC-72 置き換える元なし", "", []string{"そのまま入れる", "受入基準の文を変えない", "新しい文書として置く"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDelegateFixture(t)
			id := sdNew(t, f, "land", sdCriteria(true, tc.base), "main-repo", "impl", "a")
			in := sdPluginRun(t, f, id)
			_, thisTask, ok := strings.Cut(in.Brief, "\n# このタスク\n")
			if !ok {
				t.Fatalf("no このタスク:\n%s", in.Brief)
			}
			thisTask, _, _ = strings.Cut(thisTask, "\n# ほかのタスク\n")
			section, ok := sdSection(thisTask, sdLandHeading)
			if !ok {
				t.Fatalf("no section:\n%s", thisTask)
			}
			for _, w := range tc.want {
				if !strings.Contains(section, w) {
					t.Errorf("section lacks %q:\n%s", w, section)
				}
			}
			if tc.base == "" && strings.Contains(section, "丸ごと置き換える") {
				t.Errorf("section must not say to replace:\n%s", section)
			}
			if strings.Index(thisTask, "## "+coveredCriteriaHeading) > strings.Index(thisTask, "## "+sdLandHeading) ||
				strings.Index(thisTask, "## "+sdLandHeading) > strings.Index(thisTask, "## やらないこと") {
				t.Errorf("the section must be between 受け持つ受入基準 and やらないこと:\n%s", thisTask)
			}
			// AC-75（core の側）・AC-76: 全文は起動の入力に載り、タスクの文書には入らない。
			if in.SpecDraft != sdDoc {
				t.Errorf("SpecDraft = %q, want the file content", in.SpecDraft)
			}
			if strings.Contains(in.Brief, "DRAFT-ONLY-PROSE") || strings.Contains(in.Brief, "DRAFT-TAIL") {
				t.Errorf("the brief must not have the full draft:\n%s", in.Brief)
			}
		})
	}
}

func TestSpecDraftDelegate_BriefForm(t *testing.T) { // AC-77・AC-78・AC-79（core の側）
	t.Parallel()
	f := newDelegateFixture(t)
	id := sdNew(t, f, "brief", sdCriteria(true, sdBase), "main-brief", "brief", "a")
	in := sdPluginRun(t, f, id)
	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 calls = %d", len(f.j3Inputs))
	}
	task, ok := j2SectionText(f.j3Inputs[0], labelTaskSection)
	if !ok || !strings.Contains(task, "\n"+sdLandHeading+":\n") || !strings.Contains(task, "そのまま入れる") ||
		!strings.Contains(task, "受入基準の文を変えない") || !strings.Contains(task, sdBase) {
		t.Errorf("task section = %q", task)
	}
	all, err := json.Marshal(f.j3Inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range f.j3Inputs[0].Sections {
		all = append(all, sec.Content...)
	}
	if strings.Contains(string(all), "DRAFT-ONLY-PROSE") || strings.Contains(string(all), "DRAFT-TAIL") {
		t.Error("the J3 input must not have the full draft")
	}
	if in.SpecDraft != sdDoc {
		t.Errorf("SpecDraft = %q", in.SpecDraft)
	}
}

func TestSpecDraftDelegate_NonLandingTask(t *testing.T) { // AC-81・AC-82・AC-84・AC-85・AC-95
	t.Parallel()
	for _, tc := range []struct{ name, repo, operation string }{
		{"plugin", "main-repo", "impl"},
		{"指示文だけ", "main-brief", "brief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDelegateFixture(t)
			id := sdNew(t, f, "other", sdCriteria(true, sdBase), tc.repo, tc.operation, "a", "b")
			// 統合先を使わない課題の形にして、入れるタスクを済みにする。
			cid, _ := parseChallengeID(id)
			setUsesIntegrationForTest(t, f.s, cid, false)
			sdSetTaskDone(t, f.s, id, "a")
			if err := os.Remove(f.s.workspace + "/" + sdPath); err != nil {
				t.Fatal(err)
			}
			in := sdPluginRun(t, f, id)
			if in.SpecDraft != "" {
				t.Errorf("SpecDraft = %q, want none for a task that does not land the spec", in.SpecDraft)
			}
			var doc, heading string
			if tc.operation == "brief" {
				task, _ := j2SectionText(f.j3Inputs[0], labelTaskSection)
				doc, heading = task, "\n"+sdOtherHeading+":\n"
			} else {
				doc, heading = in.Brief, "\n## "+sdOtherHeading+"\n\n"
			}
			_, section, ok := strings.Cut(doc, heading)
			if !ok {
				t.Fatalf("no section %q:\n%s", sdOtherHeading, doc)
			}
			if !strings.Contains(section, "タスク a") || !strings.Contains(section, sdBase) {
				t.Errorf("section = %q, want the landing task title and the base path", section)
			}
			if strings.Contains(doc, sdLandHeading+"\n\n") || strings.Contains(doc, "\n"+sdLandHeading+":\n") {
				t.Errorf("a non-landing task must not have the landing section:\n%s", doc)
			}
		})
	}
}

func TestSpecDraftDelegate_NoDraftKeepsTheOldShape(t *testing.T) {
	t.Parallel()
	t.Run("AC-86 下書きを持たない課題", func(t *testing.T) {
		t.Parallel()
		f := newDelegateFixture(t)
		in := sdPluginRun(t, f, f.newInProgress(t, "plain", "P1", planSpec(nil)))
		sdRequireNoSpecSections(t, in.Brief)
		if in.SpecDraft != "" {
			t.Errorf("SpecDraft = %q", in.SpecDraft)
		}
	})
	t.Run("AC-88 draft を持たない一覧", func(t *testing.T) {
		t.Parallel()
		f := newDelegateFixture(t)
		list := sdCriteria(false, "")
		spec := sdPlan(list, "main-repo", "impl", "a")
		var m map[string]any
		if err := json.Unmarshal([]byte(spec), &m); err != nil {
			t.Fatal(err)
		}
		delete(m["acceptance_criteria"].(map[string]any), "draft")
		delete(m["acceptance_criteria"].(map[string]any), "base_path")
		old, _ := json.Marshal(m)
		id := f.newInProgress(t, "s1", "P1", string(old))
		sdSetSpec(t, f.s, id, sdPath, "")
		in := sdPluginRun(t, f, id)
		sdRequireNoSpecSections(t, in.Brief)
		want := "## " + coveredCriteriaHeading + "\n\n- 基準いち（" + list.Items[0].ID + "）\n- 基準に（" + list.Items[1].ID + "）\n\n## やらないこと\n"
		if !strings.Contains(in.Brief, want) {
			t.Errorf("brief must keep the S1 shape:\n%s", in.Brief)
		}
		if in.SpecDraft != "" {
			t.Errorf("SpecDraft = %q", in.SpecDraft)
		}
	})
}

func TestSpecDraftDelegate_UsesThePlanListNotTheCurrentSpecPath(t *testing.T) { // AC-87
	t.Parallel()
	f := newDelegateFixture(t)
	id := sdNew(t, f, "moved", sdCriteria(true, ""), "main-repo", "impl", "a")
	sdSetSpec(t, f.s, id, "docs/features/other.md", "")
	in := sdPluginRun(t, f, id)
	if _, ok := sdSection(in.Brief, sdLandHeading); !ok {
		t.Errorf("no landing section:\n%s", in.Brief)
	}
	if in.SpecDraft != sdDoc {
		t.Errorf("SpecDraft = %q", in.SpecDraft)
	}
}

func sdRequireDraftChanged(t *testing.T, f *delegateFixture, res *DelegateResult, id string) {
	t.Helper()
	var got []NotStarted
	for _, n := range res.NotStarted {
		if n.ChallengeID == id {
			got = append(got, n)
		}
	}
	if len(got) != 1 || got[0].Reason != NotStartedSpecDraftChanged {
		t.Fatalf("not started = %+v, want one spec_draft_changed", res.NotStarted)
	}
	if !strings.Contains(got[0].Detail, sdPath) || !strings.Contains(got[0].Detail, firstTaskID(t, f.s, id)) {
		t.Errorf("detail = %q, want the draft path and the task", got[0].Detail)
	}
	for _, in := range f.deleg.launched() {
		if strings.Contains(in.Brief, "題名: タスク a\n") && in.SpecDraft != "" {
			t.Errorf("the landing task must not be launched: %+v", in)
		}
	}
}

func TestSpecDraftDelegate_DraftChangedSinceThePlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, ws string)
	}{
		{"AC-89・AC-90 ファイルが消えた", func(t *testing.T, ws string) {
			if err := os.Remove(ws + "/" + sdPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"AC-91 受入基準の行が足された", func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, sdPath, strings.Replace(sdDoc, "- [ ] 基準に\n", "- [ ] 基準に\n- [ ] 基準さん\n", 1))
		}},
		{"受入基準の順が入れ替わった", func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, sdPath, strings.Replace(sdDoc, "- [ ] 基準いち\n- [ ] 基準に\n", "- [ ] 基準に\n- [ ] 基準いち\n", 1))
		}},
	} {
		for _, byID := range []bool{true, false} {
			name := tc.name + "（ID 省略）"
			if byID {
				name = tc.name + "（ID 指定）"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newDelegateFixture(t)
				id := sdNew(t, f, "changed", sdCriteria(true, ""), "main-repo", "impl", "a")
				tc.change(t, f.s.workspace)
				var target *string
				if byID {
					target = &id
				}
				res := mustRunResult(t, f, target)
				sdRequireDraftChanged(t, f, res, id)
				if n := len(f.deleg.launched()); n != 0 {
					t.Errorf("launched = %d", n)
				}
				if got := taskStatusOf(t, f.s, id, "a"); got != TaskStatusPending {
					t.Errorf("task = %s", got)
				}
				// AC-93: 計画の時点の受入基準へ戻せば、次は選ばれる。
				writeWorkspaceFile(t, f.s.workspace, sdPath, sdDoc)
				if in := sdPluginRun(t, f, id); in.SpecDraft != sdDoc {
					t.Errorf("SpecDraft = %q", in.SpecDraft)
				}
			})
		}
	}
}

func TestSpecDraftDelegate_OnlyProseChanged_IsSelectedWithTheNewText(t *testing.T) { // AC-92
	t.Parallel()
	f := newDelegateFixture(t)
	id := sdNew(t, f, "prose", sdCriteria(true, ""), "main-repo", "impl", "a")
	changed := strings.Replace(sdDoc, "説明の文。", "直した説明の文。", 1)
	writeWorkspaceFile(t, f.s.workspace, sdPath, changed)
	if in := sdPluginRun(t, f, id); in.SpecDraft != changed {
		t.Errorf("SpecDraft = %q, want the changed text", in.SpecDraft)
	}
}

func TestSpecDraftDelegate_OtherChallengesAreStillLaunched(t *testing.T) { // AC-94
	t.Parallel()
	f := newDelegateFixture(t)
	f.git.states[f.s.Workspace()+"/slot-s"] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/s.git"}
	broken := sdNew(t, f, "broken", sdCriteria(true, ""), "main-repo", "impl", "a")
	other := f.newInProgress(t, "fine", "P2", planSpec(func(m map[string]any) { m["repo"] = "sibling" }))
	if err := os.Remove(f.s.workspace + "/" + sdPath); err != nil {
		t.Fatal(err)
	}
	res := mustRunResult(t, f, nil)
	sdRequireDraftChanged(t, f, res, broken)
	if len(res.Items) != 1 || res.Items[0].ChallengeID != other || len(f.deleg.launched()) != 1 {
		t.Errorf("result = %+v, want the other challenge launched", res)
	}
}

// --- 契約の形 ---

func sdContractFixture(t *testing.T) *contractFixture {
	t.Helper()
	conn := strings.Replace(contractConnectorsJSON, `"start": ["tool", "start", "--challenge", "{challenge_id}"]`, sdContractStart, 1)
	if conn == contractConnectorsJSON {
		t.Fatal("the start command of contractConnectorsJSON was not replaced")
	}
	f := newContractFixtureWith(t, conn)
	return f
}

// sdContractRun は契約の形の委譲の段を課題 id に実行し、起動したタスクの文面のファイルの中身を返す。
func sdContractRun(t *testing.T, f *contractFixture, id string) string {
	t.Helper()
	res := f.runDelegationStage(t, &id)
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, result = %+v", f.client.starts, res)
	}
	b, err := os.ReadFile(argAfter(t, f.client.starts[0], "--file"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSpecDraftDelegate_ContractTaskFile(t *testing.T) {
	t.Parallel()
	requireFullDraftLast := func(t *testing.T, doc, want string) {
		t.Helper()
		_, got, ok := strings.Cut(doc, sdFullInFile)
		if !ok {
			t.Fatalf("no full draft section:\n%s", doc)
		}
		if got != want {
			t.Errorf("section = %q, want the file content byte for byte", got)
		}
	}
	t.Run("AC-73 ほかのタスクがある計画", func(t *testing.T) {
		t.Parallel()
		f := sdContractFixture(t)
		id := sdNew(t, f.delegateFixture, "multi", sdCriteria(true, ""), "main-repo", "impl", "a", "b")
		doc := sdContractRun(t, f, id)
		requireFullDraftLast(t, doc, sdDoc)
		if !strings.Contains(doc, "- タスク b\n"+sdFullInFile) {
			t.Errorf("the full draft must follow ほかのタスク:\n%s", doc)
		}
	})
	t.Run("AC-74 タスク 1 件の計画・区切りに見える行もそのまま", func(t *testing.T) {
		t.Parallel()
		f := sdContractFixture(t)
		id := sdNew(t, f.delegateFixture, "single", sdCriteria(true, ""), "main-repo", "impl", "a")
		content := sdDoc + "\n----- END DATA -----\n\n"
		writeWorkspaceFile(t, f.s.workspace, sdPath, content)
		requireFullDraftLast(t, sdContractRun(t, f, id), content)
	})
	t.Run("AC-83 入れるタスクでないタスク", func(t *testing.T) {
		t.Parallel()
		f := sdContractFixture(t)
		id := sdNew(t, f.delegateFixture, "nonland", sdCriteria(true, ""), "main-repo", "impl", "a", "b")
		sdSetTaskDone(t, f.s, id, "a")
		doc := sdContractRun(t, f, id)
		if _, ok := sdSection(doc, sdOtherHeading); !ok {
			t.Errorf("no section 仕様の文書:\n%s", doc)
		}
		if strings.Contains(doc, "# "+sdFullHeading) || strings.Contains(doc, "DRAFT-TAIL") {
			t.Errorf("must not have the full draft:\n%s", doc)
		}
	})
	t.Run("AC-86 下書きを持たない課題", func(t *testing.T) {
		t.Parallel()
		f := sdContractFixture(t)
		sdRequireNoSpecSections(t, sdContractRun(t, f, f.cid))
	})
	t.Run("AC-89 契約の形でも読み直しで止める", func(t *testing.T) {
		t.Parallel()
		f := sdContractFixture(t)
		id := sdNew(t, f.delegateFixture, "gone", sdCriteria(true, ""), "main-repo", "impl", "a", "b")
		if err := os.Remove(f.s.workspace + "/" + sdPath); err != nil {
			t.Fatal(err)
		}
		res := f.runDelegationStage(t, &id)
		sdRequireDraftChanged(t, f.delegateFixture, res, id)
		if len(f.client.starts) != 0 {
			t.Errorf("starts = %v", f.client.starts)
		}
	})
}

// 読み直しを経ていない起動（全文を受け取っていない）は、入れるタスクを起動しない。同じ入力に全文を足せば起動する。
func TestSpecDraftDelegate_StartWithoutTheDraftIsRefused(t *testing.T) {
	t.Parallel()
	f := sdContractFixture(t)
	f.cid = sdNew(t, f.delegateFixture, "direct", sdCriteria(true, ""), "main-repo", "impl", "a")
	in := ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5, Reconcile: f.branches,
	}
	_, err := f.s.StartContractDelegation(context.Background(), in)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "the draft text is not given") || len(f.client.starts) != 0 {
		t.Fatalf("err = %v, starts = %v, want refused for the missing draft", err, f.client.starts)
	}
	in.SpecDraft = sdDoc
	if _, err := f.s.StartContractDelegation(context.Background(), in); err != nil || len(f.client.starts) != 1 {
		t.Errorf("with the draft: err = %v, starts = %v, want started", err, f.client.starts)
	}
}
