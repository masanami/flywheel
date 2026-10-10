package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

// dist は UI のビルド成果物（web/ の Vite が internal/server/dist/ui へ出す。
// git の追跡外）。.gitkeep があるので、UI をビルドしていなくても embed は成立し、
// Node が無い環境でも go build が通る。
//
//go:embed all:dist
var distFS embed.FS

// fallback は UI がビルドされていないときに返す最小の index.html（追跡する）。
//
//go:embed fallback
var fallbackFS embed.FS

// uiFS は配信する UI。ビルドした成果物に index.html があればそれを、
// 無ければ最小の index.html だけを持つ fallback を返す。
func uiFS() fs.FS {
	return pickUI(distFS, "dist/ui", fallbackFS, "fallback")
}

func pickUI(dist fs.FS, distDir string, fallback fs.FS, fallbackDir string) fs.FS {
	if sub, err := fs.Sub(dist, distDir); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return sub
		}
	}
	sub, err := fs.Sub(fallback, fallbackDir)
	if err != nil {
		return emptyFS{}
	}
	return sub
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// uiHandler は GET / と UI の静的ファイルを返す。無いパスは notFound に任せる。
func uiHandler(root fs.FS, notFound http.HandlerFunc) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			notFound(w, r)
			return
		}
		p := path.Clean(r.URL.Path)
		if p == "/" {
			serveIndex(w, r, root)
			return
		}
		info, err := fs.Stat(root, p[1:])
		if err != nil || info.IsDir() {
			notFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	http.ServeFileFS(w, r, root, "index.html")
}
