package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// 下書きづくりの記録・判定 needs_spec と出力の検査・入力の区画・詳細の値（docs/features/request-to-plan-s2b.md の S2b-1）を
// 実ストアで確かめる。

const specDraftConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "tool", "form": "flow", "permission_mode": "auto", "operations": [{"id": "impl", "flow": "ticket"}]},
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [{"id": "brief"}]}
  ],
  "repos": [
    {"name": "flow-wt", "remote": "o/wt", "default_branch": "main", "connector": "tool", "slots": {"provider": "worktree", "base": "w1", "count": 1}},
    {"name": "flow-wt2", "remote": "o/wt2", "default_branch": "main", "connector": "tool", "slots": {"provider": "worktree", "base": "w2", "count": 1}},
    {"name": "flow-clone", "remote": "o/cl", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["c1"]}},
    {"name": "brief-repo", "remote": "o/br", "default_branch": "main", "connector": "direct", "slots": {"provider": "worktree", "base": "w3", "count": 1}}
  ]
}`

const needsSpecReason = "既存の文書が受け持たない振る舞いを足す"

func specDraftConn(t *testing.T, doc string) *ConnectorsDeclaration {
	t.Helper()
	decl, err := parseConnectorsDeclaration([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := decl.validate(); err != nil {
		t.Fatal(err)
	}
	return decl
}

// newDraftFixture は対象にできるリポジトリを持つ宣言と、分類済の課題を持つ。
func newDraftFixture(t *testing.T) (*j2Fixture, string) {
	t.Helper()
	f := newJ2Fixture(t)
	f.conn = specDraftConn(t, specDraftConnectorsJSON)
	return f, f.newClassified(t, "spec draft", "P1").ID
}

func needsSpecOutput(repo string, basePath any) []byte {
	b, _ := json.Marshal(map[string]any{"verdict": "needs_spec",
		"spec_draft": map[string]any{"repo": repo, "base_path": basePath, "reason": needsSpecReason}})
	return b
}

// putSpecDraft は課題の記録を作り、状態 state にする。
func putSpecDraft(t *testing.T, s *Store, id string, u specDraftUpdate) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		d := SpecDraft{Path: specDraftPathFor(cid), Repo: "flow-wt", Reason: needsSpecReason}
		if err := insertSpecDraft(context.Background(), tx, cid, d, time.Now()); err != nil {
			return err
		}
		return updateSpecDraft(context.Background(), tx, cid, u, time.Now())
	}); err != nil {
		t.Fatalf("putSpecDraft: %v", err)
	}
}

// runNeedsSpec は課題へ計画の呼び出しを 1 回行う。登録できなければ理由（run の誤りの文）を返す。
func runDraftJ2(t *testing.T, f *j2Fixture, id string, out []byte) (JudgmentAutoItem, string) {
	t.Helper()
	res, err := f.run(t, succeededInvoker(out), idPtr(id))
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("PlanAutoJ2: %+v %v", res, err)
	}
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, r := range runs {
		if r.ID == res.Items[0].RunID {
			reason = r.Error
		}
	}
	return res.Items[0], reason
}

func requireInvalidDraftOutput(t *testing.T, f *j2Fixture, id string, out []byte, want string) {
	t.Helper()
	before := f.detail(t, id).SpecDraft
	item, reason := runDraftJ2(t, f, id, out)
	if item.Result != RunResultInvalidOutput || !strings.Contains(reason, want) {
		t.Errorf("result = %s, reason = %q, want invalid_output containing %q", item.Result, reason, want)
	}
	d := f.detail(t, id)
	if !reflect.DeepEqual(d.SpecDraft, before) || d.Status != StatusClassified {
		t.Errorf("an invalid output changed the challenge: %+v", d.SpecDraft)
	}
}

func TestPlanAutoJ2_NeedsSpecMakesAPendingRecordWithoutAPlan(t *testing.T) { // AC-1・AC-2・AC-3・AC-4・AC-5・AC-7・AC-43
	t.Parallel()
	f, id := newDraftFixture(t)
	item, reason := runDraftJ2(t, f, id, needsSpecOutput("flow-wt", "docs/features/example.md"))
	if item.Result != RunResultSucceeded || item.Outcome != "needs_spec" || item.Status == nil || *item.Status != string(StatusClassified) {
		t.Fatalf("item = %+v (%s)", item, reason)
	}
	d := f.detail(t, id)
	cid, _ := parseChallengeID(id)
	want := &SpecDraft{Path: ".flywheel/specs/" + strings.ToLower(id) + ".md", BasePath: strPtr("docs/features/example.md"),
		Repo: "flow-wt", Reason: needsSpecReason, State: SpecDraftStatePending}
	if !reflect.DeepEqual(d.SpecDraft, want) {
		t.Errorf("record = %+v, want %+v", d.SpecDraft, want)
	}
	if len(d.Plans) != 0 || d.Status != StatusClassified || d.SpecPath != nil {
		t.Errorf("plans = %d, status = %s, spec_path = %v", len(d.Plans), d.Status, d.SpecPath)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM spec_draft WHERE challenge_id = ? AND state = 'pending'`, cid); n != 1 {
		t.Errorf("spec_draft rows = %d, want 1", n)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'spec_draft_requested'`, cid); n != 1 {
		t.Errorf("spec_draft_requested = %d, want 1", n)
	}
	if got := specDraftPathFor(123); got != ".flywheel/specs/c-123.md" {
		t.Errorf("AC-4: path of C-123 = %q", got)
	}
}

func TestPlanAutoJ2_NeedsSpecWithNullBasePath(t *testing.T) { // AC-6
	t.Parallel()
	f, id := newDraftFixture(t)
	runDraftJ2(t, f, id, needsSpecOutput("flow-wt", nil))
	cid, _ := parseChallengeID(id)
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM spec_draft WHERE challenge_id = ? AND base_path IS NULL`, cid); n != 1 {
		t.Errorf("records with a NULL base_path = %d, want 1", n)
	}
}

func TestPlanAutoJ2_NeedsSpecNotAvailable(t *testing.T) { // AC-8〜AC-13
	t.Parallel()
	only := func(repo string) string {
		var doc map[string]any
		if err := json.Unmarshal([]byte(specDraftConnectorsJSON), &doc); err != nil {
			panic(err)
		}
		doc["repos"] = slices.DeleteFunc(doc["repos"].([]any), func(r any) bool { return r.(map[string]any)["name"] != repo })
		b, _ := json.Marshal(doc)
		return string(b)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *j2Fixture, id string)
	}{
		{"AC-8 spec path", func(t *testing.T, f *j2Fixture, id string) {
			writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
			f.setSpec(t, id, criteriaDocPath)
		}},
		{"AC-9 skipped record", func(t *testing.T, f *j2Fixture, id string) {
			putSpecDraft(t, f.s, id, specDraftUpdate{State: SpecDraftStateSkipped})
		}},
		{"AC-10 done task", func(t *testing.T, f *j2Fixture, id string) {
			cid, _ := parseChallengeID(id)
			addTaskWithStatus(t, f.s, cid, 1, "a", TaskStatusDone)
		}},
		{"AC-11 task in progress", func(t *testing.T, f *j2Fixture, id string) {
			cid, _ := parseChallengeID(id)
			addTaskWithStatus(t, f.s, cid, 1, "a", TaskStatusInProgress)
		}},
		{"AC-12 no flow repo", func(t *testing.T, f *j2Fixture, _ string) { f.conn = specDraftConn(t, only("brief-repo")) }},
		{"AC-13 clone only", func(t *testing.T, f *j2Fixture, _ string) { f.conn = specDraftConn(t, only("flow-clone")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := newDraftFixture(t)
			tc.setup(t, f, id)
			requireInvalidDraftOutput(t, f, id, needsSpecOutput("flow-wt", nil), "spec draft not available")
		})
	}
}

func TestPlanAutoJ2_NeedsSpecAfterAPlanRejection(t *testing.T) { // AC-14
	t.Parallel()
	f, id := newDraftFixture(t)
	f.rejectPlanForTest(t, id, "仕様の文書を作ってから", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ttyAtt)
	f.s.now = nil
	cid, _ := parseChallengeID(id)
	addTaskWithStatus(t, f.s, cid, 1, "pending-task", TaskStatusPending)
	addTaskWithStatus(t, f.s, cid, 1, "cancelled-task", TaskStatusCancelled)
	item, reason := runDraftJ2(t, f, id, needsSpecOutput("flow-wt", nil))
	if d := f.detail(t, id); item.Result != RunResultSucceeded || d.SpecDraft == nil || d.SpecDraft.State != SpecDraftStatePending {
		t.Errorf("item = %+v (%s), record = %+v", item, reason, d.SpecDraft)
	}
}

func TestValidateJ2Output_NeedsSpec(t *testing.T) { // AC-15〜AC-22
	t.Parallel()
	conn := specDraftConn(t, specDraftConnectorsJSON)
	vc := j2ValidationContext{Agent: defaultAgentDeclaration(), Conn: conn, SpecDraftRepos: specDraftTargetRepos(conn)}
	out := func(mut func(m, sd map[string]any)) []byte {
		sd := map[string]any{"repo": "flow-wt", "base_path": nil, "reason": needsSpecReason}
		m := map[string]any{"verdict": "needs_spec", "spec_draft": sd}
		if mut != nil {
			mut(m, sd)
		}
		b, _ := json.Marshal(m)
		return b
	}
	set := func(key string, v any) func(m, sd map[string]any) {
		return func(_, sd map[string]any) { sd[key] = v }
	}
	for _, tc := range []struct {
		name string
		out  []byte
		want string // 空は検査を通る
	}{
		{"valid", out(nil), ""},
		{"base_path omitted", out(func(_, sd map[string]any) { delete(sd, "base_path") }), ""},
		{"base_path of 500", out(set("base_path", strings.Repeat("a", 500))), ""},
		{"reason of 2000", out(set("reason", strings.Repeat("あ", 2000))), ""},
		{"AC-15 undeclared repo", out(set("repo", "ghost")), "spec draft repo invalid"},
		{"AC-16 not a flow repo", out(set("repo", "brief-repo")), "spec draft repo invalid"},
		{"AC-17 clone repo", out(set("repo", "flow-clone")), "spec draft repo invalid"},
		{"AC-18 absolute", out(set("base_path", "/etc/example.md")), "spec draft base path invalid"},
		{"AC-19 dot dot", out(set("base_path", "docs/../example.md")), "spec draft base path invalid"},
		{"AC-20 501 characters", out(set("base_path", strings.Repeat("a", 501))), "spec draft base path invalid"},
		{"AC-21 no spec_draft", out(func(m, _ map[string]any) { delete(m, "spec_draft") }), "requires spec_draft"},
		{"AC-21 null spec_draft", out(func(m, _ map[string]any) { m["spec_draft"] = nil }), "requires spec_draft"},
		{"AC-22 reason of 2001", out(set("reason", strings.Repeat("あ", 2001))), "spec_draft.reason is longer"},
		{"blank reason", out(set("reason", " \n")), "must not be blank"},
		{"spec_draft with another verdict", out(func(m, _ map[string]any) { m["verdict"] = "uncertain"; m["question"] = "q" }), "spec_draft must be omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reason := validateJ2OutputReason(tc.out, vc)
			if tc.want == "" && reason != "" || tc.want != "" && !strings.Contains(reason, tc.want) {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestJ2ExpandOutputSchema_HasNoNeedsSpec(t *testing.T) { // AC-23
	t.Parallel()
	for _, origin := range []TaskAdditionOrigin{TaskAdditionOriginExpand, TaskAdditionOriginFollowup} {
		if got := string(j2ExpandOutputSchemaFor(origin)); strings.Contains(got, "needs_spec") || strings.Contains(got, "spec_draft") {
			t.Errorf("expand schema (%s) has the spec draft items: %s", origin, got)
		}
	}
	if got := string(j2OutputSchema()); !strings.Contains(got, `"needs_spec"`) || !strings.Contains(got, `"spec_draft"`) {
		t.Errorf("plan schema lacks the spec draft items: %s", got)
	}
}

func draftSection(t *testing.T, f *j2Fixture, id string) string {
	t.Helper()
	content, ok := j2SectionText(planInput(t, f, id), "仕様の下書きづくり")
	if !ok {
		t.Fatal("no section 仕様の下書きづくり")
	}
	return content
}

func TestPlanAutoJ2_SpecDraftSectionWhenAvailable(t *testing.T) { // AC-24・AC-25・AC-26
	t.Parallel()
	f, id := newDraftFixture(t)
	got := draftSection(t, f, id)
	for _, want := range []string{"回せる", "flow-wt", "flow-wt2"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"flow-clone", "brief-repo", "回せない"} {
		if strings.Contains(got, never) {
			t.Errorf("section has %q:\n%s", never, got)
		}
	}
}

func TestPlanAutoJ2_SpecDraftSectionReasons(t *testing.T) { // AC-27〜AC-35
	t.Parallel()
	withSpec := func(t *testing.T, f *j2Fixture, id string) {
		writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
		f.setSpec(t, id, criteriaDocPath)
	}
	record := func(state SpecDraftState) func(t *testing.T, f *j2Fixture, id string) {
		return func(t *testing.T, f *j2Fixture, id string) { putSpecDraft(t, f.s, id, specDraftUpdate{State: state}) }
	}
	for _, tc := range []struct {
		name   string
		setup  []func(t *testing.T, f *j2Fixture, id string)
		reason string
		never  string
	}{
		{"AC-27・AC-28・AC-29 done", []func(*testing.T, *j2Fixture, string){record(SpecDraftStateDone), withSpec}, "下書きづくりを済ませた", "仕様の文書がある"},
		{"AC-30 spec path", []func(*testing.T, *j2Fixture, string){withSpec}, "仕様の文書がある", ""},
		{"AC-31 skipped", []func(*testing.T, *j2Fixture, string){record(SpecDraftStateSkipped)}, "オーナーが仕様の文書なしで計画すると決めた", ""},
		{"AC-32・AC-33 skipped with spec path", []func(*testing.T, *j2Fixture, string){record(SpecDraftStateSkipped), withSpec}, "仕様の文書がある", "オーナーが仕様の文書なしで計画すると決めた"},
		{"AC-34 done task", []func(*testing.T, *j2Fixture, string){func(t *testing.T, f *j2Fixture, id string) {
			cid, _ := parseChallengeID(id)
			addTaskWithStatus(t, f.s, cid, 1, "a", TaskStatusDone)
		}}, "着手したタスクがある", ""},
		{"AC-35 no target repo", []func(*testing.T, *j2Fixture, string){func(t *testing.T, f *j2Fixture, _ string) { f.conn = j2TestConnectors(t) }}, "対象にできるリポジトリが無い", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := newDraftFixture(t)
			for _, s := range tc.setup {
				s(t, f, id)
			}
			// 区画を読むだけなので、出力は検査を通らなくてよい。
			got := draftSection(t, f, id)
			if !strings.Contains(got, "回せない") || !strings.Contains(got, tc.reason) {
				t.Errorf("section = %q, want 回せない with %q", got, tc.reason)
			}
			if tc.never != "" && strings.Contains(got, tc.never) {
				t.Errorf("section = %q must not have %q", got, tc.never)
			}
		})
	}
}

func TestSelectJ2AutoTargets_SpecDraftStates(t *testing.T) { // AC-36〜AC-41
	t.Parallel()
	for _, tc := range []struct {
		state    SpecDraftState
		selected bool
	}{
		{SpecDraftStatePending, false}, {SpecDraftStateRunning, false}, {SpecDraftStateAwaitingOwner, false},
		{SpecDraftStateDone, true}, {SpecDraftStateSkipped, true},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			f, id := newDraftFixture(t)
			putSpecDraft(t, f.s, id, specDraftUpdate{State: tc.state})
			var invoked bool
			inv := succeededInvoker(j2OutputFor(false, func(m map[string]any) { m["repo"] = "brief-repo"; m["operation"] = "brief" }))
			inv.onInvoke = func(JudgmentLaunchInput) { invoked = true }
			res, err := f.run(t, inv, nil)
			if err != nil {
				t.Fatal(err)
			}
			if invoked != tc.selected || len(res.NotStarted) != 0 {
				t.Errorf("invoked = %v, want %v; not_started = %+v", invoked, tc.selected, res.NotStarted)
			}
			if !tc.selected {
				if _, err := f.run(t, inv, idPtr(id)); !errors.Is(err, ErrInvalidTransition) || invoked {
					t.Errorf("with the ID: err = %v, invoked = %v, want invalid transition without invoking", err, invoked)
				}
			}
		})
	}
}

func TestPlanAutoJ2_PlanWithoutRecordProceeds(t *testing.T) { // AC-42
	t.Parallel()
	f, id := newDraftFixture(t)
	item, reason := runDraftJ2(t, f, id, j2OutputFor(false, func(m map[string]any) { m["repo"] = "brief-repo"; m["operation"] = "brief" }))
	if d := f.detail(t, id); item.Result != RunResultSucceeded || d.Status != StatusAwaitingPlanApproval || d.SpecDraft != nil {
		t.Errorf("item = %+v (%s), status = %s", item, reason, d.Status)
	}
}

// landingFixture は下書きを持つ分類済の課題。state が空でなければ、対象のリポジトリ harness-repo の記録を持つ。
func landingFixture(t *testing.T, state SpecDraftState) (*j2Fixture, string) {
	t.Helper()
	f, id := draftChallenge(t, landDraftPath, "")
	writeWorkspaceFile(t, f.s.workspace, landDraftPath, covDoc)
	if state != "" {
		cid, _ := parseChallengeID(id)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			if err := insertSpecDraft(context.Background(), tx, cid, SpecDraft{Path: landDraftPath, Repo: "harness-repo", Reason: "r"}, time.Now()); err != nil {
				return err
			}
			return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: state}, time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f, id
}

func TestPlanAutoJ2_TargetRepoLineInTheCriteriaSection(t *testing.T) { // AC-44・AC-45
	t.Parallel()
	for state, want := range map[SpecDraftState]bool{SpecDraftStateDone: true, "": false, SpecDraftStateSkipped: false} {
		f, id := landingFixture(t, state)
		content, ok := j2SectionText(planInput(t, f, id), AcceptanceCriteriaSectionLabel)
		if !ok {
			t.Fatal("no section 受入基準")
		}
		if got := strings.Contains(content, "対象のリポジトリ"); got != want {
			t.Errorf("state %q: line = %v, want %v:\n%s", state, got, want, content)
		}
		if want {
			requireLines(t, content, "対象のリポジトリ: harness-repo")
		}
	}
}

func TestPlanAutoJ2_SpecLandingRepo(t *testing.T) { // AC-46〜AC-49
	t.Parallel()
	// タスク 2 件の計画。先頭が下書きを入れる。repos はタスクのリポジトリ。統合先を使う計画はリポジトリが 1 つなので、
	// 入れるタスクだけが別のリポジトリの計画は 1 タスクで、入れないタスクの違いは入れるタスクの無い作り直しの形で確かめる。
	one := func(repo string) []byte {
		return j2OutputFor(false, func(m map[string]any) {
			landPlan(landTask{lands: true})(m)
			m["tasks"].([]any)[0].(map[string]any)["repo"] = repo
		})
	}
	for _, tc := range []struct {
		name  string
		state SpecDraftState
		repo  string
		want  string
	}{
		{"AC-46 same repo", SpecDraftStateDone, "harness-repo", ""},
		{"AC-47 other repo", SpecDraftStateDone, "cli-repo-brief", "spec landing repo mismatch"},
		{"AC-49 no record", "", "cli-repo-brief", ""},
		{"skipped record", SpecDraftStateSkipped, "cli-repo-brief", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := landingFixture(t, tc.state)
			other := *specDraftRepoCopy(f.conn, "harness-repo")
			other.Name = "cli-repo-brief"
			f.conn.Repos = append(f.conn.Repos, other)
			item, reason := runDraftJ2(t, f, id, one(tc.repo))
			if tc.want == "" && item.Result != RunResultSucceeded || tc.want != "" && !strings.Contains(reason, tc.want) {
				t.Errorf("item = %+v, reason = %q, want %q", item, reason, tc.want)
			}
		})
	}
}

func specDraftRepoCopy(conn *ConnectorsDeclaration, name string) *ConnectorRepo {
	r, _ := conn.findConnectorRepo(name)
	c := *r
	return &c
}

func TestValidateJ2Output_SpecLandingRepoChecksOnlyTheLandingTaskAtRegistration(t *testing.T) { // AC-48・AC-50
	t.Parallel()
	vc := j2TestContext(t, false)
	list := landCriteria(true)
	vc.CheckCriteria, vc.Criteria, vc.SpecDraftLandingRepo = true, list, "harness-repo"
	// 済みのタスクが下書きを入れた後の作り直し（入れるタスクを持たない）では、別のリポジトリのタスクでも通る。
	notLanding := j2OutputFor(false, func(m map[string]any) {
		covPlan(nil, landAllIDs)(m)
		m["uses_integration"] = false
		m["tasks"].([]any)[0].(map[string]any)["repo"] = "flywheel"
		m["tasks"].([]any)[0].(map[string]any)["operation"] = "define"
	})
	title := "入れたタスク"
	done := vc
	done.DoneSpecLanding = &title
	if _, reason := validateJ2OutputReason(notLanding, done); reason != "" {
		t.Errorf("AC-48: reason = %q", reason)
	}

	landing := j2OutputFor(false, landPlan(landTask{lands: true}))
	other := vc
	other.SpecDraftLandingRepo = "flywheel"
	v, reason := validateJ2OutputReason(landing, other)
	if v != nil || !strings.Contains(reason, "spec landing repo mismatch") {
		t.Fatalf("registration: reason = %q", reason)
	}
	registered, reason := validateJ2OutputReason(landing, vc)
	if reason != "" {
		t.Fatal(reason)
	}
	// 承認した計画の検査し直し（登録の検査をしない）。
	recheck := j2TestContext(t, false)
	recheck.SkipFlowReason, recheck.RecheckCriteria, recheck.SpecDraftLandingRepo = true, true, "flywheel"
	if _, reason := validateJ2OutputReason(registered.Spec, recheck); reason != "" {
		t.Errorf("AC-50: recheck reason = %q", reason)
	}
}

func TestSpecDraft_DetailCarriesTheRecordAndTheOpenHold(t *testing.T) { // AC-357〜AC-366・AC-370〜AC-373
	t.Parallel()
	f, id := newDraftFixture(t)
	if got := f.detail(t, id).SpecDraft; got != nil {
		t.Fatalf("AC-357: record = %+v", got)
	}
	cid, _ := parseChallengeID(id)
	var holdID int64
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'classified', '2026-11-26T00:00:00.000Z')`, cid)
		if err != nil {
			return err
		}
		holdID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	questions := []SpecDraftQuestion{{Question: "Q?", Options: []SpecDraftOption{{Label: "a"}, {Label: "b"}}, Recommended: 2, Reason: "why"}}
	answers := []SpecDraftAnswer{{Attempt: 1, Question: "P?", Answer: "yes", By: "supervisor", Basis: []SpecDraftBasis{{Path: "docs/a.md", Locator: "§1"}}}}
	why := SpecDraftHoldQuestion
	putSpecDraft(t, f.s, id, specDraftUpdate{State: SpecDraftStateAwaitingOwner, Attempts: 2, Questions: questions, Answers: answers, HoldRowID: &holdID, HoldReason: &why})

	got := f.detail(t, id).SpecDraft
	want := &SpecDraft{Path: specDraftPathFor(cid), Repo: "flow-wt", Reason: needsSpecReason, State: SpecDraftStateAwaitingOwner, Attempts: 2,
		Questions: questions, Answers: answers, HoldID: strPtr(formatHoldID(holdID)), HoldReason: &why}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("record = %+v, want %+v", got, want)
	}

	// 閉じた保留は出さない。
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE hold SET closed_at = '2026-11-26T00:00:00.000Z' WHERE id = ?`, holdID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, id).SpecDraft; got.HoldID != nil || got.HoldReason != nil {
		t.Errorf("AC-372: hold = %v %v after the hold is closed", got.HoldID, got.HoldReason)
	}
}

func TestUpdateSpecDraft_RejectsValuesOutsideTheClosedSets(t *testing.T) {
	t.Parallel()
	f, id := newDraftFixture(t)
	cid, _ := parseChallengeID(id)
	bad := SpecDraftHoldReason("other")
	for name, u := range map[string]specDraftUpdate{
		"state":       {State: "other"},
		"hold reason": {State: SpecDraftStatePending, HoldReason: &bad},
	} {
		err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			return updateSpecDraft(context.Background(), tx, cid, u, time.Now())
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: SpecDraftStatePending}, time.Now())
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("no record: err = %v, want ErrNotFound", err)
	}
}

func TestImport_SpecDraftSurvivesTheRoundTrip(t *testing.T) { // AC-410〜AC-413
	t.Parallel()
	src, _ := newWorkspaceStore(t)
	id := createAndShow(t, src, CreateInput{Title: "drafting"}).ID
	cid, _ := parseChallengeID(id)
	var holdID int64
	why := SpecDraftHoldFailed
	if err := src.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'classified', '2026-11-26T00:00:00.000Z')`, cid)
		if err != nil {
			return err
		}
		if holdID, err = res.LastInsertId(); err != nil {
			return err
		}
		d := SpecDraft{Path: specDraftPathFor(cid), BasePath: strPtr("docs/features/example.md"), Repo: "flow-wt", Reason: needsSpecReason}
		if err := insertSpecDraft(context.Background(), tx, cid, d, time.Now()); err != nil {
			return err
		}
		return updateSpecDraft(context.Background(), tx, cid, specDraftUpdate{State: SpecDraftStateAwaitingOwner, Attempts: 3,
			Questions: []SpecDraftQuestion{{Question: "Q?", Options: []SpecDraftOption{{Label: "a"}, {Label: "b"}}, Recommended: 1, Reason: "why"}},
			Answers:   []SpecDraftAnswer{{Attempt: 1, Question: "P?", Answer: "yes", By: "owner", Basis: []SpecDraftBasis{}}},
			HoldRowID: &holdID, HoldReason: &why}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	want, got := showDetail(t, src, id).SpecDraft, showDetail(t, dst, id).SpecDraft
	if want == nil || want.HoldID == nil || want.HoldReason == nil || len(want.Questions) != 1 || len(want.Answers) != 1 || !reflect.DeepEqual(got, want) {
		t.Errorf("record after import = %+v, want %+v", got, want)
	}
}
