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
)

// specLinkFixture はワークスペースと外のディレクトリを作り、リンクを置く。
func specLinkFixture(t *testing.T, ws string) {
	t.Helper()
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "o.md", "x")
	writeWorkspaceFile(t, ws, "docs/x.md", "x")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"outdir":      outside,
		"outfile":     filepath.Join(outside, "o.md"),
		"outsub":      filepath.Join(outside, "sub"),
		"dangling":    filepath.Join(outside, "notyet.md"),
		"reldangling": "../notyet.md",
		"chain":       "outdir",
		"loop":        "loop",
		"indir":       "docs",
		"inabs":       filepath.Join(ws, "docs"),
		"indangling":  "docs/notyet.md",
		"root":        ".",
		"docs/up":     "..",
	} {
		if err := os.Symlink(target, filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpecPath_CreateAndEdit(t *testing.T) {
	ctx := context.Background()
	s, ws := newWorkspaceStore(t)
	specLinkFixture(t, ws)

	for _, ok := range []string{
		"docs/x.md", "docs/y.md", "no/such/dir/x.md", "docs/../docs/x.md", "indir/x.md", "inabs/new.md",
		"indangling", "docs/up/docs/x.md", "docs/x.md/under-a-file.md", "..hidden.md",
	} {
		c, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "t", Spec: ok})
		if err != nil {
			t.Errorf("create %q: %v", ok, err)
			continue
		}
		if c.SpecPath == nil || *c.SpecPath != ok {
			t.Errorf("create %q: returned spec_path = %v", ok, c.SpecPath)
		}
		d, err := s.GetChallenge(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.SpecPath == nil || *d.SpecPath != ok {
			t.Errorf("show %q: spec_path = %v", ok, d.SpecPath)
		}
	}

	plain, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "plain"})
	if err != nil || plain.SpecPath != nil {
		t.Fatalf("create without spec: %+v, %v", plain, err)
	}

	kept, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "kept", Spec: "docs/x.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		filepath.Join(ws, "docs/x.md"), "/etc/hosts", "..", "../x.md", "docs/../../x.md", "../" + filepath.Base(ws) + "/docs/x.md",
		"outdir/o.md", "outdir/new/x.md", "outfile", "dangling", "reldangling", "chain/o.md", "loop", "loop/x.md",
		// リンクの後の `..` は、リンクの先から数える（字面で畳むと内側に見える）。
		"outsub/../o.md",
		// 字面で畳むと外を指すリンクになる。
		"docs/up/../outfile", "no/../outfile",
		".", "docs/..", "root", "  ", "a\x00b", "bad\xff.md",
	} {
		before := mustExport(t, s)
		if _, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "t", Spec: bad}); !errors.Is(err, ErrValidation) {
			t.Errorf("create %q: err = %v, want ErrValidation", bad, err)
		}
		title := "changed"
		if _, err := s.EditChallenge(ctx, ChannelCLI, kept.ID, EditInput{Title: &title, Spec: &bad}); !errors.Is(err, ErrValidation) {
			t.Errorf("edit %q: err = %v, want ErrValidation", bad, err)
		}
		if string(mustExport(t, s)) != string(before) {
			t.Fatalf("%q: a rejected create or edit changed the store", bad)
		}
	}

	next := "docs/y.md"
	c, err := s.EditChallenge(ctx, ChannelCLI, kept.ID, EditInput{Spec: &next})
	if err != nil || c.SpecPath == nil || *c.SpecPath != next || c.Version != kept.Version+1 {
		t.Fatalf("edit: %+v, %v", c, err)
	}
	// 同じ値は差分が無いので版を変えない。指定しない edit は場所を保つ。
	if c, err = s.EditChallenge(ctx, ChannelCLI, kept.ID, EditInput{Spec: &next}); err != nil || c.Version != kept.Version+1 {
		t.Fatalf("same value: %+v, %v", c, err)
	}
	title := "renamed"
	if c, err = s.EditChallenge(ctx, ChannelCLI, kept.ID, EditInput{Title: &title}); err != nil || c.SpecPath == nil || *c.SpecPath != next {
		t.Fatalf("edit of another field: %+v, %v", c, err)
	}
	empty := ""
	if c, err = s.EditChallenge(ctx, ChannelCLI, kept.ID, EditInput{Spec: &empty}); err != nil || c.SpecPath != nil {
		t.Fatalf("clear: %+v, %v", c, err)
	}
	if d, err := s.GetChallenge(ctx, kept.ID); err != nil || d.SpecPath != nil {
		t.Fatalf("show after clear: %+v, %v", d, err)
	}

	logs, err := s.ListActivities(ctx, &kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	var joined string
	for _, a := range logs {
		joined += a.Action + " " + string(a.Before) + " " + string(a.After) + "\n"
	}
	for _, want := range []string{`"spec_path":"docs/x.md"`, `"spec_path":"docs/y.md"`, `"spec_path":null`} {
		if !strings.Contains(joined, want) {
			t.Errorf("activity log lacks %s:\n%s", want, joined)
		}
	}
}

func TestImport_SpecPathAndCoversSurviveTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, _ := newWorkspaceStore(t)
	c, err := src.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "with", Spec: "docs/x.md"})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	// 計画から covers を入れる口はまだ無いので、列へ直接書く。
	if err := src.mutate(ctx, ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		made, err := createTasks(ctx, tx, rec, cid, 1, []TaskInput{
			{Key: "a", Title: "a", Body: "b", DoneCriteria: "d", Repo: "r", Operation: "o"},
			{Key: "b", Title: "b", Body: "b", DoneCriteria: "d", Repo: "r", Operation: "o"},
		})
		if err != nil {
			return err
		}
		id, _ := parseTaskID(made[0].ID)
		_, err = tx.ExecContext(ctx, `UPDATE task SET covers = ? WHERE id = ?`, `["AC-0123abcd","AC-89abcdef"]`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(ctx, mustExport(t, src)); err != nil {
		t.Fatal(err)
	}
	d, err := dst.GetChallenge(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.SpecPath == nil || *d.SpecPath != "docs/x.md" {
		t.Errorf("spec_path = %v", d.SpecPath)
	}
	if len(d.Tasks) != 2 || !reflect.DeepEqual(d.Tasks[0].Covers, []string{"AC-0123abcd", "AC-89abcdef"}) || d.Tasks[1].Covers != nil {
		t.Errorf("tasks = %+v", d.Tasks)
	}
}

func TestSpecPath_TransitionResultKeepsIt(t *testing.T) {
	ctx := context.Background()
	s, _ := newWorkspaceStore(t)
	c, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "t", Spec: "docs/x.md"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ClassifyChallenge(ctx, ChannelCLI, c.ID, ClassifyInput{Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SpecPath == nil || *got.SpecPath != "docs/x.md" {
		t.Errorf("spec_path of the transition result = %v", got.SpecPath)
	}
}
