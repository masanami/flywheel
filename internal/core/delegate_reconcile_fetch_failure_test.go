package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// 完了の照合（成果物の種類 pr）で、GitHub からの取得の失敗と「見つからない」を分ける（#487・
// 決定 2026-11-08 オーナー）。取得の失敗だけなら保留を作らず課題の状態も変えず、連続回数を 1 増やして
// 次の周で照合だけをやり直す。連続 3 回で人間対応待ち。「成果物あり」か「見つからない」で 0 に戻す。
// 既定の報告はブランチ feat/x・pr_urls https://github.com/o/r/pull/1（委譲先 o/r）。

// fetchFailures は課題 C-1 の照合の取得の失敗の連続回数と、記録した調べたブランチ（NULL は ""）。
func (f *delegateFixture) fetchFailures(t *testing.T) (string, string) {
	t.Helper()
	return f.queryString(t, `SELECT reconcile_fetch_failures FROM challenge WHERE id = 1`),
		f.queryString(t, `SELECT reconcile_fetch_branch FROM challenge WHERE id = 1`)
}

// assertUnchangedAfterFetchFailure は、取得の失敗の後に課題が着手中のまま・未回答の保留なし・回数が want であることを確かめる。
func assertUnchangedAfterFetchFailure(t *testing.T, f *delegateFixture, res *DelegateResult, want string) {
	t.Helper()
	d := f.detail(t, "C-1")
	open := 0
	for _, h := range d.Holds {
		if h.Answer == nil {
			open++
		}
	}
	if d.Status != StatusInProgress || open != 0 {
		t.Fatalf("status=%s holds=%+v, want in_progress without an open hold", d.Status, d.Holds)
	}
	if n, _ := f.fetchFailures(t); n != want {
		t.Fatalf("reconcile_fetch_failures = %s, want %s", n, want)
	}
	if len(res.Items) != 1 || res.Items[0].Status != nil {
		t.Fatalf("items = %+v, want one item without a status change", res.Items)
	}
	if !strings.Contains(res.Items[0].Note, "retried in the next cycle") {
		t.Errorf("note = %q, want the retry notice", res.Items[0].Note)
	}
}

// (a) 取得の失敗だけでは保留ができず、状態が変わらず、回数が増える。
func TestReconcileFetchFailure_OnlyFailuresDoNotHoldAndCount(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchangedAfterFetchFailure(t, f, res, "1")
	if _, b := f.fetchFailures(t); b != "feat/x" {
		t.Errorf("reconcile_fetch_branch = %q, want feat/x", b)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM occurrence`); got != "0" {
		t.Errorf("occurrences = %s, want none", got)
	}
}

// (b) 次の周では子を起動せず照合だけをやり直し、3 回続けて失敗したら人間対応待ちにする。理由には
// 取得の失敗・連続の回数・最後のエラーのメッセージが出る。
func TestReconcileFetchFailure_RetriesWithoutLaunchingAndHoldsOnTheThirdFailure(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	for i, want := range []string{"1", "2"} {
		res, err := f.run(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertUnchangedAfterFetchFailure(t, f, res, want)
		if i == 1 && !strings.Contains(res.Items[0].Note, "no delegation was launched") {
			t.Errorf("retry note = %q", res.Items[0].Note)
		}
	}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runs := f.delegateRuns(t); len(runs) != 1 {
		t.Fatalf("delegate runs = %d, want 1 (the retries must not launch a child)", len(runs))
	}
	if n := len(f.deleg.launched()); n != 1 {
		t.Errorf("delegations launched = %d, want 1", n)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 {
		t.Fatalf("status=%s holds=%+v, want a hold on the third failure", d.Status, d.Holds)
	}
	q := d.Holds[0].Question
	for _, w := range []string{"取得の失敗", "連続の回数: 3 回", "上限 3 回", "最後のエラーのメッセージ", "fake gh down", "照合の結果"} {
		if !strings.Contains(q, w) {
			t.Errorf("question lacks %q:\n%s", w, q)
		}
	}
	if strings.Contains(q, "成果物が見つからない") {
		t.Errorf("question must not say the artifact was not found:\n%s", q)
	}
	if rid := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`); rid != "2" {
		t.Errorf("hold.run_id = %q, want the delegation run", rid)
	}
	if res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Errorf("item status = %v", res.Items[0].Status)
	}
	// 3 回とも同じブランチと pr_urls の PR を調べた。
	if got := strings.Count(strings.Join(f.branches.callLog(), "|"), "exists o/r feat/x"); got != 3 {
		t.Errorf("branch lookups = %d, want 3: %v", got, f.branches.callLog())
	}
	if got := len(f.branches.pullCallLog()); got != 3 {
		t.Errorf("pr_urls lookups = %d, want 3", got)
	}
}

// (c) 見つからないと確定した場合（ブランチの 404・PR の空の一覧・pr_urls の 404）は、いまと同じく
// 人間対応待ちにし、問いに「成果物が見つからない」と書く。回数は 0 のまま。
func TestReconcileFetchFailure_ConfirmedNotFoundStillHolds(t *testing.T) {
	f := newReconFixture(t, "impl")
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "成果物が見つからない") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
	for _, w := range []string{"リモートのブランチ: 見つからない", "このブランチを head とする PR: 見つからない", "o/r で見つからない"} {
		if !strings.Contains(d.Holds[0].Question, w) {
			t.Errorf("question lacks %q:\n%s", w, d.Holds[0].Question)
		}
	}
	if n, b := f.fetchFailures(t); n != "0" || b != "" {
		t.Errorf("fetch failures = %s/%q, want 0/NULL", n, b)
	}
}

// (d) 片方が取得の失敗で、もう片方が見つからない場合は、取得の失敗として扱う。
func TestReconcileFetchFailure_MixedFailureAndNotFoundIsAFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *delegateFixture)
	}{
		{"branch lookup fails, no pull requests", func(f *delegateFixture) { f.branches.existsErr = errors.New("error connecting to api.github.com") }},
		{"pull request list fails, branch missing", func(f *delegateFixture) { f.branches.prsErr = errors.New("error connecting to api.github.com") }},
		{"reported pull request fails, branch and list empty", func(f *delegateFixture) {
			f.branches.pullErrs["o/r#1"] = errors.New("HTTP 502")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newReconFixture(t, "impl")
			c.setup(f)
			res, err := f.run(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertUnchangedAfterFetchFailure(t, f, res, "1")
		})
	}
}

// (e) 引いたもののどれか 1 つで成果物が見つかれば、他が取得の失敗でも成果物あり（検証中・回数 0）。
func TestReconcileFetchFailure_AnyFoundArtifactWins(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *delegateFixture)
	}{
		{"branch lookup fails, head pull request open", func(f *delegateFixture) {
			f.branches.existsErr = errFakeGH
			f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(2, "open", "develop")}
		}},
		{"head list fails, reported pull request open", func(f *delegateFixture) {
			f.branches.prsErr = errFakeGH
			f.givePull(pullPR("o/r", 1, "open", "develop"))
		}},
		{"reported pull request fails, head pull request merged", func(f *delegateFixture) {
			f.branches.pullErrs["o/r#1"] = errFakeGH
			f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(2, "merged", "develop")}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newReconFixture(t, "impl")
			c.setup(f)
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.status(t); got != StatusVerifying {
				t.Fatalf("status = %s, want verifying", got)
			}
			if n, _ := f.fetchFailures(t); n != "0" {
				t.Errorf("reconcile_fetch_failures = %s, want 0", n)
			}
		})
	}
}

// (f) やり直しで「成果物あり」か「見つからない」の判定が出たら、回数を 0 に戻し、ブランチを消す。
// 見つかった成果物は、元の委譲の run の成果物として記録する。
func TestReconcileFetchFailure_ResetOnFoundOrNotFound(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.err = errFakeGH
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		f.branches.err = nil
		f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(2, "open", "develop")}
		res, err := f.run(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.status(t); got != StatusVerifying {
			t.Fatalf("status = %s, want verifying", got)
		}
		if n, b := f.fetchFailures(t); n != "0" || b != "" {
			t.Errorf("fetch failures = %s/%q, want 0/NULL", n, b)
		}
		if len(f.delegateRuns(t)) != 1 {
			t.Errorf("a new delegation was launched")
		}
		if res.Items[0].RunID != "R-2" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusVerifying) {
			t.Errorf("item = %+v", res.Items[0])
		}
		if got := f.queryString(t, `SELECT channel || '/' || run_id FROM activity WHERE entity = 'challenge' AND action = 'submit'`); got != "invoker/2" {
			t.Errorf("submit activity = %q, want invoker/2", got)
		}
		prs := f.prArtifacts(t)
		if len(prs) != 1 || prs[0].Ref != mkPR(2, "open", "develop").URL {
			t.Errorf("pr artifacts = %+v", prs)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.err = errFakeGH
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		f.branches.err = nil
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, "C-1")
		if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "成果物が見つからない") {
			t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
		}
		if n, b := f.fetchFailures(t); n != "0" || b != "" {
			t.Errorf("fetch failures = %s/%q, want 0/NULL", n, b)
		}
	})
	t.Run("a failure after a reset counts from 1 again", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.branches.err = errFakeGH
		for i := 0; i < 2; i++ {
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
		}
		// 回数が 2 のときに「見つからない」で 0 に戻り、回答で再開した子の次の照合の失敗は 1 から数える。
		f.branches.err = nil
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if n, _ := f.fetchFailures(t); n != "0" {
			t.Fatalf("reconcile_fetch_failures = %s, want 0", n)
		}
		f.answerLatestHold(t, "C-1", "PR を作り直して")
		f.branches.err = errFakeGH
		res, err := f.run(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.delegateRuns(t)) != 2 {
			t.Fatalf("delegate runs = %d, want 2 (resumed after the answer)", len(f.delegateRuns(t)))
		}
		assertUnchangedAfterFetchFailure(t, f, res, "1")
	})
}

// (g) pr_urls の経路でも同じ区別になる: 報告にブランチが無く、pr_urls の PR の取得だけが失敗したら
// 取得の失敗（保留なし・回数 1）。gh が別の PR を返したのも取得の失敗。次の周のやり直しで見つかれば成果物あり。
func TestReconcileFetchFailure_PRURLsPath(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *delegateFixture)
	}{
		{"fetch failure", func(f *delegateFixture) { f.branches.pullErrs["o/s#5"] = errors.New("boom 502") }},
		{"gh returns a different pull request", func(f *delegateFixture) { f.branches.pulls["o/s#5"] = pullPR("x/y", 5, "open", "main") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newReconFixture(t, "impl")
			f.reportPRs("https://github.com/o/s/pull/5")
			c.setup(f)
			res, err := f.run(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertUnchangedAfterFetchFailure(t, f, res, "1")
			if _, b := f.fetchFailures(t); b != "" {
				t.Errorf("reconcile_fetch_branch = %q, want NULL (no branch was looked up)", b)
			}

			delete(f.branches.pullErrs, "o/s#5")
			f.givePull(pullPR("o/s", 5, "open", "main"))
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.status(t); got != StatusVerifying {
				t.Fatalf("status after the retry = %s, want verifying", got)
			}
			if got := f.branches.pullCallLog(); strings.Join(got, "|") != "pr o/s 5|pr o/s 5" {
				t.Errorf("pull lookups = %v", got)
			}
			if got := f.branches.branchCallLog(); len(got) != 0 {
				t.Errorf("branch lookups = %v, want none", got)
			}
			if n, _ := f.fetchFailures(t); n != "0" {
				t.Errorf("reconcile_fetch_failures = %s, want 0", n)
			}
		})
	}
}

// 報告にブランチが無くスロットのブランチを調べた照合のやり直しは、同じスロットのブランチを調べる
// （スロットは解放済みで、やり直しでは検査しない）。
func TestReconcileFetchFailure_RetryUsesTheSlotBranchOfTheFirstReconciliation(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.setSlotBranch("feat/slot")
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) {
		m["branch"] = nil
		m["pr_urls"] = []string{}
	})
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if _, b := f.fetchFailures(t); b != "feat/slot" {
		t.Fatalf("reconcile_fetch_branch = %q, want feat/slot", b)
	}
	f.setSlotBranchNow("main") // スロットは別の状態になっていても、やり直しは記録したブランチを調べる
	f.branches.err = nil
	f.branches.prs["feat/slot"] = []UpstreamPullRequest{mkPR(3, "open", "develop")}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	log := strings.Join(f.branches.branchCallLog(), "|")
	if log != "exists o/r feat/slot|prs o/r feat/slot|exists o/r feat/slot|prs o/r feat/slot" {
		t.Errorf("branch lookups = %q", log)
	}
}

// ID を指定した `run C-1` でも、照合のやり直しの対象なら子を起動せず照合だけをやり直す。
func TestReconcileFetchFailure_RunWithIDAlsoRetriesOnly(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	id := "C-1"
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchangedAfterFetchFailure(t, f, res, "2")
	if len(f.delegateRuns(t)) != 1 {
		t.Errorf("a new delegation was launched")
	}
}

// 成果物の種類が pr 以外の照合は変えない（branch の取得の失敗は、これまでどおり保留にする）。
func TestReconcileFetchFailure_BranchArtifactsAreUnchanged(t *testing.T) {
	f := newReconFixture(t, "impl-branch")
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "fake gh down") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
	if n, _ := f.fetchFailures(t); n != "0" {
		t.Errorf("reconcile_fetch_failures = %s, want 0", n)
	}
}

// 上限の保留に回答して再開した子の照合の失敗は、前の run の回数を引き継がず 1 から数える
// （回数は失敗した照合の run ごとの連続の失敗）。
func TestReconcileFetchFailure_ANewRunAfterTheLimitHoldCountsFromOne(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	for i := 0; i < 3; i++ {
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.status(t); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human after 3 failures", got)
	}
	f.answerLatestHold(t, "C-1", "もう一度確かめて")
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runs := f.delegateRuns(t); len(runs) != 2 {
		t.Fatalf("delegate runs = %d, want 2 (resumed with the answer)", len(runs))
	}
	assertUnchangedAfterFetchFailure(t, f, res, "1")
	if got := f.queryString(t, `SELECT reconcile_fetch_run_id FROM challenge WHERE id = 1`); got != "3" {
		t.Errorf("reconcile_fetch_run_id = %s, want the resumed run 3", got)
	}
}

// 失敗した照合の run より後に J5 の run がある（人が提出して J5 が差し戻した）課題は、照合のやり直しの
// 対象にしない（通常の起動の判定に任せ、差し戻しの再開を奪わない）。
func TestReconcileFetchFailure_NotRetriedAfterAJ5Run(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	now := formatTimestamp(time.Now())
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (1, 'judgment', 'J5', 1, 's', 1, 'h', ?, ?, ?, 'succeeded', 1000000, 'judgment')`, now, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var p *pendingReconcileRetry
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		p, err = loadPendingReconcileRetry(context.Background(), tx, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("pending retry = %+v, want none after a J5 run", p)
	}
}

// やり直しの時点で報告が宣言の検査を通らなくなっていたら、着手中のまま残さず人間対応待ちにする。
func TestReconcileFetchFailure_UnretriableReportHolds(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.err = errFakeGH
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET output = '{"outcome":"broken"}' WHERE id = 2`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "照合をやり直せない") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
	if len(f.delegateRuns(t)) != 1 {
		t.Errorf("a new delegation was launched")
	}
	if res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Errorf("item = %+v", res.Items[0])
	}
}
