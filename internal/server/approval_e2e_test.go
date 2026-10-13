package server

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// E2E: 実バイナリの `flywheel serve` に対し、実際の HTTP で承認の 2 段階の API を通し、
// 結果を同じストアへの `flywheel show --json`（別プロセス）で確かめる。
// AC-153〜156（承認の経路・記録）、AC-160〜163（release_held・conflict・invalid_transition・検証）、
// AC-172〜175（他の書き込みの API が無い）。
func TestE2EApprovalWithRealBinary(t *testing.T) {
	bin := flywheelBinary(t)
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	addOp(t, ws, "release", "rel", strp("v1"))
	name := core.FleetNameFromPath(mustAbs(t, ws))

	port, _ := startBinary(t, bin, ws, 0)
	token, _ := fetchToken(t, port)
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/workspaces/" + name + "/challenges/C-1"

	do := func(method, url, body string, csrf bool) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if csrf {
			req.AddCookie(&http.Cookie{Name: "flywheel_csrf_" + strconv.Itoa(port), Value: token})
			req.Header.Set(CSRFHeader, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := do("GET", base+"/approval", "", false)
	if code != 200 || !strings.Contains(body, `"release_held":true`) || !strings.Contains(body, `"head_sha":null`) {
		t.Fatalf("summary: %d %s", code, body)
	}
	var sum struct {
		Approval struct{ Version int } `json:"approval"`
	}
	_ = json.Unmarshal([]byte(body), &sum)

	// AC-163/161: 不正な本文・古い版は変更しない。
	if code, _ := do("POST", base+"/approve", `{}`, true); code != 400 {
		t.Errorf("empty body = %d", code)
	}
	if code, _ := do("POST", base+"/approve", `{"version":`+strconv.Itoa(sum.Approval.Version+9)+`}`, true); code != 409 {
		t.Errorf("stale = %d", code)
	}
	// AC-172〜175: 他の書き込みは 404（正しい CSRF でも）。
	for _, p := range []string{"/reject", "/answer"} {
		if code, _ := do("POST", base+p, `{"version":1}`, true); code != 404 {
			t.Errorf("POST %s = %d", p, code)
		}
	}

	if code, b := do("POST", base+"/approve", `{"version":`+strconv.Itoa(sum.Approval.Version)+`}`, true); code != 200 || !strings.Contains(b, `"card"`) {
		t.Fatalf("approve: %d %s", code, b)
	}
	show := cliJSON(t, ws, "show", "C-1")
	var d struct {
		Approvals []struct {
			Kind         string `json:"kind"`
			Channel      string `json:"channel"`
			Verification string `json:"verification"`
		} `json:"approvals"`
		Operations []struct {
			State string `json:"state"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(show), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Approvals) != 1 || d.Approvals[0].Kind != "completion" || d.Approvals[0].Channel != "ui" || d.Approvals[0].Verification != "none" {
		t.Errorf("approvals = %+v", d.Approvals)
	}
	if len(d.Operations) != 1 || d.Operations[0].State != "pending" {
		t.Errorf("release must stay held: %+v", d.Operations)
	}
	if code, b := do("POST", base+"/approve", `{"version":99}`, true); code != 409 {
		t.Errorf("after approval = %d %s", code, b)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	return a
}
