package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S1 の「承認の時点の head」（AC-1〜AC-15・AC-21・AC-22・AC-26〜AC-32・AC-34〜AC-37・
// AC-104・AC-105）を、疑似端末の子プロセス・--relayed・偽の gh で検証する。core の公開 API を直接呼ぶ検証は
// internal/core/approval_head_test.go にある。

type completionFixture struct {
	ws  string
	id  string
	ops []string // release の ID（refs の順）
}

// newCompletionFixture は完了確認待ちの課題に、refs それぞれを参照に持つ release を足す。偽の gh は heads の
// PR の head を返す（値が空の PR は取得に失敗する）。宣言のリポジトリは o/r。
func newCompletionFixture(t *testing.T, heads map[int]string, refs ...string) *completionFixture {
	t.Helper()
	ws := initializedWorkspace(t)
	declareReleaseRepo(t, ws)
	fakeGHReleaseHeads(t, heads)
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "it works")
	f := &completionFixture{ws: ws, id: created["challenge"].(map[string]any)["id"].(string)}
	for i, ref := range refs {
		f.ops = append(f.ops, addRelease(t, ws, f.id, fmt.Sprintf("ship %d", i), ref))
	}
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, f.id), "awaiting_completion_approval")
	return f
}

func (f *completionFixture) relayed(t *testing.T, target string) map[string]any {
	t.Helper()
	return runJSON(t, f.ws, "approve", target, "--relayed", "--session", "s", "--quote", "q")
}

func (f *completionFixture) state(t *testing.T, op string) string {
	t.Helper()
	return findOperation(t, runJSON(t, f.ws, "show", f.id), op)["state"].(string)
}

func (f *completionFixture) challengeStatus(t *testing.T) string {
	t.Helper()
	return statusOfChallenge(t, f.ws, f.id)
}

// verified は直前の委譲の run の成果物に ref の行を足し、検証の head を設定する。
func (f *completionFixture) verified(t *testing.T, ref, head string) {
	t.Helper()
	runID := coretest.InsertRun(t, f.ws, coretest.InsertRunInput{
		ChallengeID: challengeIDToInternalID(t, f.id), Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
		EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 5_000_000, BudgetBucket: "impl",
	})
	coretest.InsertRunArtifact(t, f.ws, runID, "pr", ref, "open", "main")
	if head != "" {
		coretest.SetRunArtifactVerifiedHead(t, f.ws, runID, ref, head)
	}
}

func jsonList(t *testing.T, doc map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := doc[key].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func opNumber(op string) int64 {
	var n int64
	_, _ = fmt.Sscanf(op, "OP-%d", &n)
	return n
}

func (f *completionFixture) approvedHead(t *testing.T, op string) (string, bool) {
	t.Helper()
	return coretest.ApprovalHeadSHA(t, f.ws, opNumber(op))
}

// --- 端末の要約（AC-1・AC-2・AC-7〜AC-9）と --relayed の出力（AC-3・AC-4・AC-10） ---

func TestApproveHead_CompletionSummaryShowsFullAndShortHeadsBeforeTheConfirmation(t *testing.T) {
	h1, h2 := releaseHead(1, 0), releaseHead(2, 0)
	f := newCompletionFixture(t, map[int]string{1: h1, 2: h2}, releasePRURL(1), releasePRURL(2))
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.id}, f.id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	prompt := strings.Index(ptyOutput, confirmPrompt(f.id))
	for _, h := range []string{h1, h2} {
		if i := strings.Index(ptyOutput, h); i < 0 || i > prompt {
			t.Errorf("full head %s must be shown before the confirmation prompt (pos %d, prompt %d)", h, i, prompt)
		}
		if !strings.Contains(ptyOutput, h[:12]) {
			t.Errorf("short head %s missing", h[:12])
		}
	}
	for i, op := range f.ops {
		if got, ok := f.approvedHead(t, op); !ok || got != []string{h1, h2}[i] {
			t.Errorf("%s head_sha = %q (found %v), want %s", op, got, ok, []string{h1, h2}[i])
		}
	}
}

func TestApproveHead_RelayedCompletionRecordsHeadsAndPrintsReleases(t *testing.T) {
	h1, h2 := releaseHead(1, 0), releaseHead(2, 0)
	f := newCompletionFixture(t, map[int]string{1: h1, 2: h2}, releasePRURL(1), releasePRURL(2))
	doc := f.relayed(t, f.id)
	rels := jsonList(t, doc, "releases")
	if len(rels) != 2 || rels[0]["operation_id"] != f.ops[0] || rels[0]["head_sha"] != h1 || rels[1]["operation_id"] != f.ops[1] || rels[1]["head_sha"] != h2 {
		t.Errorf("releases = %v", rels)
	}
	if ex := jsonList(t, doc, "excluded_releases"); len(ex) != 0 {
		t.Errorf("excluded_releases = %v, want none", ex)
	}
	for i, op := range f.ops {
		if got, ok := f.approvedHead(t, op); !ok || got != []string{h1, h2}[i] {
			t.Errorf("%s head_sha = %q (found %v)", op, got, ok)
		}
	}
}

// --- 外す理由（AC-5・AC-6・AC-11〜AC-14） ---

func TestApproveHead_CompletionLeavesUnapprovableReleasesPendingAndStillCompletes(t *testing.T) {
	good := releaseHead(1, 0)
	f := newCompletionFixture(t, map[int]string{1: good, 2: ""}, releasePRURL(1), releasePRURL(2), "https://example.com/not-a-pr", "https://github.com/x/undeclared/pull/3")
	doc := f.relayed(t, f.id)
	if got := f.challengeStatus(t); got != "done" {
		t.Errorf("challenge status = %s, want done (the completion approval stands)", got)
	}
	want := map[string]string{f.ops[1]: "head_unavailable", f.ops[2]: "not_a_pr_url", f.ops[3]: "repo_not_declared"}
	ex := jsonList(t, doc, "excluded_releases")
	if len(ex) != 3 {
		t.Fatalf("excluded_releases = %v", ex)
	}
	for _, e := range ex {
		if want[e["operation_id"].(string)] != e["reason"] {
			t.Errorf("excluded %v, want reason %s", e, want[e["operation_id"].(string)])
		}
	}
	if f.state(t, f.ops[0]) != "approved" {
		t.Errorf("%s must be approved", f.ops[0])
	}
	for op := range want {
		if f.state(t, op) != "pending" {
			t.Errorf("%s must stay pending", op)
		}
		if _, ok := f.approvedHead(t, op); ok {
			t.Errorf("%s must have no approval row", op)
		}
	}
}

func TestApproveHead_SummaryNamesTheExcludedReasonsAndSaysTheyAreNotApproved(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: ""}, releasePRURL(1), "https://example.com/not-a-pr", "https://github.com/x/undeclared/pull/3")
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.id}, f.id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	for _, want := range []string{"head_unavailable", "not_a_pr_url", "repo_not_declared", "同時に承認されません"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("summary lacks %q: %q", want, ptyOutput)
		}
	}
	assertOperationListing(t, ptyOutput, approvedReleasesPrefix, nil, f.ops)
}

// --- 単独の承認（AC-15・AC-21・AC-26・AC-27・AC-104） ---

func TestApproveHead_OperationTerminalAndRelayedRecordTheFetchedHead(t *testing.T) {
	h1, h2 := releaseHead(1, 0), releaseHead(2, 0)
	f := newCompletionFixture(t, map[int]string{1: h1, 2: h2}, releasePRURL(1), releasePRURL(2))
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.ops[0]}, f.ops[0])
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	if !strings.Contains(ptyOutput, h1) || !strings.Contains(ptyOutput, h1[:12]) {
		t.Errorf("summary lacks the head: %q", ptyOutput)
	}
	if got, ok := f.approvedHead(t, f.ops[0]); !ok || got != h1 {
		t.Errorf("terminal head_sha = %q (found %v)", got, ok)
	}
	f.relayed(t, f.ops[1])
	if got, ok := f.approvedHead(t, f.ops[1]); !ok || got != h2 {
		t.Errorf("relayed head_sha = %q (found %v)", got, ok)
	}
}

func TestApproveHead_OperationThatCannotBeApprovedEndsWithUpstreamUnavailableAndWritesNothing(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: ""}, releasePRURL(1), "https://example.com/not-a-pr", "https://github.com/x/undeclared/pull/3")
	for i, reason := range []string{"head_unavailable", "not_a_pr_url", "repo_not_declared"} {
		op := f.ops[i]
		// 端末: 確認の入力を求める前に終わる。
		code, ptyOutput, _, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", op}, op)
		if code != 1 || readErrorCode(t, stderr) != string(CodeUpstreamUnavailable) || !strings.Contains(stderr, reason) {
			t.Errorf("%s terminal: exit=%d stderr=%s, want exit 1 upstream_unavailable naming %s", op, code, stderr, reason)
		}
		if strings.Contains(ptyOutput, confirmPrompt(op)) {
			t.Errorf("%s: the confirmation must not be asked", op)
		}
		// --relayed
		requireErrorCode(t, []string{"approve", op, "--relayed", "--session", "s", "--quote", "q", "--workspace", f.ws}, 1, CodeUpstreamUnavailable)
		if f.state(t, op) != "pending" {
			t.Errorf("%s must stay pending", op)
		}
	}
	if n := coretest.CountReleaseApprovals(t, f.ws); n != 0 {
		t.Errorf("release approval rows = %d, want 0", n)
	}
}

// --- 検証の head（AC-28〜AC-37・AC-105） ---

func TestApproveHead_VerifiedHeadMismatchExcludesAndMatchOrNoRecordApproves(t *testing.T) {
	h1, h2, h3 := releaseHead(1, 0), releaseHead(2, 0), releaseHead(3, 0)
	f := newCompletionFixture(t, map[int]string{1: h1, 2: h2, 3: h3}, releasePRURL(1), releasePRURL(2)+"/files", releasePRURL(3))
	f.verified(t, releasePRURL(1), releaseHead(1, 1)) // 異なる → 外す
	f.verified(t, releasePRURL(2), h2)                // /files の参照でも同じ PR → 一致
	// PR 3 は成果物の行が無い → 記録なし

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.id}, f.id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	if !strings.Contains(ptyOutput, "verified_head_mismatch") || !strings.Contains(ptyOutput, "検証の head の記録が無い") {
		t.Errorf("summary must name verified_head_mismatch and the missing record: %q", ptyOutput)
	}
	if f.state(t, f.ops[0]) != "pending" || f.state(t, f.ops[1]) != "approved" || f.state(t, f.ops[2]) != "approved" {
		t.Errorf("states: %s %s %s", f.state(t, f.ops[0]), f.state(t, f.ops[1]), f.state(t, f.ops[2]))
	}
}

func TestApproveHead_RelayedJSONNamesVerifiedHeadMismatch(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	f.verified(t, releasePRURL(1), releaseHead(1, 1))
	ex := jsonList(t, f.relayed(t, f.id), "excluded_releases")
	if len(ex) != 1 || ex[0]["operation_id"] != f.ops[0] || ex[0]["reason"] != "verified_head_mismatch" {
		t.Errorf("excluded_releases = %v", ex)
	}
}

// 最新の行（id が大きい）の値と比べる。最新の行が NULL なら、小さい方の値と比べる。
func TestApproveHead_ComparesWithTheLatestNonNullVerifiedHead(t *testing.T) {
	h := releaseHead(1, 0)
	f := newCompletionFixture(t, map[int]string{1: h}, releasePRURL(1))
	f.verified(t, releasePRURL(1), releaseHead(1, 1)) // 古い行: 異なる
	f.verified(t, releasePRURL(1), h)                 // 新しい行: 一致
	f.verified(t, releasePRURL(1), "")                // 最新の行は NULL → 無視
	if ex := jsonList(t, f.relayed(t, f.id), "excluded_releases"); len(ex) != 0 {
		t.Errorf("excluded_releases = %v, want none (the latest non-NULL row matches)", ex)
	}
	if f.state(t, f.ops[0]) != "approved" {
		t.Errorf("must be approved")
	}
}

func TestApproveHead_OperationWithVerifiedHeadMismatchIsUpstreamUnavailable(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	f.verified(t, releasePRURL(1), releaseHead(1, 1))
	code, _, _, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.ops[0]}, f.ops[0])
	if code != 1 || readErrorCode(t, stderr) != string(CodeUpstreamUnavailable) || !strings.Contains(stderr, "verified_head_mismatch") {
		t.Errorf("terminal: exit=%d stderr=%s", code, stderr)
	}
	requireErrorCode(t, []string{"approve", f.ops[0], "--relayed", "--session", "s", "--quote", "q", "--workspace", f.ws}, 1, CodeUpstreamUnavailable)
}

func TestApproveHead_OperationWithNoVerifiedRecordIsApprovedAndSaysSo(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.ops[0]}, f.ops[0])
	if code != 0 || !strings.Contains(ptyOutput, "検証の head の記録が無い") {
		t.Errorf("exit=%d stderr=%s stdout=%s pty=%q", code, stderr, stdout, ptyOutput)
	}
}

// --- ① と ② の間の変化（AC-23・AC-24）と ② が head を取り直さないこと（AC-30） ---

func startCompletionApproval(t *testing.T, f *completionFixture) (waitFinish func() (code int, stderr string), confirm func()) {
	t.Helper()
	cmd, _, stderr := newChildCmd([]string{"approve", "--workspace", f.ws, "--json", f.id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	drain := startPTYDrain(master)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(drainSnapshot(drain), confirmPrompt(f.id)) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the confirmation prompt")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() (int, string) {
			code := waitChild(t, cmd, 10*time.Second)
			drain.waitDone(5 * time.Second)
			return code, stderr.String()
		}, func() {
			if _, err := master.Write([]byte(f.id + "\n")); err != nil {
				t.Fatalf("write to master: %v", err)
			}
		}
}

func TestApproveHead_ReleaseAddedBetweenSummaryAndConfirmationIsConflict(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	wait, confirm := startCompletionApproval(t, f)
	addRelease(t, f.ws, f.id, "late", releasePRURL(1))
	confirm()
	code, stderr := wait()
	if code != 1 || readErrorCode(t, stderr) != string(CodeConflict) {
		t.Fatalf("exit=%d stderr=%s, want conflict", code, stderr)
	}
	if f.challengeStatus(t) != "awaiting_completion_approval" {
		t.Errorf("nothing may be written")
	}
}

func TestApproveHead_ExcludedReleaseRejectedBetweenSummaryAndConfirmationIsConflict(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1), "https://example.com/not-a-pr")
	wait, confirm := startCompletionApproval(t, f)
	runConfirmedRejectOp(t, f.ws, f.ops[1])
	confirm()
	code, stderr := wait()
	if code != 1 || readErrorCode(t, stderr) != string(CodeConflict) {
		t.Fatalf("exit=%d stderr=%s, want conflict", code, stderr)
	}
}

func runConfirmedRejectOp(t *testing.T, ws, op string) {
	t.Helper()
	runJSON(t, ws, "reject", op, "--reason", "no", "--relayed", "--session", "s", "--quote", "q")
}

// ② は ① で取得した head を残す: ① の後に GitHub の head が変わっても、承認の記録は ① の値。
func TestApproveHead_TheRecordedHeadIsTheOneFetchedAtTheSummary(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	// 偽の gh を、応答をファイルから読むものに差し替える（PATH の先頭）。
	dir := t.TempDir()
	resp := filepath.Join(dir, "resp.json")
	body := func(head string) string {
		return fmt.Sprintf(`{"html_url":%q,"title":"PR","state":"open","merged_at":null,"base":{"ref":"main"},"head":{"sha":%q}}`, releasePRURL(1), head)
	}
	if err := os.WriteFile(resp, []byte(body(releaseHead(1, 0))), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n*check-runs*) echo '{\"total_count\":0,\"check_runs\":[]}';;\n*status*) echo '{\"state\":\"success\",\"total_count\":0,\"statuses\":[]}';;\n*) cat " + resp + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	wait, confirm := startCompletionApproval(t, f)
	if err := os.WriteFile(resp, []byte(body(releaseHead(1, 5))), 0o644); err != nil {
		t.Fatal(err)
	}
	confirm()
	if code, stderr := wait(); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got, ok := f.approvedHead(t, f.ops[0]); !ok || got != releaseHead(1, 0) {
		t.Errorf("head_sha = %q, want the head fetched at the summary %s", got, releaseHead(1, 0))
	}
}

// --- merged_unapproved の承認し直し（AC-104 の一部） ---

func TestApproveHead_OperationRevokedAsMergedUnapprovedIsRejectedBeforeTheConfirmation(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	coretest.InsertOperationExecution(t, f.ws, opNumber(f.ops[0]), coretest.ExecutionAttempt{
		StartedAt: "2026-10-22T00:00:00.000Z", EndedAt: "2026-10-22T00:00:01.000Z", Result: "merged_unapproved",
	})
	code, ptyOutput, _, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.ops[0]}, f.ops[0])
	if code != 1 || readErrorCode(t, stderr) != string(CodeVerificationRejected) {
		t.Errorf("exit=%d stderr=%s, want verification_rejected", code, stderr)
	}
	if strings.Contains(ptyOutput, confirmPrompt(f.ops[0])) {
		t.Errorf("the confirmation must not be asked: %q", ptyOutput)
	}
}

// --- 完了した課題の pending の release は approve <OP-ID> で承認できる（AC-105）。UI の経路は release を承認しない（AC-22） ---

func TestApproveHead_DoneChallengesPendingReleaseCanBeApprovedByOperationID(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	runJSON(t, f.ws, "approve", f.id, "--hold-release", "--relayed", "--session", "s", "--quote", "q")
	if f.challengeStatus(t) != "done" || f.state(t, f.ops[0]) != "pending" {
		t.Fatalf("setup: challenge %s release %s", f.challengeStatus(t), f.state(t, f.ops[0]))
	}
	f.relayed(t, f.ops[0])
	if got, ok := f.approvedHead(t, f.ops[0]); !ok || got != releaseHead(1, 0) {
		t.Errorf("head_sha = %q (found %v)", got, ok)
	}
}

func TestApproveHead_HoldReleaseCreatesNoReleaseApproval(t *testing.T) {
	f := newCompletionFixture(t, map[int]string{1: releaseHead(1, 0)}, releasePRURL(1))
	doc := runJSON(t, f.ws, "approve", f.id, "--hold-release", "--relayed", "--session", "s", "--quote", "q")
	if n := coretest.CountReleaseApprovals(t, f.ws); n != 0 {
		t.Errorf("release approval rows = %d, want 0", n)
	}
	if _, ok := doc["releases"]; ok {
		t.Errorf("--hold-release must not print releases: %v", doc)
	}
}
