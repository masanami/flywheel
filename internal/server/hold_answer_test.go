package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4d-2 の回答の口（POST …/challenges/{id}/holds/{hold}/answer。AC-34〜AC-46、AC-47・48・51・52）。

const (
	answerOptsJSON = `[{"id":"a","label":"案 A","description":"説明 A"},{"id":"b","label":"案 B","description":""}]`
	holdAnswerPath = "/api/v1/workspaces/w/challenges/C-1/holds/H-1/answer"
	otherHoldPath  = "/api/v1/workspaces/w/challenges/C-1/holds/H-2/answer"
	overLimitOpts  = `[{"id":"add","label":"足す","description":"上限が上がる"},{"id":"skip","label":"足さずに進める","description":""},{"id":"replan","label":"計画を作り直す","description":""}]`
)

// answerWorkspace は、選択肢つきの保留 H-1 を持つ人間対応待ちの課題 C-1 と、保留 H-2 を持つ課題 C-2 を置く。
func answerWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	st := openStore(t, ws)
	for _, title := range []string{"課題 1", "課題 2"} {
		if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: title, DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	coretest.InsertHoldWithOptions(t, ws, 1, "どちらにする？", "unclassified", "2026-11-22T00:00:00.000Z", answerOptsJSON, "")
	coretest.InsertHoldWithOptions(t, ws, 2, "別の課題の問い", "unclassified", "2026-11-22T00:00:01.000Z", answerOptsJSON, "")
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	coretest.SetChallengeStatus(t, ws, 2, "awaiting_human")
	return ws
}

// readHold は課題 C-1 の詳細から、保留 holdID の check と options を読む。
func readHold(t *testing.T, port int, holdID string) (check string, opts []any) {
	t.Helper()
	for _, h := range mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-1")["holds"].([]any) {
		m := h.(map[string]any)
		if m["hold_id"] == holdID {
			return m["check"].(string), m["options"].([]any)
		}
	}
	t.Fatalf("hold %s not found", holdID)
	return "", nil
}

func postAnswer(t *testing.T, port int, path, check, option string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"check": check, "option": option})
	return apiPost(t, port, path, string(b))
}

func holdState(t *testing.T, ws string, id string) core.Hold {
	t.Helper()
	d, err := openStore(t, ws).GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.Holds[0]
}

// AC-34・35・36: 選べる識別と照合の値で 200。経路 ui・本人確認 none で見出しだけが記録され、サイクルが始まる（きっかけ answer）。
func TestHoldAnswer_RecordsTheLabelOverUIAndStartsTheCycle(t *testing.T) {
	ws := answerWorkspace(t)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	check, _ := readHold(t, port, "H-1")
	code, body := postAnswer(t, port, holdAnswerPath, check, "a")
	if code != 200 {
		t.Fatalf("answer = %d %s", code, body)
	}
	var doc struct {
		Cycle struct {
			Reason  string `json:"reason"`
			Started bool   `json:"started"`
		} `json:"cycle"`
		Hold map[string]any `json:"hold"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Cycle.Started || doc.Cycle.Reason != "started" || doc.Hold["hold_id"] != "H-1" || doc.Hold["answer"] != "案 A" {
		t.Errorf("response = %s", body)
	}
	h := holdState(t, ws, "C-1")
	if h.Answer == nil || *h.Answer != "案 A" || h.AnswerOption == nil || *h.AnswerOption != "a" ||
		h.AnsweredChannel == nil || *h.AnsweredChannel != "ui" || h.AnsweredVerification == nil || *h.AnsweredVerification != "none" {
		t.Errorf("recorded hold = %+v", h)
	}
	if n := atomic.LoadInt32(&r.calls); n != 1 || r.trigger != core.CycleTriggerAnswer {
		t.Errorf("calls=%d trigger=%q", n, r.trigger)
	}
}

// AC-37: すでに走っているときは重ねて始めず、応答は started=false・already_running。
func TestHoldAnswer_DoesNotStackACycleWhenOneIsRunning(t *testing.T) {
	ws := answerWorkspace(t)
	port, r := recordingServer(t, ws, CycleLaunch{AlreadyRunning: true})
	check, _ := readHold(t, port, "H-1")
	code, body := postAnswer(t, port, holdAnswerPath, check, "a")
	if code != 200 || !strings.Contains(body, `"already_running"`) || strings.Contains(body, `"started":true`) {
		t.Fatalf("answer = %d %s", code, body)
	}
	if atomic.LoadInt32(&r.calls) != 1 {
		t.Errorf("calls = %d", r.calls)
	}
}

// AC-38: 古い照合の値は 409 で、保留は開いたまま。
func TestHoldAnswer_StaleCheckIs409AndKeepsTheHoldOpen(t *testing.T) {
	ws := answerWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	code, body := postAnswer(t, port, holdAnswerPath, "stale", "a")
	if code != http.StatusConflict {
		t.Fatalf("answer = %d %s", code, body)
	}
	if !holdState(t, ws, "C-1").Open() {
		t.Error("hold was closed")
	}
}

// AC-39: 二度押し（閉じた保留を指す）は 409。
func TestHoldAnswer_ClosedHoldIs409(t *testing.T) {
	ws := answerWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	if code, body := postAnswer(t, port, holdAnswerPath, check, "a"); code != 200 {
		t.Fatalf("first = %d %s", code, body)
	}
	if code, body := postAnswer(t, port, holdAnswerPath, check, "a"); code != http.StatusConflict {
		t.Errorf("second = %d %s", code, body)
	}
}

// AC-40: ボタンで選べない選択肢（足す依頼の「足す」「計画を作り直す」）は 403。「足さずに進める」は選べる。
func TestHoldAnswer_OverLimitAddAndReplanAre403(t *testing.T) {
	ws := initWorkspace(t)
	st := openStore(t, ws)
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "課題", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	add := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1})
	coretest.InsertTaskAdditionHold(t, ws, add, "task_add_over_limit", "上限を超える", "unclassified", overLimitOpts)
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, opts := readHold(t, port, "H-1")
	buttons := map[string]any{}
	for _, o := range opts {
		m := o.(map[string]any)
		buttons[m["id"].(string)] = m["button"]
	}
	if buttons["add"] != false || buttons["replan"] != false || buttons["skip"] != true {
		t.Errorf("buttons = %v", buttons) // AC-48
	}
	for _, opt := range []string{"add", "replan"} {
		if code, body := postAnswer(t, port, holdAnswerPath, check, opt); code != http.StatusForbidden {
			t.Errorf("%s = %d %s", opt, code, body)
		}
	}
	if !holdState(t, ws, "C-1").Open() {
		t.Error("hold was closed")
	}
}

// AC-41: 選択肢を持たない保留を指すと 403。
func TestHoldAnswer_HoldWithoutOptionsIs403(t *testing.T) {
	ws := initWorkspace(t)
	st := openStore(t, ws)
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "課題", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	coretest.InsertHoldWithOptions(t, ws, 1, "自由に答える問い", "unclassified", "2026-11-22T00:00:00.000Z", "", "")
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	if code, body := postAnswer(t, port, holdAnswerPath, check, "a"); code != http.StatusForbidden {
		t.Errorf("answer = %d %s", code, body)
	}
}

// AC-42・43: 保留に無い識別・識別なし・添え書きつき（知らないキー）は 400 で、回答は記録されない。
func TestHoldAnswer_BadInputIs400AndRecordsNothing(t *testing.T) {
	ws := answerWorkspace(t)
	port, r := recordingServer(t, ws, CycleLaunch{Started: true})
	check, _ := readHold(t, port, "H-1")
	bodies := map[string]string{
		"unknown option": `{"check":"` + check + `","option":"zzz"}`,
		"no option":      `{"check":"` + check + `"}`,
		"no check":       `{"option":"a"}`,
		"with a note":    `{"check":"` + check + `","option":"a","answer":"ただし"}`,
		"with replan":    `{"check":"` + check + `","option":"a","replan":true}`,
		"not json":       `nope`,
		"trailing":       `{"check":"` + check + `","option":"a"} {}`,
	}
	for name, b := range bodies {
		if code, body := apiPost(t, port, holdAnswerPath, b); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, code, body)
		}
	}
	if !holdState(t, ws, "C-1").Open() {
		t.Error("hold was closed")
	}
	if atomic.LoadInt32(&r.calls) != 0 {
		t.Errorf("cycle started on a refused answer")
	}
}

// AC-44・45: 無い保留・ほかの課題の保留は 404。
func TestHoldAnswer_MissingOrForeignHoldIs404(t *testing.T) {
	ws := answerWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	for name, path := range map[string]string{"missing": "/api/v1/workspaces/w/challenges/C-1/holds/H-9/answer", "foreign": otherHoldPath} {
		if code, body := postAnswer(t, port, path, check, "a"); code != http.StatusNotFound {
			t.Errorf("%s = %d %s", name, code, body)
		}
	}
}

// AC-46: CSRF の値が無ければ断られる。
func TestHoldAnswer_WithoutCSRFIsRefused(t *testing.T) {
	ws := answerWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, _ := readHold(t, port, "H-1")
	b, _ := json.Marshal(map[string]string{"check": check, "option": "a"})
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+holdAnswerPath, strings.NewReader(string(b)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if !holdState(t, ws, "C-1").Open() {
		t.Error("hold was closed")
	}
}

// AC-47・48・51・52: 課題の詳細と questions の保留に、照合の値と選択肢ごとの button が入る（照合の値は同じ保留で一致する）。
func TestHoldReads_CarryCheckAndButton(t *testing.T) {
	ws := answerWorkspace(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	check, opts := readHold(t, port, "H-1")
	if check == "" || len(opts) != 2 || opts[0].(map[string]any)["button"] != true {
		t.Errorf("detail check=%q opts=%v", check, opts)
	}
	qs := mustJSON(t, port, "/api/v1/questions")["questions"].([]any)
	found := false
	for _, q := range qs {
		m := q.(map[string]any)
		if m["challenge_id"] == "C-1" {
			found = true
			if m["check"] != check || m["options"].([]any)[1].(map[string]any)["button"] != true {
				t.Errorf("question = %v", m)
			}
		}
	}
	if !found {
		t.Error("question for C-1 missing")
	}
}
