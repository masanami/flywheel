package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 区切り S4d-2 の土台（T1）: 足した 6 つの口は、いまの守り（CSRF）を通り、中身を置くまでは 404。誤りの対応づけは、後続が埋める枠の前提。

// 回答の口は中身を置いた（hold_answer_test.go が確かめる）ので、この一覧には載せない。
var screenPostRoutes = []string{
	"/api/v1/workspaces/w/ingest",
	"/api/v1/workspaces/w/cycle/stop",
	"/api/v1/workspaces/w/followups/done",
	"/api/v1/workspaces/w/followups/drop",
}

func TestScreenRoutes_AreGuardedByCSRFAndHaveNoBodyYet(t *testing.T) {
	_, port := startServer(t)
	token, _ := fetchToken(t, port)
	for _, p := range screenPostRoutes {
		if code, body := post(t, port, p, "", ""); code != http.StatusForbidden || !strings.Contains(body, CodeForbiddenOrigin) {
			t.Errorf("POST %s without CSRF: %d %q, want 403 forbidden_origin", p, code, body)
		}
		if code, _ := post(t, port, p, token, token); code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404 until the route has a body", p, code)
		}
		if code, _ := getPath(t, port, p, fmt.Sprintf("127.0.0.1:%d", port), ""); code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404 (write route)", p, code)
		}
	}
	if code, _ := getPath(t, port, "/api/v1/workspaces/w/notices", fmt.Sprintf("127.0.0.1:%d", port), ""); code != http.StatusNotFound {
		t.Errorf("GET notices: %d, want 404 until the route has a body", code)
	}
	if code, _ := post(t, port, "/api/v1/workspaces/w/notices", token, token); code != http.StatusNotFound {
		t.Errorf("POST notices: %d, want 404 (read route)", code)
	}
	// Host の検査も通る（許可されない Host は core の前で 403）。
	if code, body := getPath(t, port, "/api/v1/workspaces/w/notices", "evil.example:80", ""); code != http.StatusForbidden || !strings.Contains(body, CodeForbiddenOrigin) {
		t.Errorf("bad host: %d %q", code, body)
	}
}

func TestWriteCoreError_ButtonRouteMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{"hold or options changed / hold closed", core.ErrConflict, 409, CodeConflict},
		{"not accepted from the screen", core.ErrVerificationRejected, 403, CodeVerificationRejected},
		{"unknown option / no option / note", core.ErrValidation, 400, CodeValidationFailed},
		{"no such hold / other challenge's hold", core.ErrNotFound, 404, CodeNotFound},
		{"upstream unreadable", fmt.Errorf("%w: r#1: boom", core.ErrUpstreamFetchFailed), 502, CodeUpstreamUnavailable},
		{"a spec that is not ingestable stays a validation error", fmt.Errorf("%w: %w", core.ErrValidation, core.ErrUpstreamFetchFailed), 400, CodeValidationFailed},
		{"other", errors.New("boom"), 500, CodeInternalError},
	} {
		rec := httptest.NewRecorder()
		writeCoreError(rec, tc.err)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", tc.name, rec.Code, rec.Body.String(), tc.want, tc.code)
		}
	}
}
