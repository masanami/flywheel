package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// approvalFixture は ws に課題 C-1 を作り、status にする。plan のときは計画（本文 "PLAN"）つき。
func approvalFixture(t *testing.T, ws string, status string) {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if _, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: "title", DoneCriteria: "crit"}); err != nil {
		t.Fatal(err)
	}
	if status == "awaiting_plan_approval" {
		coretest.SetChallengeStatus(t, ws, 1, "classified")
		if _, _, err := st.PlanChallenge(ctx, core.ChannelCLI, "C-1", core.PlanInput{Body: "PLAN"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	coretest.SetChallengeStatus(t, ws, 1, status)
}

func addOp(t *testing.T, ws, kind, summary string, ref *string) {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.CreateOperation(context.Background(), core.ChannelCLI, core.OperationInput{ChallengeID: "C-1", Kind: kind, Summary: summary, Ref: ref}); err != nil {
		t.Fatal(err)
	}
}

func approvalServer(t *testing.T, ws string) int {
	t.Helper()
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	return port
}

// apiPost は正しい CSRF（cookie とヘッダ）つきで本文を POST する。
func apiPost(t *testing.T, port int, path, body string) (int, string) {
	t.Helper()
	token, _ := fetchToken(t, port)
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "flywheel_csrf_" + strconv.Itoa(port), Value: token})
	req.Header.Set(CSRFHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const (
	approvalPath = "/api/v1/workspaces/w/challenges/C-1/approval"
	approvePath  = "/api/v1/workspaces/w/challenges/C-1/approve"
)

func errCode(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	assertCodeInTable(t, doc.Error.Code)
	return doc.Error.Code
}

func strp(s string) *string { return &s }

func TestApprovalSummaryForPlan(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	code, body := apiGet(t, port, approvalPath)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc struct {
		Approval map[string]json.RawMessage `json:"approval"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for k, v := range doc.Approval {
		got[k] = string(v)
	}
	want := map[string]string{
		"challenge_id": `"C-1"`, "title": `"title"`, "kind": `"plan"`, "done_criteria": `null`,
		"plan": `{"body":"PLAN","version":1}`, "releases": `[]`, "other_pending_operations": `[]`, "release_held": `true`,
		"pairs": `[]`, "stages": `{"actual":null,"expected":null,"figure":null,"issues":null,"issues_answer":null,"non_goals":null}`,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %s, want %s", k, got[k], w)
		}
	}
	if _, ok := got["version"]; !ok || len(got) != len(want)+1 {
		t.Errorf("keys = %v", got)
	}
}

func TestApprovalSummaryForCompletionResolvesHeads(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "PR1", strp("https://github.com/o/r/pull/1/files"))
	addOp(t, ws, "release", "PR2", strp("https://github.com/o/r/pull/2"))
	addOp(t, ws, "release", "tag", strp("v1.0.0"))
	addOp(t, ws, "delete", "rm", nil)
	routes := ghRoutesPendingFor(1)
	// PR 2 の取得は失敗させる（経路を置かない）
	dir, calls := writeFakeGH(t, routes)
	withGHOnPATH(t, dir)
	port := approvalServer(t, ws)

	code, body := apiGet(t, port, approvalPath)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc struct {
		Approval struct {
			Kind         string  `json:"kind"`
			DoneCriteria *string `json:"done_criteria"`
			Plan         any     `json:"plan"`
			Releases     []struct {
				ID      string  `json:"id"`
				Summary string  `json:"summary"`
				Ref     *string `json:"ref"`
				HeadSHA *string `json:"head_sha"`
			} `json:"releases"`
			Other []struct {
				ID, Kind, Summary string
			} `json:"other_pending_operations"`
			ReleaseHeld bool `json:"release_held"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	a := doc.Approval
	if a.Kind != "completion" || a.DoneCriteria == nil || *a.DoneCriteria != "crit" || a.Plan != nil || !a.ReleaseHeld {
		t.Errorf("approval = %+v", a)
	}
	if len(a.Releases) != 3 {
		t.Fatalf("releases = %+v", a.Releases)
	}
	if h := a.Releases[0].HeadSHA; h == nil || *h != "abc1" {
		t.Errorf("PR1 head = %v", h)
	}
	if a.Releases[1].HeadSHA != nil || a.Releases[2].HeadSHA != nil {
		t.Errorf("failed/non-PR heads must be null: %+v", a.Releases)
	}
	if len(a.Other) != 1 || a.Other[0].Kind != "delete" {
		t.Errorf("other = %+v", a.Other)
	}
	// PR 1（3 回）と PR 2（失敗の 1 回）だけが gh を呼ぶ。非 PR の ref では呼ばない。
	if n := calls(); n != ghCallsPerFetch+1 {
		t.Errorf("gh calls = %d, want %d", n, ghCallsPerFetch+1)
	}
}

func TestApprovalSummaryWithoutGHIs200(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "PR1", strp("https://github.com/o/r/pull/1"))
	t.Setenv("PATH", t.TempDir())
	port := approvalServer(t, ws)
	code, body := apiGet(t, port, approvalPath)
	if code != 200 || !strings.Contains(body, `"head_sha":null`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestApprovalSummaryErrors(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	port := approvalServer(t, ws)
	if code, body := apiGet(t, port, approvalPath); code != 409 || errCode(t, body) != "invalid_transition" {
		t.Errorf("not awaiting: %d %s", code, body)
	}
	if code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-99/approval"); code != 404 || errCode(t, body) != "not_found" {
		t.Errorf("unknown: %d %s", code, body)
	}
}

func showApprovals(t *testing.T, ws string) core.ChallengeDetail {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	d, err := st.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	return *d
}

func currentVersion(t *testing.T, port int) int {
	t.Helper()
	_, body := apiGet(t, port, approvalPath)
	var doc struct {
		Approval struct{ Version int } `json:"approval"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Approval.Version
}

func TestApprovePlan(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	v := currentVersion(t, port)
	code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"approvals", "card", "challenge", "holds", "operations", "plans", "runs", "source_binding"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("response lacks %q (show --json + card): %s", k, body)
		}
	}
	d := showApprovals(t, ws)
	if d.Status != core.StatusInProgress {
		t.Errorf("status = %s, want in_progress", d.Status)
	}
	if len(d.Approvals) != 1 {
		t.Fatalf("approvals = %+v", d.Approvals)
	}
	ap := d.Approvals[0]
	if ap.Kind != core.ApprovalKindPlan || ap.Decision != core.ApprovalDecisionApproved || ap.Channel != "ui" || ap.Verification != "none" || ap.Actor == "" {
		t.Errorf("approval = %+v", ap)
	}
	// 二度目は状態が対象外
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v+1)+`}`); code != 409 || errCode(t, body) != "invalid_transition" {
		t.Errorf("second: %d %s", code, body)
	}
}

func TestApproveCompletionHoldsRelease(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "rel", strp("v1"))
	port := approvalServer(t, ws)
	v := currentVersion(t, port)
	code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	d := showApprovals(t, ws)
	if len(d.Operations) != 1 || d.Operations[0].State != core.OperationStatePending {
		t.Errorf("release must stay pending: %+v", d.Operations)
	}
	last := d.Approvals[len(d.Approvals)-1]
	if last.Kind != core.ApprovalKindCompletion || last.Channel != "ui" || last.Verification != "none" {
		t.Errorf("approval = %+v", last)
	}
	for _, a := range d.Approvals {
		if a.Kind == core.ApprovalKindRelease {
			t.Errorf("release was approved: %+v", a)
		}
	}
}

func TestApproveConflictAndValidation(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	v := currentVersion(t, port)
	before := showApprovals(t, ws)

	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v+5)+`}`); code != 409 || errCode(t, body) != "conflict" {
		t.Errorf("stale: %d %s", code, body)
	}
	for name, b := range map[string]string{
		"missing":   `{}`,
		"null":      `{"version":null}`,
		"string":    `{"version":"1"}`,
		"float":     `{"version":1.5}`,
		"unknown":   `{"version":1,"hold_release":false}`,
		"empty":     ``,
		"trailing":  `{"version":1} {"version":1}`,
		"not-json":  `version=1`,
		"array":     `[1]`,
		"too-large": `{"version":99999999999999999999999}`,
	} {
		if code, body := apiPost(t, port, approvePath, b); code != 400 || errCode(t, body) != "validation_failed" {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	after := showApprovals(t, ws)
	if len(after.Approvals) != len(before.Approvals) || after.Status != before.Status || after.Version != before.Version {
		t.Error("rejected requests changed the store")
	}
}

// AC-161: ① の後に別プロセスの `flywheel edit` で版を実際に動かすと、① の版を添えた approve は
// 409・conflict で拒否され、何も承認しない。
func TestApproveConflictAfterRealEditInAnotherProcess(t *testing.T) {
	for _, status := range []string{"awaiting_plan_approval", "awaiting_completion_approval"} {
		t.Run(status, func(t *testing.T) {
			ws := initWorkspace(t)
			approvalFixture(t, ws, status)
			port := approvalServer(t, ws)
			v := currentVersion(t, port) // ①
			cliJSON(t, ws, "edit", "C-1", "--title", "edited in another process")
			if now := currentVersion(t, port); now == v {
				t.Fatalf("flywheel edit が版を動かさなかった: %d", v)
			}
			code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
			if code != 409 || errCode(t, body) != "conflict" {
				t.Fatalf("approve = %d %s", code, body)
			}
			d := showApprovals(t, ws)
			if len(d.Approvals) != 0 || string(d.Status) != status {
				t.Errorf("何も承認しないはず: status=%s approvals=%+v", d.Status, d.Approvals)
			}
		})
	}
}

func TestApproveRequiresCSRF(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	if code, body := post(t, port, approvePath, "", ""); code != 403 || !strings.Contains(body, "forbidden_origin") {
		t.Errorf("%d %s", code, body)
	}
	if d := showApprovals(t, ws); len(d.Approvals) != 0 {
		t.Error("approved without CSRF")
	}
	if code, _ := apiGet(t, port, approvePath); code != 404 {
		t.Errorf("GET approve = %d, want 404", code)
	}
}

// AC-172〜175: 差し戻し・回答・予算の増額・不可逆操作の承認のエンドポイントは無い。
func TestNoOtherWriteEndpoints(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "rel", strp("v1"))
	port := approvalServer(t, ws)
	for _, p := range []string{
		"/api/v1/workspaces/w/challenges/C-1/reject",
		"/api/v1/workspaces/w/challenges/C-1/answer",
		"/api/v1/workspaces/w/budget",
		"/api/v1/workspaces/w/budget/raise",
		"/api/v1/workspaces/w/operations/OP-1/approve",
		"/api/v1/workspaces/w/challenges/C-1/operations/OP-1/approve",
	} {
		if code, body := apiPost(t, port, p, `{"version":1}`); code != 404 {
			t.Errorf("POST %s = %d %s, want 404", p, code, body)
		}
	}
	d := showApprovals(t, ws)
	if len(d.Approvals) != 0 || d.Operations[0].State != core.OperationStatePending {
		t.Errorf("store changed: %+v", d)
	}
}
