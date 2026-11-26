package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 仕様の下書きの見分けと、置き換える元の文書の場所（docs/features/request-to-plan.md の S2a の土台）を実ストアで確かめる。

func createAndShow(t *testing.T, s *Store, in CreateInput) *ChallengeDetail {
	t.Helper()
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, in)
	if err != nil {
		t.Fatalf("create %+v: %v", in, err)
	}
	return showDetail(t, s, c.ID)
}

func showDetail(t *testing.T, s *Store, id string) *ChallengeDetail {
	t.Helper()
	d, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// AC-1〜AC-7。
func TestSpecIsDraft_DecidedByTheNormalizedPlace(t *testing.T) {
	s, _ := newWorkspaceStore(t)
	for _, c := range []struct {
		ac, spec string
		want     bool
	}{
		{"AC-1", ".flywheel/specs/example.md", true},
		{"AC-2", ".flywheel/specs/sub/example.md", true},
		{"AC-3", "./.flywheel/specs/example.md", true},
		{"AC-3 (doubled slash)", ".flywheel//specs/./example.md", true},
		{"AC-4", ".flywheel/repos/example/docs/features/example.md", false},
		{"AC-5", "", false},
		{"AC-6", ".flywheel/specs-old/example.md", false},
		{"AC-7", ".flywheel/specs", false},
		{"AC-7 (trailing slash)", ".flywheel/specs/", false},
		{"nested place", "docs/.flywheel/specs/example.md", false},
	} {
		if got := createAndShow(t, s, CreateInput{Title: "t", Spec: c.spec}).SpecIsDraft(); got != c.want {
			t.Errorf("%s: SpecIsDraft(%q) = %v, want %v", c.ac, c.spec, got, c.want)
		}
	}
}

// AC-8。
func TestSpecIsDraft_KeepsTheSpecPathAsReceived(t *testing.T) {
	s, _ := newWorkspaceStore(t)
	const given = "./.flywheel/specs/example.md"
	d := createAndShow(t, s, CreateInput{Title: "t", Spec: given})
	if d.SpecPath == nil || *d.SpecPath != given {
		t.Errorf("spec_path = %v, want %q", d.SpecPath, given)
	}
}

// AC-9。
func TestInit_NewGitignoreExcludesSpecDrafts(t *testing.T) {
	_, dir := newWorkspaceStore(t)
	data, err := os.ReadFile(filepath.Join(dir, flywheelDirName, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("\n"+string(data), "\nspecs/\n") {
		t.Errorf(".gitignore lacks the specs/ line:\n%s", data)
	}
}

// AC-10。
func TestInit_LeavesAnExistingGitignoreAsItIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, flywheelDirName, ".gitignore")
	writeWorkspaceFile(t, dir, filepath.Join(flywheelDirName, ".gitignore"), "flywheel.db\n# mine")
	for range 2 {
		if _, err := Init(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "flywheel.db\n# mine" {
			t.Fatalf(".gitignore changed: %q", data)
		}
	}
}

// AC-11。
func TestReadSpecCriteria_DraftUsesTheSameRulesAndIDs(t *testing.T) {
	_, ws := newWorkspaceStore(t)
	const doc = "# 題\n\n## 受入基準\n\n### 区分\n\n- [ ] 一つ目の基準\n- [x]   二つ目の  基準\n  - [ ] 入れ子の行\n\n## 受入基準（実装済み）\n\n- [ ] 数えない\n"
	writeWorkspaceFile(t, ws, ".flywheel/specs/example.md", doc)
	writeWorkspaceFile(t, ws, "docs/features/example.md", doc)
	draft, err := readSpecCriteria(ws, ".flywheel/specs/example.md")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := readSpecCriteria(ws, "docs/features/example.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(draft) == 0 || !reflect.DeepEqual(draft, plain) {
		t.Errorf("draft criteria = %+v, plain = %+v", draft, plain)
	}
}

// AC-13・AC-14・AC-22・AC-26。
func TestSpecBasePath_Create(t *testing.T) {
	s, _ := newWorkspaceStore(t)
	const base = "docs/features/example.md"
	for _, c := range []struct{ name, spec, base string }{
		{"AC-13 with a draft", ".flywheel/specs/example.md", base},
		{"AC-22 a file no repository has", ".flywheel/specs/example.md", "no/such/repository/file.md"},
		{"AC-26 not a draft", "docs/features/other.md", base},
		{"AC-26 no spec", "", base},
	} {
		d := createAndShow(t, s, CreateInput{Title: "t", Spec: c.spec, SpecBase: c.base})
		if d.SpecBasePath == nil || *d.SpecBasePath != c.base {
			t.Errorf("%s: spec_base_path = %v, want %q", c.name, d.SpecBasePath, c.base)
		}
	}
	if d := createAndShow(t, s, CreateInput{Title: "t", Spec: ".flywheel/specs/example.md"}); d.SpecBasePath != nil {
		t.Errorf("AC-14: spec_base_path = %q, want nil", *d.SpecBasePath)
	}
}

// AC-15・AC-16・AC-23。
func TestSpecBasePath_Edit(t *testing.T) {
	ctx := context.Background()
	s, ws := newWorkspaceStore(t)
	id := createAndShow(t, s, CreateInput{Title: "t", SpecBase: "docs/a.md"}).ID
	edit := func(v string) *Challenge {
		t.Helper()
		c, err := s.EditChallenge(ctx, ChannelCLI, id, EditInput{SpecBase: &v})
		if err != nil {
			t.Fatalf("edit %q: %v", v, err)
		}
		return c
	}
	check := func(label string, got *Challenge, want string) {
		t.Helper()
		d := showDetail(t, s, id)
		for _, p := range []*string{got.SpecBasePath, d.SpecBasePath} {
			if (want == "") != (p == nil) || (p != nil && *p != want) {
				t.Errorf("%s: spec_base_path = %v, want %q", label, p, want)
			}
		}
	}
	check("AC-15", edit("docs/b.md"), "docs/b.md")
	version := showDetail(t, s, id).Version
	check("same value", edit("docs/b.md"), "docs/b.md")
	if got := showDetail(t, s, id).Version; got != version {
		t.Errorf("editing to the same value bumped the version: %d -> %d", version, got)
	}
	other := "other"
	c, err := s.EditChallenge(ctx, ChannelCLI, id, EditInput{Title: &other})
	if err != nil {
		t.Fatal(err)
	}
	check("another field keeps it", c, "docs/b.md")
	check("AC-16", edit(""), "")

	coretest.SetChallengeStatus(t, ws, 1, string(StatusAwaitingPlanApproval))
	check("AC-23", edit("docs/c.md"), "docs/c.md")
}

// AC-24。
func TestSpecBasePath_EditOfAFinishedChallengeIsTerminalState(t *testing.T) {
	ctx := context.Background()
	s, ws := newWorkspaceStore(t)
	id := createAndShow(t, s, CreateInput{Title: "t", SpecBase: "docs/a.md"}).ID
	coretest.SetChallengeStatus(t, ws, 1, string(StatusDone))
	for _, v := range []string{"docs/b.md", ""} {
		if _, err := s.EditChallenge(ctx, ChannelCLI, id, EditInput{SpecBase: &v}); !errors.Is(err, ErrTerminalState) {
			t.Errorf("edit %q of a done challenge: %v, want ErrTerminalState", v, err)
		}
	}
	if d := showDetail(t, s, id); d.SpecBasePath == nil || *d.SpecBasePath != "docs/a.md" {
		t.Errorf("spec_base_path = %v", d.SpecBasePath)
	}
}

// AC-17〜AC-21。
func TestSpecBasePath_Validation(t *testing.T) {
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	id := createAndShow(t, s, CreateInput{Title: "kept", SpecBase: "docs/a.md"}).ID
	// 文字は Unicode のコードポイントで数える（「あ」は 3 バイト）。
	long := func(n int) string { return strings.Repeat("あ", n) }
	for _, c := range []struct{ ac, base string }{
		{"AC-17/18", "/etc/hosts"},
		{"AC-19", ".."},
		{"AC-19", "../x.md"},
		{"AC-19", "docs/../x.md"},
		{"AC-19", "docs/.."},
		{"AC-20", long(501)},
		{"AC-20 ascii", strings.Repeat("a", 501)},
		{"invalid UTF-8", "docs/\xff.md"},
		{"NUL", "docs/\x00.md"},
	} {
		if _, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "bad", SpecBase: c.base}); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: create %q: %v, want ErrValidation", c.ac, c.base, err)
		}
		v := c.base
		if _, err := s.EditChallenge(ctx, ChannelCLI, id, EditInput{SpecBase: &v}); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: edit %q: %v, want ErrValidation", c.ac, c.base, err)
		}
	}
	list, err := s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("AC-17: challenges = %d, want 1 (a rejected create makes nothing)", len(list))
	}
	if d := showDetail(t, s, id); d.SpecBasePath == nil || *d.SpecBasePath != "docs/a.md" {
		t.Errorf("AC-18: spec_base_path after rejected edits = %v", d.SpecBasePath)
	}
	for _, ok := range []string{long(500), strings.Repeat("a", 500), "..hidden/x..md", "./docs//x.md"} {
		d := createAndShow(t, s, CreateInput{Title: "ok", SpecBase: ok})
		if d.SpecBasePath == nil || *d.SpecBasePath != ok {
			t.Errorf("AC-21: spec_base_path = %v, want the received %q", d.SpecBasePath, ok)
		}
	}
}

func TestSpecBasePath_TransitionResultKeepsIt(t *testing.T) {
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	id := createAndShow(t, s, CreateInput{Title: "t", SpecBase: "docs/a.md"}).ID
	got, err := s.ClassifyChallenge(ctx, ChannelCLI, id, ClassifyInput{Priority: string(PriorityP1)})
	if err != nil {
		t.Fatal(err)
	}
	if got.SpecBasePath == nil || *got.SpecBasePath != "docs/a.md" {
		t.Errorf("spec_base_path after a transition = %v", got.SpecBasePath)
	}
}

// makeTwoTasks は課題にタスクを 2 件作り、その ID を返す（lands_spec を書く口はまだ無い）。
func makeTwoTasks(t *testing.T, s *Store, challengeID string) []int64 {
	t.Helper()
	cid, _ := parseChallengeID(challengeID)
	var ids []int64
	if err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		made, err := createTasks(context.Background(), tx, rec, cid, 1, []TaskInput{
			{Key: "a", Title: "a", Body: "b", DoneCriteria: "d", Repo: "r", Operation: "o"},
			{Key: "b", Title: "b", Body: "b", DoneCriteria: "d", Repo: "r", Operation: "o"},
		})
		if err != nil {
			return err
		}
		for _, m := range made {
			if m.LandsSpec {
				t.Errorf("a newly made task has lands_spec: %+v", m)
			}
			id, _ := parseTaskID(m.ID)
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ids
}

// AC-129・AC-130。
func TestImport_SpecBasePathAndLandsSpecSurviveTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, _ := newWorkspaceStore(t)
	with := createAndShow(t, src, CreateInput{Title: "with", Spec: ".flywheel/specs/example.md", SpecBase: "docs/features/example.md"}).ID
	without := createAndShow(t, src, CreateInput{Title: "without"}).ID
	ids := makeTwoTasks(t, src, with)
	if err := src.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET lands_spec = 1 WHERE id = ?`, ids[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}

	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(ctx, mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	d := showDetail(t, dst, with)
	if d.SpecBasePath == nil || *d.SpecBasePath != "docs/features/example.md" {
		t.Errorf("AC-129: spec_base_path = %v", d.SpecBasePath)
	}
	if len(d.Tasks) != 2 || !d.Tasks[0].LandsSpec || d.Tasks[1].LandsSpec {
		t.Errorf("AC-130: tasks = %+v", d.Tasks)
	}
	if got := showDetail(t, dst, without).SpecBasePath; got != nil {
		t.Errorf("AC-129: spec_base_path of a challenge without it = %q", *got)
	}
}
