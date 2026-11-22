package server

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 画面の承認の口（POST …/approve）がサイクルを始める決まりの試験（AC-112〜116）。始める処理はサーバへ渡す差し替えで確かめる。

// recordingServer は始める処理の呼び出し（回数・きっかけ）を記録する server を立てる。
func recordingServer(t *testing.T, ws string, launch CycleLaunch) (port int, r *startRecorder) {
	t.Helper()
	r = &startRecorder{launch: launch}
	return startCycleServerWith(t, ws, r.start), r
}

func addUnclassifiedChallenge(t *testing.T, ws string) {
	t.Helper()
	st := openStore(t, ws)
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "other", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
}

func approveCycle(t *testing.T, port int) (string, bool) {
	t.Helper()
	v := currentVersion(t, port)
	code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
	if code != 200 {
		t.Fatalf("approve = %d %s", code, body)
	}
	var doc struct {
		Cycle struct {
			Reason  string `json:"reason"`
			Started bool   `json:"started"`
		} `json:"cycle"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Cycle.Reason, doc.Cycle.Started
}

// AC-112・AC-113・AC-115: 計画の承認が成立し、始められるものがあれば、始める処理が 1 回、きっかけ approve で呼ばれ、cycle.started は真。
func TestApprovePlanStartsTheCycleOnceWithApprove(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	reason, started := approveCycle(t, port)
	if reason != "started" || !started {
		t.Errorf("cycle = %s %v", reason, started)
	}
	if n := atomic.LoadInt32(&r.calls); n != 1 || r.trigger != core.CycleTriggerApprove {
		t.Errorf("calls=%d trigger=%q", n, r.trigger)
	}
}

// AC-116: 反映を保留にした完了の承認が成立し、始められるものが 0 件なら、始める処理を呼ばず cycle.reason は no_work。
func TestApproveCompletionWithoutWorkReportsNoWork(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "rel", strp("v1"))
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	reason, started := approveCycle(t, port)
	if reason != "no_work" || started {
		t.Errorf("cycle = %s %v", reason, started)
	}
	if n := atomic.LoadInt32(&r.calls); n != 0 {
		t.Errorf("calls=%d", n)
	}
}

// 承認が成立しなかったとき（版の食い違い）は、サイクルを始めない。
func TestApproveConflictDoesNotStartTheCycle(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	v := currentVersion(t, port)
	if code, _ := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v+5)+`}`); code != 409 {
		t.Fatalf("code = %d", code)
	}
	if n := atomic.LoadInt32(&r.calls); n != 0 {
		t.Errorf("calls=%d", n)
	}
}

// AC-114・AC-115: 画面の「サイクルを回す」は、きっかけ ui で始め、cycle.started 相当の started は真。
func TestCyclePostKeepsTheUITrigger(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 || body != "{\"started\":true}\n" && body != `{"started":true}` {
		t.Fatalf("POST = %d %q", code, body)
	}
	if n := atomic.LoadInt32(&r.calls); n != 1 || r.trigger != core.CycleTriggerUI {
		t.Errorf("calls=%d trigger=%q", n, r.trigger)
	}
}

// 読んだ後に、始めたプロセスが排他で断られたとき（ほかの操作が先に始めた）は、409 locked。
func TestCyclePostChildRefusedByLockIsConflict(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	port, _ := recordingServer(t, ws, CycleLaunch{AlreadyRunning: true})
	if code, body := apiPost(t, port, cyclePath, ""); code != 409 || errCode(t, body) != "locked" {
		t.Fatalf("POST = %d %s", code, body)
	}
}

// 承認の口で、始めたプロセスが排他で断られたときは、cycle.reason は already_running。
func TestApprovePlanChildRefusedReportsAlreadyRunning(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	addUnclassifiedChallenge(t, ws)
	port, _ := recordingServer(t, ws, CycleLaunch{AlreadyRunning: true})
	if reason, started := approveCycle(t, port); reason != "already_running" || started {
		t.Errorf("cycle = %s %v", reason, started)
	}
}
