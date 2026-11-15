package core

import (
	"strings"
	"testing"
	"time"
)

// このファイルは、課題が保留の間に契約の run（form: cli）が failed・cancelled で終わった場合の、答えの後の周の挙動
// （docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli`）の「保留の間に `failed`・`cancelled` で終わったとき（Issue #635）」）を、
// 実ストア・偽の ContractClient で固定する。

// 保留の間に failed・cancelled で終わった契約の run は、答えの後の周で start も resume も呼ばず、新しい run も始めない。
// 答えは保留に残り、課題は「保留の間に run が終わった」ことを添えた新しい保留で人間対応待ちに戻る。
// tickingClock は、呼ぶたびに 1 秒進む時計をストアへ入れる（保留・終わり・回答が同じミリ秒に重ならないようにする）。
func tickingClock(f *contractFixture) {
	cur := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { cur = cur.Add(time.Second); return cur }
}

func TestHeldStop_EndedDuringHoldIsReportedAfterTheAnswerWithoutRestart(t *testing.T) {
	for _, st := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		t.Run(string(st), func(t *testing.T) {
			f := newContractFixture(t)
			tickingClock(f)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil),
				contractOut(st, nil, func(s *ContractStatus) { s.Summary = "要約XYZ" })}
			f.poll(t)
			f.poll(t)
			if got := f.status(t, f.cid); got != StatusAwaitingHuman {
				t.Fatalf("status = %s, want awaiting_human", got)
			}
			f.delegationCycle(t)
			f.answerLatestHold(t, f.cid, "ANSWER-LATE")
			f.delegationCycle(t)
			f.delegationCycle(t)

			if len(f.client.starts) != 1 || len(f.client.resumes) != 0 {
				t.Fatalf("starts = %d resumes = %v, want 1 and none", len(f.client.starts), f.client.resumes)
			}
			d := f.detail(t, f.cid)
			if d.Status != StatusAwaitingHuman {
				t.Fatalf("status = %s, want awaiting_human（失敗として写す）", d.Status)
			}
			if len(d.Holds) != 2 {
				t.Fatalf("holds = %d, want 2（答えを残した保留と、失敗の写し）", len(d.Holds))
			}
			if d.Holds[0].Answer == nil || !strings.Contains(*d.Holds[0].Answer, "ANSWER-LATE") {
				t.Fatalf("first hold answer = %v, want kept", d.Holds[0].Answer)
			}
			q := d.Holds[1].Question
			if !d.Holds[1].Open() || !strings.Contains(q, "要約XYZ") || !strings.Contains(q, "保留の間に") {
				t.Fatalf("second hold = %+v, want open with the summary and 保留の間に", d.Holds[1])
			}
		})
	}
}

// 着手中に終わり、保留を作る前にプロセスが止まった契約の run を掃引（HoldUnrecordedContractStops）が保留にするときは、
// 保留の間に終わった旨を添えない。回答なしで閉じた古い保留があっても同じ。
func TestHeldStop_SweepOfAStopEndedWhileInProgressHasNoDuringHoldNote(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE contract_run SET state = 'failed', summary = '要約XYZ', ended_at = ?`, formatTimestamp(f.s.currentTime())); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	d := f.detail(t, f.cid)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "要約XYZ") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
	if strings.Contains(d.Holds[0].Question, "保留の間に") {
		t.Fatalf("question = %q, want no during-hold note", d.Holds[0].Question)
	}
}
