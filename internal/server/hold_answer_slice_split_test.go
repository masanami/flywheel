package server

// 画面のボタンの回答の口で、分けるかの保留に答える（docs/features/request-to-plan-s3.md）。AC の番号は親の Issue の受入基準の並び順。

import (
	"context"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

func sliceSplitAnswerWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	if _, err := openStore(t, ws).CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "大きい課題"}); err != nil {
		t.Fatal(err)
	}
	coretest.InsertSliceSplitHold(t, ws, 1, "o/app#1")
	return ws
}

// AC-142・143・145: split も single も 200 で成立し、経路 ui で記録する。split は元の課題を取り下げる。
func TestHoldAnswer_SliceSplitOptions(t *testing.T) {
	for option, want := range map[string]core.Status{"split": core.StatusWithdrawn, "single": core.StatusClassified} {
		t.Run(option, func(t *testing.T) {
			ws := sliceSplitAnswerWorkspace(t)
			port, _ := recordingServer(t, ws, CycleLaunch{Started: true})
			check, _ := readHold(t, port, "H-1")
			if code, body := postAnswer(t, port, holdAnswerPath, check, option); code != 200 {
				t.Fatalf("answer = %d %s", code, body)
			}
			d, err := openStore(t, ws).GetChallenge(context.Background(), "C-1")
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != want {
				t.Errorf("status = %s, want %s", d.Status, want)
			}
			if h := d.Holds[0]; h.AnsweredChannel == nil || *h.AnsweredChannel != "ui" {
				t.Errorf("channel = %v", h.AnsweredChannel)
			}
		})
	}
}

// AC-144: 分けるかの保留でない保留では、同じ識別 split の選択肢でも取り下げは起こらない。
func TestHoldAnswer_SplitOptionOfAnotherHoldDoesNotWithdraw(t *testing.T) {
	ws := sliceSplitAnswerWorkspace(t)
	coretest.Exec(t, ws, `DELETE FROM slice_split`)
	port, _ := recordingServer(t, ws, CycleLaunch{Started: true})
	check, _ := readHold(t, port, "H-1")
	if code, body := postAnswer(t, port, holdAnswerPath, check, "split"); code != 200 {
		t.Fatalf("answer = %d %s", code, body)
	}
	d, err := openStore(t, ws).GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != core.StatusClassified || d.Requirement != nil {
		t.Errorf("status = %s requirement = %+v", d.Status, d.Requirement)
	}
}
