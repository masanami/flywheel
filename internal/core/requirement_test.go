package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

func createRequirement(t *testing.T, s *Store, title string) *Requirement {
	t.Helper()
	r, err := s.CreateRequirement(context.Background(), ChannelCLI, CreateRequirementInput{Title: title})
	if err != nil {
		t.Fatalf("CreateRequirement: %v", err)
	}
	return r
}

func TestRequirement_ClosedSetsAndSharedValues_AC161_AC162(t *testing.T) {
	s, _ := newWorkspaceStore(t)
	for _, want := range []string{"RQ-1", "RQ-2"} {
		if r := createRequirement(t, s, "通知"); r.ID != want {
			t.Errorf("ID = %q, want %q", r.ID, want)
		}
	}
	if got, want := RequirementOriginValues(), []RequirementOrigin{"issue", "chat", "manual"}; !reflect.DeepEqual(got, want) {
		t.Errorf("origins = %v, want %v", got, want)
	}
	if got, want := SliceSplitStateValues(), []SliceSplitState{"pending", "awaiting_owner", "split", "single"}; !reflect.DeepEqual(got, want) {
		t.Errorf("slice split states = %v, want %v", got, want)
	}
	if got := []int{SliceMinCount, SliceMaxCount, SliceTitleMaxRunes, SliceTextMaxRunes}; !reflect.DeepEqual(got, []int{2, 8, 200, 2000}) {
		t.Errorf("limits = %v", got)
	}
	if requirementEvidenceDir != ".flywheel/requirements" {
		t.Errorf("evidence dir = %q", requirementEvidenceDir)
	}
	if origin, ok := ParseChallengeOrigin("split"); !ok || origin != ChallengeOriginSplit {
		t.Errorf("challenge origin split = %q, %v", origin, ok)
	}
}

func TestChallengeDetail_RequirementAndSliceSplitAreNilWithoutRecords_AC214_AC218(t *testing.T) {
	s, _ := newWorkspaceStore(t)
	d := createAndShow(t, s, CreateInput{Title: "t"})
	if d.Requirement != nil || d.SliceSplit != nil {
		t.Errorf("requirement = %+v, slice split = %+v, want nil", d.Requirement, d.SliceSplit)
	}
}

// coretest の FillAllTables は、C-1 を元の課題とする要求の記録と区切りを分ける記録、要求を指す C-2 を作る。
func TestChallengeDetail_ChallengesOfARequirement_AC216_AC217_AC219(t *testing.T) {
	s, ws := newWorkspaceStore(t)
	coretest.FillAllTables(t, ws)
	req := "RQ-1"
	coretest.ResolveRequirement(t, ws, 1, "2026-11-27T10:00:00.000Z")
	if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "late", Requirement: &req}); !errors.Is(err, ErrValidation) {
		t.Fatalf("create on a resolved requirement: %v, want ErrValidation", err)
	}
	coretest.Exec(t, ws, `UPDATE requirement SET resolved_at = NULL WHERE id = 1`)
	third := createAndShow(t, s, CreateInput{Title: "third", Requirement: &req})
	// 元の課題自身が要求を指していても、一覧には含めない。
	coretest.Exec(t, ws, `UPDATE challenge SET requirement_id = 1, status = 'withdrawn' WHERE id = 1`)

	ids := func(r *RequirementDetail) []string {
		var out []string
		for _, c := range r.Challenges {
			out = append(out, c.ID)
		}
		return out
	}
	want := []string{"C-2", third.ID}
	if !sort.StringsAreSorted(want) || !reflect.DeepEqual(ids(third.Requirement), want) {
		t.Errorf("challenges = %v, want %v", ids(third.Requirement), want)
	}
	for _, unlink := range []bool{false, true} {
		if unlink {
			coretest.Exec(t, ws, `UPDATE challenge SET requirement_id = NULL WHERE id = 1`)
		}
		origin := showDetail(t, s, "C-1")
		if origin.Requirement == nil || origin.Requirement.ID != "RQ-1" || !reflect.DeepEqual(ids(origin.Requirement), want) {
			t.Errorf("unlink=%v: origin requirement = %+v", unlink, origin.Requirement)
		}
	}

	split := showDetail(t, s, "C-1").SliceSplit
	if split == nil || split.State != SliceSplitStateSplit || split.Reason == nil || *split.Reason != "r" ||
		split.EvidencePath == nil || *split.EvidencePath != ".flywheel/requirements/split-C-1.md" ||
		split.HoldID == nil || *split.HoldID != "H-1" || split.Slices == nil || len(split.Slices) != 0 {
		t.Errorf("slice split = %+v", split)
	}
}

// 書き出して読み込んだストアで、要求の記録・区切りを分ける記録・課題の requirement_id の全列が同じ。
func TestImport_RequirementAndSliceSplitSurviveTheRoundTrip_AC238_AC239_AC240(t *testing.T) {
	s, ws := newWorkspaceStore(t)
	coretest.FillAllTables(t, ws)
	// NULL を許す列にも値を入れる。
	coretest.Exec(t, ws, `UPDATE requirement SET chat_session_id = (SELECT MIN(id) FROM chat_session)`)
	tables := []string{"requirement", "slice_split", "challenge"}
	before := coretest.DumpTables(t, ws, tables...)

	dst, dstWS := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, s)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if after := coretest.DumpTables(t, dstWS, tables...); after != before {
		t.Errorf("tables differ after the round trip:\n%s\nwant:\n%s", after, before)
	}
	// 要求の記録と区切りを分ける記録は、どの列も値を持つ形で往復させる。
	records := coretest.DumpTables(t, ws, "requirement", "slice_split")
	if strings.Count(records, "\n") != 4 || strings.Contains(records, "<nil>") {
		t.Errorf("fixture does not fill every column:\n%s", records)
	}
}

func TestDiscardRequirementEvidence(t *testing.T) {
	const path = requirementEvidenceDir + "/RQ-1.md"
	failures := func(t *testing.T, s *Store, entity, entityID string) []string {
		t.Helper()
		all, err := s.ListActivities(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range all {
			if a.Action == ActivityActionRequirementEvidenceCleanupFailed {
				if a.Entity != entity || a.EntityID != entityID {
					t.Errorf("activity target = %s %s, want %s %s", a.Entity, a.EntityID, entity, entityID)
				}
				out = append(out, string(a.After))
			}
		}
		return out
	}
	write := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("removes the file even when the context is cancelled", func(t *testing.T) {
		s, ws := newWorkspaceStore(t)
		write(t, filepath.Join(ws, path))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.discardRequirementEvidence(ctx, path, "requirement", 1, "tester")
		if exists(filepath.Join(ws, path)) {
			t.Error("the evidence remains")
		}
		if got := failures(t, s, "requirement", "RQ-1"); len(got) != 0 {
			t.Errorf("activities = %v, want none", got)
		}
	})
	t.Run("a missing file is not a failure", func(t *testing.T) {
		s, _ := newWorkspaceStore(t)
		s.discardRequirementEvidence(context.Background(), path, "requirement", 1, "tester")
		if got := failures(t, s, "requirement", "RQ-1"); len(got) != 0 {
			t.Errorf("activities = %v, want none", got)
		}
	})
	for name, link := range map[string]func(ws, outside string) (string, string){
		"a linked file outside the directory": func(ws, outside string) (string, string) {
			return filepath.Join(ws, path), outside
		},
		"a linked parent outside the directory": func(ws, outside string) (string, string) {
			return filepath.Join(ws, requirementEvidenceDir, "sub"), filepath.Dir(outside)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, ws := newWorkspaceStore(t)
			// ワークスペースの中でも、証跡の置き場の外なら消さない。
			outside := filepath.Join(ws, "docs", "RQ-1.md")
			write(t, outside)
			at, target := link(ws, outside)
			if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, at); err != nil {
				t.Fatal(err)
			}
			evidence := path
			if filepath.Base(at) == "sub" {
				evidence = requirementEvidenceDir + "/sub/RQ-1.md"
			}
			s.discardRequirementEvidence(context.Background(), evidence, "challenge", 7, "tester")
			if !exists(outside) {
				t.Error("the file outside the directory was removed")
			}
			got := failures(t, s, "challenge", "C-7")
			if len(got) != 1 || !strings.Contains(got[0], `"path":"`+evidence+`"`) {
				t.Errorf("activities = %v, want one with the path %s", got, evidence)
			}
		})
	}
}
