package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// J2・J3・J5 の入力に、記憶の置き場に INDEX.md があるときだけ索引のパスと指示の節を足す
// （AC-275・AC-276）。J1 の入力には足さない（AC-278）。

func writeMemoryIndex(t *testing.T, s *Store, decl *AgentDeclaration) string {
	t.Helper()
	dir := filepath.Join(s.Workspace(), decl.MemoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "INDEX.md")
	if err := os.WriteFile(p, []byte("# INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func requireIndexSection(t *testing.T, secs []JudgmentDataSection, wantPath string) {
	t.Helper()
	sec, ok := sectionByLabel(secs, memoryIndexSectionLabel)
	if !ok {
		t.Fatalf("no %q section:\n%s", memoryIndexSectionLabel, sectionsText(secs))
	}
	if !strings.Contains(sec.Content, wantPath) {
		t.Errorf("section = %q, want the index path %s", sec.Content, wantPath)
	}
}

func requireNoIndexSection(t *testing.T, secs []JudgmentDataSection) {
	t.Helper()
	if _, ok := sectionByLabel(secs, memoryIndexSectionLabel); ok {
		t.Fatalf("unexpected %q section:\n%s", memoryIndexSectionLabel, sectionsText(secs))
	}
	if strings.Contains(sectionsText(secs), "INDEX.md") {
		t.Errorf("input mentions INDEX.md without one:\n%s", sectionsText(secs))
	}
}

func TestMemoryIndexSections_NoIndexOrOutsideSymlink_ReturnsNothing(t *testing.T) {
	s := newStoreForTest(t)
	decl := defaultAgentDeclaration()
	if got := memoryIndexSections(s.Workspace(), decl); got != nil {
		t.Errorf("no memory dir: %v", got)
	}
	// 置き場はあるが INDEX.md が無い。
	if err := os.MkdirAll(filepath.Join(s.Workspace(), decl.MemoryDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := memoryIndexSections(s.Workspace(), decl); got != nil {
		t.Errorf("no INDEX.md: %v", got)
	}
	// 置き場の外を指す INDEX.md のリンクは渡さない。
	outside := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Workspace(), decl.MemoryDir, "INDEX.md")); err != nil {
		t.Fatal(err)
	}
	if got := memoryIndexSections(s.Workspace(), decl); got != nil {
		t.Errorf("outside symlink: %v", got)
	}
}

func TestJ2Input_MemoryIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		with bool
	}{{"with INDEX.md", true}, {"without INDEX.md", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "a", "P1")
			var got []JudgmentDataSection
			inv := succeededInvoker(j2PlanOutput(nil))
			inv.onInvoke = func(in JudgmentLaunchInput) { got = in.Sections }
			var p string
			if tc.with {
				p = writeMemoryIndex(t, f.s, f.agent)
			}
			if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("J2 was not invoked")
			}
			if tc.with {
				requireIndexSection(t, got, p)
			} else {
				requireNoIndexSection(t, got)
			}
		})
	}
}

func TestJ3Input_MemoryIndex(t *testing.T) {
	for _, with := range []bool{true, false} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		var p string
		if with {
			p = writeMemoryIndex(t, f.s, f.agent)
		}
		f.runDelegation(t, id)
		if len(f.j3Inputs) != 1 {
			t.Fatalf("J3 inputs = %d", len(f.j3Inputs))
		}
		if with {
			requireIndexSection(t, f.j3Inputs[0].Sections, p)
		} else {
			requireNoIndexSection(t, f.j3Inputs[0].Sections)
		}
	}
}

func TestJ5Input_MemoryIndex(t *testing.T) {
	for _, with := range []bool{true, false} {
		f := newJ5Fixture(t, "impl")
		f.delegateToVerifying(t, "C-1")
		var p string
		if with {
			p = writeMemoryIndex(t, f.s, f.agent)
		}
		if _, err := f.verify(t, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.j5Inputs) == 0 {
			t.Fatal("J5 was not invoked")
		}
		last := f.j5Inputs[len(f.j5Inputs)-1].Sections
		if with {
			requireIndexSection(t, last, p)
		} else {
			requireNoIndexSection(t, last)
		}
	}
}

func TestJ1Input_NeverHasMemoryIndex(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	writeMemoryIndex(t, s, decl)
	ch, err := s.CreateChallenge(t.Context(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	inv := succeededInvoker(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
	var got []JudgmentDataSection
	inv.onInvoke = func(in JudgmentLaunchInput) { got = in.Sections }
	id := ch.ID
	if _, err := s.ClassifyAutoJ1(t.Context(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: inv, CycleID: beginJ1Cycle(t, s, 300)}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("J1 was not invoked")
	}
	requireNoIndexSection(t, got)
}
