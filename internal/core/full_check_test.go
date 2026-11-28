package core

// このファイルは、統合ブランチを反映先へ上げる前のフルの検査（full_check.go）を、実ストア・偽の契約のコマンド・
// 偽の検査の起動と作業ツリーの口で検証する。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeFullCheck struct {
	out   FullCheckOutcome
	calls []FullCheckRequest
}

func (f *fakeFullCheck) RunFullCheck(_ context.Context, req FullCheckRequest) FullCheckOutcome {
	f.calls = append(f.calls, req)
	return f.out
}

type fakeHeadGit struct {
	missing  bool
	fetchErr error
	added    []string
	removed  []string
}

func (g *fakeHeadGit) HasCommit(context.Context, string, string) (bool, error) {
	return !g.missing, nil
}
func (g *fakeHeadGit) FetchHead(context.Context, string, string, string) error { return g.fetchErr }
func (g *fakeHeadGit) ListHeadRefs(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (g *fakeHeadGit) DeleteHeadRef(context.Context, string, string) error { return nil }
func (g *fakeHeadGit) AddHeadWorktree(_ context.Context, _, _, sha string) error {
	g.added = append(g.added, sha)
	return nil
}
func (g *fakeHeadGit) RemoveHeadWorktree(_ context.Context, _, path string) error {
	g.removed = append(g.removed, path)
	return nil
}

// execFullCheck は、宣言に full_check を足した接続の宣言で、反映の段を 1 回実行する。
func execFullCheck(t *testing.T, f *reflFixture, runner FullCheckRunner, git HeadWorktreeGit) ReleaseExecItem {
	t.Helper()
	opts := f.opts()
	conn := *opts.Connectors
	conn.Repos = append([]ConnectorRepo(nil), conn.Repos...)
	for i := range conn.Repos {
		conn.Repos[i].FullCheck = []string{"make", "gate"}
	}
	opts.Connectors, opts.FullCheck, opts.HeadGit = &conn, runner, git
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, opts)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ExecuteReleases = %+v, %v, want one item", res, err)
	}
	return res.Items[0]
}

func newFullCheckFixture(t *testing.T) *reflFixture {
	t.Helper()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	return f
}

func TestFullCheck_PassingRunsAtTheIntegrationHeadBeforeLanding(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	check, git := &fakeFullCheck{out: FullCheckOutcome{Passed: true}}, &fakeHeadGit{}
	if item := execFullCheck(t, f, check, git); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if len(check.calls) != 1 || strings.Join(check.calls[0].Command, " ") != "make gate" || check.calls[0].Dir == "" ||
		check.calls[0].Timeout != fullCheckTimeout {
		t.Fatalf("calls = %+v", check.calls)
	}
	if len(git.added) != 1 || git.added[0] != relHead || len(git.removed) == 0 || git.removed[len(git.removed)-1] != check.calls[0].Dir {
		t.Fatalf("added %v removed %v, want the worktree of %s created and removed", git.added, git.removed, relHead)
	}
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %v, want the land answer once", f.client.resumes)
	}
}

func TestFullCheck_FailureDoesNotPromoteAndRecordsTheReason(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	check, git := &fakeFullCheck{out: FullCheckOutcome{Detail: "exit status 2\nFAIL internal/core"}}, &fakeHeadGit{}
	if item := execFullCheck(t, f, check, git); item.Result != ExecutionResultFullCheckFailed {
		t.Fatalf("item = %+v, want full_check_failed", item)
	}
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want no land answer", f.client.resumes)
	}
	f.wantOpState(t, "pending")
	at := f.attempts(t)
	last := at[len(at)-1]
	if last.Result != ExecutionResultFullCheckFailed || !strings.Contains(last.Detail, "make gate") ||
		!strings.Contains(last.Detail, relHead) || !strings.Contains(last.Detail, "FAIL internal/core") {
		t.Fatalf("attempt = %+v, want the reason recorded", last)
	}
}

// 検査を用意できないときは上げずに、承認を保って待つ。
func TestFullCheck_UnpreparableCheckDoesNotPromote(t *testing.T) {
	t.Parallel()
	pass := &fakeFullCheck{out: FullCheckOutcome{Passed: true}}
	cases := []struct {
		name   string
		runner FullCheckRunner
		git    HeadWorktreeGit
	}{
		{"no runner", nil, &fakeHeadGit{}},
		{"no git", pass, nil},
		{"fetch fails", pass, &fakeHeadGit{missing: true, fetchErr: context.DeadlineExceeded}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFullCheckFixture(t)
			if item := execFullCheck(t, f, c.runner, c.git); item.Result != ExecutionResultUpstreamFailed {
				t.Fatalf("item = %+v, want upstream_failed", item)
			}
			if len(f.client.resumes) != 0 {
				t.Fatalf("resumes = %v, want no land answer", f.client.resumes)
			}
			f.wantOpState(t, "approved")
		})
	}
	if len(pass.calls) != 0 {
		t.Fatalf("calls = %+v, want none", pass.calls)
	}
}

// 検査を通した中身へ「入れてよい」を返し直すときは、フルの検査をもう一度は実行しない。
func TestFullCheck_ResendingLandForThePassedContentDoesNotRunItAgain(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	first := &fakeFullCheck{out: FullCheckOutcome{Passed: true}}
	if item := execFullCheck(t, f, first, &fakeHeadGit{}); item.Result != ExecutionResultLandUnconfirmed || len(first.calls) != 1 {
		t.Fatalf("item = %+v calls = %+v, want land_unconfirmed after one check", item, first.calls)
	}

	f.now = f.now.Add(time.Second)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.queue(reviewFor(relHead))
	again, git := &fakeFullCheck{}, &fakeHeadGit{}
	if item := execFullCheck(t, f, again, git); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if len(again.calls) != 0 || len(git.added) != 0 {
		t.Fatalf("calls = %+v added = %v, want the full check not to run again", again.calls, git.added)
	}
	if len(f.client.resumes) != 2 {
		t.Fatalf("resumes = %v, want the land answer sent again", f.client.resumes)
	}
}

// 統合先を使わない課題は、タスクの PR でフルの検査を回すので、反映の段では実行しない。
func TestFullCheck_ChallengeWithoutIntegrationIsNotChecked(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	check := &fakeFullCheck{}
	if item := execFullCheck(t, f, check, &fakeHeadGit{}); item.Result != ExecutionResultLandSent || len(check.calls) != 0 {
		t.Fatalf("item = %+v calls = %+v, want land_sent without the full check", item, check.calls)
	}
}

func TestLoadConnectorsDeclaration_FullCheckMustBeANonEmptyArray(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{`null`, `[]`, `[""]`, `"make gate"`} {
		if _, err := parseConnectorRepos([]byte(`[{"full_check":` + bad + `}]`)); err == nil {
			t.Errorf("full_check %s: accepted", bad)
		}
	}
	repos, err := parseConnectorRepos([]byte(`[{"full_check":["make","gate"]}]`))
	if err != nil || strings.Join(repos[0].FullCheck, " ") != "make gate" {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
}

type funcFullCheck func(context.Context) FullCheckOutcome

func (f funcFullCheck) RunFullCheck(ctx context.Context, _ FullCheckRequest) FullCheckOutcome {
	return f(ctx)
}

// duringFullCheck は、試行を始めた時刻からの経過を elapsed で進められる反映の段を 1 回実行する。inCheck は検査の中で呼ぶ。
func duringFullCheck(t *testing.T, f *reflFixture, inCheck func(ctx context.Context, opts ReleaseExecOptions, elapsed *atomic.Int64)) (*ReleaseExecResult, error) {
	t.Helper()
	opts := f.opts()
	conn := *opts.Connectors
	conn.Repos = append([]ConnectorRepo(nil), conn.Repos...)
	for i := range conn.Repos {
		conn.Repos[i].FullCheck = []string{"make", "gate"}
	}
	start := f.now
	var elapsed atomic.Int64
	opts.Connectors, opts.HeadGit, opts.fullCheckRenewEvery = &conn, &fakeHeadGit{}, time.Millisecond
	opts.Now = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	opts.FullCheck = funcFullCheck(func(ctx context.Context) FullCheckOutcome {
		inCheck(ctx, opts, &elapsed)
		return FullCheckOutcome{Passed: true}
	})
	return f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, opts)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for %s", what)
			return
		}
	}
}

// 試行を始めてから releaseAttemptStaleAfter を過ぎても、検査を実行している試行は他の呼び出しに閉じられない。
func TestFullCheck_RunningAttemptStaysAliveBeyondTheStaleLimit(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	before := len(f.attempts(t))
	var checks int
	res, err := duringFullCheck(t, f, func(_ context.Context, opts ReleaseExecOptions, elapsed *atomic.Int64) {
		checks++
		if checks > 1 {
			return
		}
		for _, at := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
			elapsed.Store(int64(at))
			want := opts.Now()
			waitFor(t, "the attempt to be renewed", func() bool {
				a := f.attempts(t)
				return a[len(a)-1].open() && !a[len(a)-1].RenewedAt.Before(want)
			})
		}
		other, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, opts)
		if err != nil || len(other.Items) != 0 || len(other.NotStarted) != 1 || other.NotStarted[0].Reason != NotStartedInProgress {
			t.Errorf("concurrent ExecuteReleases = %+v, %v, want in_progress", other, err)
		}
	})
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultLandSent {
		t.Fatalf("ExecuteReleases = %+v, %v, want land_sent", res, err)
	}
	if checks != 1 || len(f.client.resumes) != 1 {
		t.Fatalf("checks = %d resumes = %v, want one check and one land answer", checks, f.client.resumes)
	}
	at := f.attempts(t)
	if len(at) != before+1 || at[len(at)-1].Result != ExecutionResultLandSent || !at[len(at)-1].StartedAt.Equal(f.now) {
		t.Fatalf("attempts = %+v, want one new attempt that keeps its start time", at[len(at)-1])
	}
}

// 検査の間に試行が閉じられていたら、検査を取り消し、「入れてよい」を返さない。
func TestFullCheck_ClosedAttemptCancelsTheCheckAndDoesNotLand(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	res, err := duringFullCheck(t, f, func(ctx context.Context, _ ReleaseExecOptions, _ *atomic.Int64) {
		a := f.attempts(t)
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			return closeOperationExecution(context.Background(), tx, a[len(a)-1].ID,
				closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultInterrupted})
		})
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the check was not cancelled")
		}
	})
	if err != nil || len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedInProgress {
		t.Fatalf("ExecuteReleases = %+v, %v, want in_progress", res, err)
	}
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want no land answer", f.client.resumes)
	}
}

// 検査の間に呼び出し元の ctx が取り消されたら（Ctrl-C 等）、検査にも取り消しが届き、試行は interrupted で閉じる。
// 承認は保ち、「入れてよい」は返さない。
func TestFullCheck_CancelledCallerClosesTheAttemptAsInterrupted(t *testing.T) {
	t.Parallel()
	f := newFullCheckFixture(t)
	opts := f.opts()
	conn := *opts.Connectors
	conn.Repos = append([]ConnectorRepo(nil), conn.Repos...)
	for i := range conn.Repos {
		conn.Repos[i].FullCheck = []string{"make", "gate"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts.Connectors, opts.HeadGit = &conn, &fakeHeadGit{}
	opts.FullCheck = funcFullCheck(func(checkCtx context.Context) FullCheckOutcome {
		cancel()
		select {
		case <-checkCtx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the check was not cancelled")
		}
		return FullCheckOutcome{Detail: "signal: killed"}
	})
	if _, err := f.s.ExecuteReleases(ctx, []string{formatOperationID(f.opID)}, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteReleases error = %v, want context.Canceled", err)
	}
	at := f.attempts(t)
	if last := at[len(at)-1]; last.open() || last.Result != ExecutionResultInterrupted {
		t.Fatalf("attempt = %+v, want closed as interrupted", last)
	}
	f.wantOpState(t, "approved")
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want no land answer", f.client.resumes)
	}
}
