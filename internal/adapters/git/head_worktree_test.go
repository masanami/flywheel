package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// J5 の一時の worktree（#465）: 元のクローンの checkout を変えず、固定した SHA を detached で取り出し、
// 未コミットの変更ごと消して記録も残さない。
func TestHeadWorktree_AddAndRemove(t *testing.T) {
	base := newClone(t, "https://github.com/o/r.git")
	gitIn(t, base, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(base, "head-only"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, base, "add", "head-only")
	gitIn(t, base, "commit", "-q", "-m", "head")
	sha := gitIn(t, base, "rev-parse", "HEAD")
	gitIn(t, base, "checkout", "-q", "main")
	gitIn(t, base, "branch", "-q", "-D", "feat") // ブランチが無くても、コミットで取り出せる

	g, ctx := New(), context.Background()
	if ok, err := g.HasCommit(ctx, base, sha); err != nil || !ok {
		t.Fatalf("HasCommit = (%v, %v)", ok, err)
	}
	path := filepath.Join(t.TempDir(), "head")
	if err := g.AddHeadWorktree(ctx, base, path, sha); err != nil {
		t.Fatalf("AddHeadWorktree: %v", err)
	}
	if got := gitIn(t, path, "rev-parse", "HEAD"); got != sha {
		t.Errorf("HEAD = %s, want %s", got, sha)
	}
	if _, err := os.Stat(filepath.Join(path, "head-only")); err != nil {
		t.Errorf("head-only: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "head-only")); !os.IsNotExist(err) {
		t.Errorf("the base clone's checkout changed (stat: %v)", err)
	}
	if got := gitIn(t, base, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("the base clone's branch = %s", got)
	}

	if err := os.WriteFile(filepath.Join(path, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveHeadWorktree(ctx, base, path); err != nil {
		t.Fatalf("RemoveHeadWorktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the worktree remains (stat: %v)", err)
	}
	if list := worktreesOf(t, base); strings.Count(list, "worktree ") != 1 {
		t.Errorf("the worktree is still listed:\n%s", list)
	}
}

func TestHasCommit_MissingCommitOrCloneIsFalse(t *testing.T) {
	base := newClone(t, "")
	for _, clone := range []string{base, filepath.Join(t.TempDir(), "no-clone")} {
		ok, err := New().HasCommit(context.Background(), clone, strings.Repeat("0", 39)+"1")
		if err != nil || ok {
			t.Errorf("HasCommit(%s) = (%v, %v), want (false, nil)", clone, ok, err)
		}
	}
}

// path が worktree でない（強制終了の後に残ったディレクトリなど）ときも消す。
func TestRemoveHeadWorktree_RemovesAPlainDirectory(t *testing.T) {
	base := newClone(t, "")
	path := filepath.Join(t.TempDir(), "left")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := New().RemoveHeadWorktree(context.Background(), base, path); err != nil {
		t.Fatalf("RemoveHeadWorktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat: %v", err)
	}
}
