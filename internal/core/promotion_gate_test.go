package core

// このファイルは、統合ブランチを反映先へ上げる前の検査（promotion_gate.go）を、実ストア・偽の契約のコマンド・偽の PR の取得・
// 偽の検査の起動と一時の worktree で固定する。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const gateDeclarationMake = `{"command": ["make", "gate"]}`

// fakeGate は偽の検査の起動。
type fakeGate struct {
	calls []PromotionGateInput
	out   PromotionGateOutput
	err   error
	// during は起動の中で呼ぶ。
	during func(in PromotionGateInput)
}

func (g *fakeGate) RunPromotionGate(_ context.Context, in PromotionGateInput) (PromotionGateOutput, error) {
	g.calls = append(g.calls, in)
	if g.during != nil {
		g.during(in)
	}
	return g.out, g.err
}

// fakeGateHeadGit は偽の一時の worktree。取り出すと、declaration があれば宣言のファイルを置く。
type fakeGateHeadGit struct {
	declaration string
	has         bool
	fetchErr    error
	added       []string // 取り出した sha
	removed     []string
}

func (g *fakeGateHeadGit) HasCommit(context.Context, string, string) (bool, error) { return g.has, nil }
func (g *fakeGateHeadGit) FetchHead(context.Context, string, string, string) error {
	return g.fetchErr
}
func (g *fakeGateHeadGit) ListHeadRefs(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (g *fakeGateHeadGit) DeleteHeadRef(context.Context, string, string) error { return nil }
func (g *fakeGateHeadGit) AddHeadWorktree(_ context.Context, _, path, sha string) error {
	g.added = append(g.added, sha)
	if g.declaration == "" {
		return nil
	}
	p := filepath.Join(path, filepath.FromSlash(promotionGateDeclarationPath))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(g.declaration), 0o600)
}
func (g *fakeGateHeadGit) RemoveHeadWorktree(_ context.Context, _, path string) error {
	g.removed = append(g.removed, path)
	return os.RemoveAll(path)
}

// gateFixture は、統合先を使う課題の上げる依頼がレビュー待ちになった状態（次の反映の段が「入れてよい」を返そうとする）を作る。
func gateFixture(t *testing.T) (*reflFixture, *fakeGate, *fakeGateHeadGit) {
	t.Helper()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	return f, f.gate.(*fakeGate), f.headGit.(*fakeGateHeadGit)
}

func TestPromotionGate_APassingGateRunsOnTheIntegrationHeadBeforeTheLandAnswer(t *testing.T) {
	t.Parallel()
	f, gate, head := gateFixture(t)
	gate.during = func(in PromotionGateInput) {
		if len(f.client.resumes) != 0 {
			t.Errorf("resumes = %v while the gate runs, want none", f.client.resumes)
		}
		if _, err := os.Stat(in.Dir); err != nil {
			t.Errorf("the gate directory: %v", err)
		}
	}
	item := f.one(t)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 1 {
		t.Fatalf("item = %+v, resumes = %v, want land_sent with 1 resume", item, f.client.resumes)
	}
	if len(gate.calls) != 1 || !reflect.DeepEqual(gate.calls[0].Command, []string{"make", "gate"}) {
		t.Fatalf("calls = %+v, want one make gate", gate.calls)
	}
	if !reflect.DeepEqual(head.added, []string{relHead}) || !reflect.DeepEqual(head.removed, []string{gate.calls[0].Dir}) {
		t.Fatalf("added = %v, removed = %v, want the head checked out and removed", head.added, head.removed)
	}
	if d := f.lastDetail(t); !strings.Contains(d, "passed on "+relHead) {
		t.Fatalf("detail = %q", d)
	}
}

func TestPromotionGate_AFailingOrTimedOutGateDoesNotLandAndKeepsTheReason(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		out  PromotionGateOutput
		want string
	}{
		"exit code": {PromotionGateOutput{ExitCode: 2, OutputTail: "FAIL internal/x"}, "exit code 2"},
		"timeout":   {PromotionGateOutput{ExitCode: -1, TimedOut: true, OutputTail: "FAIL internal/x"}, "did not finish"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, gate, head := gateFixture(t)
			gate.out = c.out
			item := f.one(t)
			if item.Result != ExecutionResultGateFailed || len(f.client.resumes) != 0 {
				t.Fatalf("item = %+v, resumes = %v, want gate_failed without resume", item, f.client.resumes)
			}
			f.wantOpState(t, "pending")
			d := f.lastDetail(t)
			if !strings.Contains(d, c.want) || !strings.Contains(d, "FAIL internal/x") || !strings.Contains(d, relHead) {
				t.Fatalf("detail = %q", d)
			}
			if len(head.removed) != 1 {
				t.Fatalf("removed = %v", head.removed)
			}
		})
	}
}

func TestPromotionGate_AGateThatCannotRunKeepsTheApprovalAndDoesNotLand(t *testing.T) {
	t.Parallel()
	for name, breakIt := range map[string]func(f *reflFixture, g *fakeGate, h *fakeGateHeadGit){
		"start error":      func(_ *reflFixture, g *fakeGate, _ *fakeGateHeadGit) { g.err = errors.New("no make") },
		"no runner":        func(f *reflFixture, _ *fakeGate, _ *fakeGateHeadGit) { f.gate = nil },
		"no worktree port": func(f *reflFixture, _ *fakeGate, _ *fakeGateHeadGit) { f.headGit = nil },
		"head not fetched": func(_ *reflFixture, _ *fakeGate, h *fakeGateHeadGit) {
			h.has, h.fetchErr = false, errors.New("offline")
		},
		"invalid declaration": func(_ *reflFixture, _ *fakeGate, h *fakeGateHeadGit) { h.declaration = `{"test": "make gate"}` },
		"empty command":       func(_ *reflFixture, _ *fakeGate, h *fakeGateHeadGit) { h.declaration = `{"command": []}` },
		"trailing data":       func(_ *reflFixture, _ *fakeGate, h *fakeGateHeadGit) { h.declaration = gateDeclarationMake + "{}" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, gate, head := gateFixture(t)
			breakIt(f, gate, head)
			item := f.one(t)
			if item.Result != ExecutionResultGateUnavailable || len(f.client.resumes) != 0 {
				t.Fatalf("item = %+v, resumes = %v, want gate_unavailable without resume", item, f.client.resumes)
			}
			f.wantOpState(t, "approved")
			if d := f.lastDetail(t); !strings.Contains(d, "could not run the promotion gate") {
				t.Fatalf("detail = %q", d)
			}
		})
	}
}

func TestPromotionGate_ARepositoryWithoutADeclarationLandsAndRecordsThat(t *testing.T) {
	t.Parallel()
	f, gate, head := gateFixture(t)
	head.declaration = ""
	if item := f.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if len(gate.calls) != 0 || !strings.Contains(f.lastDetail(t), "no promotion gate is declared") {
		t.Fatalf("calls = %v, detail = %q", gate.calls, f.lastDetail(t))
	}
}

func TestPromotionGate_AChallengeWithoutIntegrationIsNotGated(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	gate := &fakeGate{out: PromotionGateOutput{ExitCode: 1}}
	f.gate, f.headGit = gate, &fakeGateHeadGit{declaration: gateDeclarationMake, has: true}
	f.started(t)
	f.queue(reviewFor(relHead))
	if item := f.one(t); item.Result != ExecutionResultLandSent || len(gate.calls) != 0 {
		t.Fatalf("item = %+v, calls = %v, want land_sent without the gate", item, gate.calls)
	}
}

func TestPromotionGate_AnApprovalThatChangesDuringTheGateGetsNoLandAnswer(t *testing.T) {
	t.Parallel()
	f, gate, _ := gateFixture(t)
	gate.during = func(PromotionGateInput) {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET version = version + 1 WHERE id = ?`, f.opID)
			return err
		})
	}
	res := f.exec(t)
	if len(f.client.resumes) != 0 || len(res.Items) != 0 || len(res.NotStarted) != 1 {
		t.Fatalf("resumes = %v, res = %+v, want no resume", f.client.resumes, res)
	}
}

// 検査を回している間（15 分を過ぎても、検査の時間の上限までは）、別の呼び出しはその試行を止まったとみなさない。
func TestPromotionGate_ARunningGateAttemptIsNotStaleAfterTheUsualLimit(t *testing.T) {
	t.Parallel()
	f, gate, _ := gateFixture(t)
	var during []NotStarted
	var calls int
	gate.during = func(PromotionGateInput) {
		calls++
		if calls > 1 {
			return
		}
		opts := f.opts()
		opts.Now = func() time.Time { return f.now.Add(releaseAttemptStaleAfter + time.Minute) }
		res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, opts)
		if err != nil {
			t.Errorf("ExecuteReleases() error = %v", err)
			return
		}
		during = res.NotStarted
	}
	if item := f.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if calls != 1 || len(during) != 1 || during[0].Reason != NotStartedInProgress {
		t.Fatalf("calls = %d, NotStarted = %+v, want the second call to be skipped as in_progress", calls, during)
	}
}
