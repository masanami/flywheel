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

// J5 の head の fetch: 手元に無い head の 1 コミットだけを一時の ref へ取り、ref を消せる。
// 他のブランチ・タグ・FETCH_HEAD・作業ツリーは変えない。
func TestFetchHead_FetchesOnlyTheHeadIntoATemporaryRef(t *testing.T) {
	origin := newClone(t, "")
	gitIn(t, origin, "tag", "v1")
	gitIn(t, origin, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(origin, "head-only"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "add", "head-only")
	gitIn(t, origin, "commit", "-q", "-m", "head")
	sha := gitIn(t, origin, "rev-parse", "HEAD")
	gitIn(t, origin, "checkout", "-q", "-b", "other")
	gitIn(t, origin, "checkout", "-q", "main")

	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, filepath.Dir(clone), "clone", "-q", "--no-tags", "--single-branch", "--no-local", "-b", "main", origin, clone)
	g, ctx := New(), context.Background()
	if ok, _ := g.HasCommit(ctx, clone, sha); ok {
		t.Fatal("the clone already has the head")
	}
	const prefix = "refs/flywheel/j5-heads/"
	ref := prefix + "1-abcd-" + sha
	if err := g.FetchHead(ctx, clone, sha, ref); err != nil {
		t.Fatalf("FetchHead: %v", err)
	}
	if ok, err := g.HasCommit(ctx, clone, sha); err != nil || !ok {
		t.Fatalf("HasCommit after fetch = (%v, %v)", ok, err)
	}
	if got := gitIn(t, clone, "rev-parse", ref); got != sha {
		t.Errorf("ref = %s, want %s", got, sha)
	}
	if refs, err := g.ListHeadRefs(ctx, clone, prefix); err != nil || len(refs) != 1 || refs[0] != ref {
		t.Errorf("ListHeadRefs = (%v, %v)", refs, err)
	}
	if got := gitIn(t, clone, "for-each-ref", "--format=%(refname)", "refs/remotes", "refs/tags"); strings.Contains(got, "feat") || strings.Contains(got, "other") || strings.Contains(got, "v1") {
		t.Errorf("other refs were fetched:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "FETCH_HEAD")); err == nil {
		t.Error("FETCH_HEAD was written")
	}
	if _, err := os.Stat(filepath.Join(clone, "head-only")); !os.IsNotExist(err) {
		t.Errorf("the checkout changed (stat: %v)", err)
	}

	if err := g.DeleteHeadRef(ctx, clone, ref); err != nil {
		t.Fatalf("DeleteHeadRef: %v", err)
	}
	if err := g.DeleteHeadRef(ctx, clone, ref); err != nil {
		t.Errorf("DeleteHeadRef of a missing ref: %v", err)
	}
	if refs, _ := g.ListHeadRefs(ctx, clone, prefix); len(refs) != 0 {
		t.Errorf("refs remain: %v", refs)
	}
}

func TestFetchHead_FailsWithoutOriginOrCommit(t *testing.T) {
	clone := newClone(t, "")
	if err := New().FetchHead(context.Background(), clone, strings.Repeat("0", 39)+"1", "refs/flywheel/j5-heads/x"); err == nil {
		t.Error("FetchHead without an origin succeeded")
	}
}
