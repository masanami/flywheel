package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// ChangedPaths は、追跡していないファイル（ディレクトリの中と、引用の要る名前を含む）と変更したファイルを返し、
// Git が無視するものは返さない。
func TestChangedPaths_ListsChangedAndUntrackedFiles(t *testing.T) {
	clone := newClone(t, "https://github.com/o/r.git")
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(clone, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New().ChangedPaths(context.Background(), core.SlotTree{Path: clone})
	if err != nil || len(got) != 0 {
		t.Fatalf("clean clone: %q %v", got, err)
	}
	write("tracked.txt", "1")
	gitIn(t, clone, "add", "tracked.txt")
	gitIn(t, clone, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "add")
	gitIn(t, clone, "config", "status.showUntrackedFiles", "no")
	write("tracked.txt", "2")
	write("a.txt", "x")
	write("dir/日本語 の名前.txt", "x")
	write(".gitignore", "ignored.txt\n")
	write("ignored.txt", "x")
	got, err = New().ChangedPaths(context.Background(), core.SlotTree{Path: clone})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{".gitignore", "a.txt", "dir/日本語 の名前.txt", "tracked.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestChangedPaths_NotARepositoryIsAnError(t *testing.T) {
	if _, err := New().ChangedPaths(context.Background(), core.SlotTree{Path: t.TempDir()}); err == nil {
		t.Fatal("want an error")
	}
}
