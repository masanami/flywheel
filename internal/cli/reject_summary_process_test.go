package cli

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは受入基準 41（端末ありの reject <C-ID> --reason <r> は、確認の
// 入力を求める前に、対応する承認と同じ要約と入力された理由を表示する）を、
// 計画承認待ち・完了承認待ちの両方の課題について、approve の要約と実際に
// 並べて比べることで確かめる（#33。既存の approval_process_test.go は要約に
// 特定の文字列が含まれることだけを見ていた）。承認待ちの release がある完了の
// 承認では、受入基準 41 に明記した例外（D12: 振り分けの 2 行は今回の操作の
// 結果に従う）のため、approve・reject それぞれの要約を期待する全文と比べる
// （#40）。
//
// approve と reject を同じ課題へ続けて実行できない（どちらも状態を進める）
// ため、同じ手順で作った 2 つのワークスペースで 1 回ずつ実行する。

// ttySummaryBeforePrompt は args の子プロセスを疑似端末つきで起動し、確認の
// プロンプトが表示されるまで待ってから、プロンプトより前に表示された内容
// （＝要約）を返す。その後に id を入力して子プロセスを終了 0 まで進める。
// 入力のエコーが要約へ混ざらないよう、入力はプロンプトを観測してから書く。
// 疑似端末が改行を CRLF へ変えるため、LF にそろえて返す。
func ttySummaryBeforePrompt(t *testing.T, args []string, id string) string {
	t.Helper()
	cmd, stdout, stderr := newChildCmd(args)
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	// 失敗で抜けるときも子を止めて Wait を合流させてから stdout・stderr を
	// 読む（Wait 前に読むと、コピー中のゴルーチンとの競合になる）。
	stopChild := func() {
		_ = cmd.Process.Kill()
		<-exited
	}

	prompt := confirmPrompt(id)
	var shown string
	deadline := time.After(10 * time.Second)
	for {
		out := drainSnapshot(drain)
		if i := strings.Index(out, prompt); i >= 0 {
			shown = out[:i]
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("args=%v: child exited before the prompt (err=%v stdout=%q stderr=%q pty=%q)", args, err, stdout.String(), stderr.String(), out)
		case <-deadline:
			stopChild()
			t.Fatalf("args=%v: prompt %q not shown within timeout (stderr=%q pty=%q)", args, prompt, stderr.String(), out)
		case <-time.After(10 * time.Millisecond):
		}
	}

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		stopChild()
		t.Fatalf("write to master: %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("cmd.Wait: %v", err)
			}
			t.Fatalf("args=%v exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", args, exitErr.ExitCode(), stdout.String(), stderr.String(), drain.waitDone(5*time.Second))
		}
	case <-time.After(10 * time.Second):
		stopChild()
		t.Fatalf("args=%v: child did not exit after the confirmation (stderr=%q)", args, stderr.String())
	}
	return strings.ReplaceAll(shown, "\r\n", "\n")
}

// approveAndRejectSummaries は setup で同じ状態のワークスペースを 2 つ作り、
// 片方で approve、もう片方で reject --reason reason を実行して、それぞれが
// 確認前に表示した要約を返す。
func approveAndRejectSummaries(t *testing.T, setup func(t *testing.T, ws string) string, reason string) (approve, reject string) {
	t.Helper()
	approveWS := initializedWorkspace(t)
	approveID := setup(t, approveWS)
	rejectWS := initializedWorkspace(t)
	rejectID := setup(t, rejectWS)
	if approveID != rejectID {
		t.Fatalf("setup: ids differ (%s vs %s)", approveID, rejectID)
	}
	approve = ttySummaryBeforePrompt(t, []string{"approve", "--workspace", approveWS, "--json", approveID}, approveID)
	reject = ttySummaryBeforePrompt(t, []string{"reject", "--workspace", rejectWS, "--json", "--reason", reason, rejectID}, rejectID)
	return approve, reject
}

// requireRejectIsApprovePlusReason は、reject の要約が approve の要約をそのまま
// 含み、その後に理由の 1 行だけを足したものであることを確かめる（要約の後の
// 空行は確認のプロンプトとの区切りなので除いて比べる）。
func requireRejectIsApprovePlusReason(t *testing.T, approve, reject, reason string) {
	t.Helper()
	approveBody := strings.TrimRight(approve, "\n")
	rejectBody := strings.TrimRight(reject, "\n")
	if !strings.HasPrefix(rejectBody, approveBody+"\n") {
		t.Fatalf("reject summary does not start with the approval summary:\napprove=%q\nreject=%q", approve, reject)
	}
	added := strings.TrimPrefix(rejectBody, approveBody+"\n")
	if strings.Contains(added, "\n") || !strings.Contains(added, reason) {
		t.Errorf("reject summary adds %q, want only one line containing the reason %q", added, reason)
	}
}

// requireSummaryContains は、比べる要約が意味のある中身を持つこと（空同士の
// 一致で通らないこと）を確かめる。
func requireSummaryContains(t *testing.T, summary string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary = %q, want it to contain %q", summary, want)
		}
	}
}

func TestReject_PlanApproval_ShowsTheApprovalSummaryPlusReason(t *testing.T) {
	const reason = "distinctive plan rejection reason"
	approve, reject := approveAndRejectSummaries(t, func(t *testing.T, ws string) string {
		created := runJSON(t, ws, "create", "--title", "distinctive-plan-title")
		id := created["challenge"].(map[string]any)["id"].(string)
		runJSON(t, ws, "classify", id, "--priority", "P0")
		runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "distinctive plan body"))
		return id
	}, reason)

	requireSummaryContains(t, approve, "C-1", "distinctive-plan-title", "承認の種類:  計画", "計画 v1", "distinctive plan body")
	requireRejectIsApprovePlusReason(t, approve, reject, reason)
}

func TestReject_CompletionApproval_ShowsTheApprovalSummaryPlusReason(t *testing.T) {
	const reason = "distinctive completion rejection reason"
	approve, reject := approveAndRejectSummaries(t, func(t *testing.T, ws string) string {
		created := runJSON(t, ws, "create", "--title", "distinctive-completion-title", "--done-criteria", "distinctive done criteria")
		id := created["challenge"].(map[string]any)["id"].(string)
		// release 以外の不可逆操作は、承認でも差し戻しでも「同時には承認
		// されない」の欄に出る（release の扱いは下の別テスト）。
		runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "distinctive delete op")
		coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")
		return id
	}, reason)

	requireSummaryContains(t, approve, "C-1", "distinctive-completion-title", "承認の種類:  完了", "distinctive done criteria", "distinctive delete op")
	requireRejectIsApprovePlusReason(t, approve, reject, reason)
}

// TestReject_CompletionApprovalWithPendingRelease_MatchesApprovalWithD12Reassignment
// は、受入基準 41 の「対応する承認と同じ要約と入力された理由を表示する」を、
// 承認待ちの release がある完了の承認についても文字どおりの一致で確かめる。
// docs/features/m1-core.md の §承認・受入基準 41 に明記された例外（D12:
// 差し戻しでは release も承認されないため、「同時に承認される本番反映」
// 「同時には承認されない不可逆操作」の 2 行は今回の操作の結果に従う）を踏まえ、
// approve の要約全体・reject の要約全体をそれぞれ期待する文字列と完全一致で
// 比較する（Issue #40。以前は 2 行を比較から除いて部分一致だけを見ていた）。
func TestReject_CompletionApprovalWithPendingRelease_MatchesApprovalWithD12Reassignment(t *testing.T) {
	const reason = "distinctive release rejection reason"
	approve, reject := approveAndRejectSummaries(t, func(t *testing.T, ws string) string {
		created := runJSON(t, ws, "create", "--title", "distinctive-release-title", "--done-criteria", "distinctive done criteria")
		id := created["challenge"].(map[string]any)["id"].(string)
		releaseOpID := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "distinctive release op")["operation"].(map[string]any)["id"].(string)
		deleteOpID := runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "distinctive delete op")["operation"].(map[string]any)["id"].(string)
		// releaseOpID/deleteOpID は下の期待値に OP-1/OP-2 として埋め込んで
		// いるため、想定と違えばここで早期に失敗させる（新しいワークスペース
		// での 1 件目・2 件目の登録。採番が 1 から始まることは op add の
		// テストが固定している）。
		if releaseOpID != "OP-1" || deleteOpID != "OP-2" {
			t.Fatalf("op ids = %s, %s, want OP-1, OP-2", releaseOpID, deleteOpID)
		}
		coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")
		return id
	}, reason)

	// 要約の後の空行は確認のプロンプトとの区切り（ttyconfirm.go: "%s\n\n%s"）
	// なので、比較の前に末尾の改行を1本にそろえる。
	approveBody := strings.TrimRight(approve, "\n") + "\n"
	rejectBody := strings.TrimRight(reject, "\n") + "\n"

	const wantApprove = "課題 ID:     C-1\n" +
		"タイトル:    distinctive-release-title\n" +
		"承認の種類:  完了\n" +
		"完了条件:    distinctive done criteria\n" +
		"同時に承認される本番反映 (release): OP-1(release) distinctive release op\n" +
		"同時には承認されない不可逆操作:     OP-2(delete) distinctive delete op\n"
	if approveBody != wantApprove {
		t.Errorf("approve summary = %q, want %q", approveBody, wantApprove)
	}

	// reject は、release の振り分けの 2 行だけを D12 の結果（release も
	// 未承認のまま残る）に置き換え、末尾に理由の 1 行を足したものになる。
	const wantReject = "課題 ID:     C-1\n" +
		"タイトル:    distinctive-release-title\n" +
		"承認の種類:  完了\n" +
		"完了条件:    distinctive done criteria\n" +
		"同時に承認される本番反映 (release): (無し)\n" +
		"同時には承認されない不可逆操作:     OP-1(release) distinctive release op, OP-2(delete) distinctive delete op\n" +
		"理由:        " + reason + "\n"
	if rejectBody != wantReject {
		t.Errorf("reject summary = %q, want %q", rejectBody, wantReject)
	}
}
