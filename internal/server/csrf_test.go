package server

import (
	"bytes"
	"github.com/masanami/flywheel/internal/core"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var metaRe = regexp.MustCompile(`<meta name="flywheel-csrf" content="([^"]+)" />`)

func fetchToken(t *testing.T, port int) (token string, cookie *http.Cookie) {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	m := metaRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("meta が無い: %q", b)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "flywheel_csrf_"+strconv.Itoa(port) {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("cookie が無い")
	}
	return m[1], cookie
}

func post(t *testing.T, port int, path string, cookie, header string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "flywheel_csrf_" + strconv.Itoa(port), Value: cookie})
	}
	if header != "" {
		req.Header.Set(CSRFHeader, header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestIndexSetsCSRFCookieAndMeta(t *testing.T) {
	_, port := startServer(t)
	token, c := fetchToken(t, port)
	if len(token) != 43 { // 32 バイトの base64url（パディング無し）
		t.Errorf("token = %q", token)
	}
	if c.Value != token || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	token2, _ := fetchToken(t, port)
	if token2 != token {
		t.Error("同じ起動で値が変わった")
	}
}

func TestPostRequiresValidCSRF(t *testing.T) {
	_, port := startServer(t)
	token, _ := fetchToken(t, port)
	for name, tc := range map[string][2]string{
		"cookie 無し":   {"", token},
		"ヘッダ無し":       {token, ""},
		"両方無し":        {"", ""},
		"不一致":         {token, token + "x"},
		"任意の値で一致":     {"other", "other"},
		"cookie だけ誤り": {"other", token},
	} {
		code, body := post(t, port, "/api/v1/nothing", tc[0], tc[1])
		if code != 403 || !strings.Contains(body, CodeForbiddenOrigin) {
			t.Errorf("%s: %d %q", name, code, body)
		}
	}
	// 一致していればルーティングへ進む（ルートが無いので 404）。
	if code, _ := post(t, port, "/api/v1/nothing", token, token); code != 404 {
		t.Errorf("valid: %d", code)
	}
}

func TestCSRFValueDiffersPerServer(t *testing.T) {
	_, p1 := startServer(t)
	_, p2 := startServer(t)
	t1, _ := fetchToken(t, p1)
	t2, _ := fetchToken(t, p2)
	if t1 == t2 {
		t.Fatal("起動ごとに値が違うはず")
	}
	if code, body := post(t, p2, "/x", t1, t1); code != 403 || !strings.Contains(body, CodeForbiddenOrigin) {
		t.Errorf("別の起動の値: %d %s", code, body)
	}
}

func TestGetNeedsNoCSRF(t *testing.T) {
	_, port := startServer(t)
	if code, _ := get(t, port, "127.0.0.1:"+strconv.Itoa(port), ""); code == http.StatusForbidden {
		t.Error("GET が CSRF で拒否された")
	}
}

// AC-151: CSRF で拒否した要求では、ストアのファイルの内容が変わらない。
func TestRejectedPostLeavesStoreUntouched(t *testing.T) {
	ws := initWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	db := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := fetchToken(t, port)
	for _, p := range []string{"/api/v1/workspaces/w/challenges/C-1/approve", "/api/v1/workspaces/w/cycle"} {
		if code, _ := post(t, port, p, "", ""); code != 403 {
			t.Errorf("POST %s = %d", p, code)
		}
		if code, _ := post(t, port, p, token, token+"x"); code != 403 {
			t.Errorf("POST %s = %d", p, code)
		}
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("拒否した要求でストアのファイルが変わった")
	}
}
