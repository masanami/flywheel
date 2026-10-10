package server

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func fetch(h http.Handler, path string) (int, string, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(b), rec.Header().Get("Content-Type")
}

func notFoundForTest(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, CodeNotFound, "not found")
}

func TestPickUIPrefersBuiltDist(t *testing.T) {
	dist := fstest.MapFS{"dist/ui/index.html": {Data: []byte("BUILT")}}
	fb := fstest.MapFS{"fallback/index.html": {Data: []byte("FALLBACK")}}
	h := uiHandler(pickUI(dist, "dist/ui", fb, "fallback"), notFoundForTest)
	code, body, ct := fetch(h, "/")
	if code != 200 || body != "BUILT" || !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("got %d %q %q", code, body, ct)
	}
}

func TestPickUIFallsBackWhenNotBuilt(t *testing.T) {
	dist := fstest.MapFS{"dist/.gitkeep": {Data: nil}}
	fb := fstest.MapFS{"fallback/index.html": {Data: []byte("FALLBACK")}}
	h := uiHandler(pickUI(dist, "dist/ui", fb, "fallback"), notFoundForTest)
	if code, body, _ := fetch(h, "/"); code != 200 || body != "FALLBACK" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestUIHandlerServesAssetsAndRejectsUnknown(t *testing.T) {
	root := fstest.MapFS{
		"index.html":           {Data: []byte("I")},
		"manifest.webmanifest": {Data: []byte(`{"display":"standalone"}`)},
		"assets/a.js":          {Data: []byte("JS")},
	}
	h := uiHandler(root, notFoundForTest)
	if code, body, _ := fetch(h, "/assets/a.js"); code != 200 || body != "JS" {
		t.Fatalf("asset: %d %q", code, body)
	}
	if code, _, _ := fetch(h, "/manifest.webmanifest"); code != 200 {
		t.Fatalf("manifest: %d", code)
	}
	for _, p := range []string{"/nope", "/assets", "/assets/"} {
		code, body, ct := fetch(h, p)
		if code != 404 || !strings.Contains(body, CodeNotFound) || !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: %d %q %q", p, code, body, ct)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != 404 {
		t.Fatalf("POST /: %d", rec.Code)
	}
}

func TestEmbeddedFallbackIsTheNotBuiltPage(t *testing.T) {
	sub, err := fs.Sub(fallbackFS, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "UI がビルドされていません") {
		t.Fatalf("fallback index.html = %q", b)
	}
	// dist に成果物が無ければ、実際の埋め込みから fallback が選ばれる。
	got := uiHandler(pickUI(distFS, "dist/nonexistent", fallbackFS, "fallback"), notFoundForTest)
	if code, body, _ := fetch(got, "/"); code != 200 || body != string(b) {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestRealServerServesIndexAtRoot(t *testing.T) {
	_, port := startServer(t)
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "<html") {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
}
