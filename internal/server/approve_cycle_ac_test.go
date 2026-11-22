package server

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 受入基準の穴埋め（Issue #1037。番号は #988 本文の「## 受入基準」の並びの順）。画面の承認の口（POST …/approve）の側。

// #988 AC-89: 始める処理がエラーを返しても、承認の記録そのものは残る（POST …/approve）。応答は 200 で、cycle.reason は unavailable。
func TestApprove_AC89_StarterErrorKeepsTheApprovalRecord(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	port, r := recordingServer(t, ws, CycleLaunch{})
	r.err = errors.New("claude: executable file not found")

	reason, started := approveCycle(t, port)

	if n := atomic.LoadInt32(&r.calls); n != 1 {
		t.Fatalf("starter calls = %d, want 1 (the error path must be the one under test)", n)
	}
	if reason != "unavailable" || started {
		t.Errorf("cycle = %s %v, want unavailable/false", reason, started)
	}
	d := showApprovals(t, ws)
	if d.Status != core.StatusInProgress || len(d.Approvals) != 1 {
		t.Errorf("status = %s approvals = %+v, want in_progress with the approval kept", d.Status, d.Approvals)
	}
}

// #988 AC-94: 排他の heartbeat が回収の時間を過ぎているときの承認は、走っているサイクルが無い扱いで、始める処理が 1 回、きっかけ approve で呼ばれる。
func TestApprove_AC94_StaleLockStillCallsTheStarterOnce(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	id := coretest.InsertCycle(t, ws, "cron", 10_000_000, "2026-01-01T00:00:00.000Z")
	coretest.InsertLock(t, ws, id, 1, "h", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z")
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})

	reason, started := approveCycle(t, port)

	if n := atomic.LoadInt32(&r.calls); n != 1 || r.trigger != core.CycleTriggerApprove {
		t.Fatalf("starter calls = %d trigger = %q, want 1 with approve", n, r.trigger)
	}
	if reason != "started" || !started {
		t.Errorf("cycle = %s %v, want started/true", reason, started)
	}
}

// #988 AC-95（画面の口の側）: 承認が成立しなかった（版の食い違い）とき、始める処理は 0 回で、応答に cycle は出ない。
func TestApprove_AC95_VersionMismatchCallsNoStarterAndReturnsNoCycle(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	v := currentVersion(t, port)

	code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v+5)+`}`)

	if code != 409 {
		t.Fatalf("code = %d %s, want 409", code, body)
	}
	if n := atomic.LoadInt32(&r.calls); n != 0 {
		t.Errorf("starter calls = %d, want 0", n)
	}
	if strings.Contains(body, `"cycle"`) {
		t.Errorf("the error body must not carry a cycle: %s", body)
	}
	if d := showApprovals(t, ws); len(d.Approvals) != 0 {
		t.Errorf("approvals = %+v, want none", d.Approvals)
	}
}
