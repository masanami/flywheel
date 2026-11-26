package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 下書きづくりの保留への、画面の保留の回答の口（本文は check と option だけ）。テストの名前の AC の番号は、Issue #1354 の受け持ち。

func specHoldWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	if _, err := openStore(t, ws).CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "課題 1", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	coretest.InsertSpecDraftHold(t, ws, 1, "spec_question",
		`[{"question":"SQ-1","options":[{"label":"a1"},{"label":"a2"}],"recommended":2,"reason":"r"},{"question":"SQ-2","options":[{"label":"b1"},{"label":"b2"}],"recommended":1,"reason":"r"}]`,
		`[{"id":"answer","label":"推奨のまま進める"},{"id":"proceed","label":"いまの下書きで計画へ進む"},{"id":"skip","label":"仕様の文書なしで計画する"}]`)
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	return ws
}

func specDraftOf(t *testing.T, ws string) *core.SpecDraft {
	t.Helper()
	d, err := openStore(t, ws).GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	return d.SpecDraft
}

func TestHoldAnswer_SpecDraftAnswerRecordsEveryRecommendation(t *testing.T) { // AC-329
	ws := specHoldWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{Started: true})
	check, _ := readHold(t, port, "H-1")
	if code, body := postAnswer(t, port, holdAnswerPath, check, "answer"); code != http.StatusOK {
		t.Fatalf("answer = %d %s", code, body)
	}
	got := specDraftOf(t, ws).Answers
	if len(got) != 2 || got[0].Answer != "a2" || got[1].Answer != "b1" || got[0].By != "owner" {
		t.Errorf("answers = %+v", got)
	}
}

func TestHoldAnswer_SpecDraftRejectsChoicesInTheBody(t *testing.T) { // AC-330
	ws := specHoldWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	code, body := apiPost(t, port, holdAnswerPath, `{"check":"`+check+`","option":"answer","choices":[{"question":1,"choice":1}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("answer = %d %s", code, body)
	}
	if !holdState(t, ws, "C-1").Open() {
		t.Error("hold was closed")
	}
}

func TestHoldAnswer_SpecDraftSkip(t *testing.T) { // AC-333
	ws := specHoldWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	if code, body := postAnswer(t, port, holdAnswerPath, check, "skip"); code != http.StatusOK {
		t.Fatalf("answer = %d %s", code, body)
	}
	if got := specDraftOf(t, ws).State; got != core.SpecDraftStateSkipped {
		t.Errorf("state = %s", got)
	}
}
