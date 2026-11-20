package core

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"testing"
)

// このファイルは form: cli の接続ツールの run が成果物つきで検証中へ進むときの release の作成と置き換え、および本番反映の後の
// ready と観測の待ちを、実ストア・偽の ContractClient・偽の PR の取得で固定する。

const reviewPRURL2 = "https://github.com/o/r/pull/2"

func (f *reviewFlowFixture) setPR(number int, url, state, base string) {
	f.branches.pulls[releaseKey(number)] = UpstreamPullRequest{URL: url, Title: "PR TITLE", State: state, Base: base}
}

func releaseKey(number int) string { return "o/r#" + strconv.Itoa(number) }

type releaseRow struct{ ref, state, reason string }

func (f *contractFixture) releases(t *testing.T) []releaseRow {
	t.Helper()
	var out []releaseRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT COALESCE(ref, ''), state, COALESCE(closed_reason, '') FROM operation WHERE kind = 'release' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r releaseRow
			if err := rows.Scan(&r.ref, &r.state, &r.reason); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func reviewWaitOutFor(url string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: []string{"respond", "ready", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: url}, reviewContentArtifact}
	})
}

// (1) レビュー待ちを取り込んで検証中へ進めたとき、その PR を指す release が 1 件作られる。同じ PR を読み直しても増えない。
func TestPollContractRuns_ReviewWaitCreatesOneReleaseForTheDefaultBranchPR(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	want := []releaseRow{{ref: reviewPRURL, state: "pending"}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
}

// 宛先が既定ブランチでない PR・宣言にないリポジトリの PR には release を作らない（検証中へは進む）。
func TestPollContractRuns_ReviewWaitDoesNotCreateReleaseForANonDefaultBasePR(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "develop")
	f.toVerifying(t)
	if got := f.releases(t); len(got) != 0 {
		t.Fatalf("releases = %+v, want none for a PR whose base is not the default branch", got)
	}
}

// PR の取得に失敗したときは release の無い検証中を作らず、課題は着手中のまま次の周で読み直す。
func TestPollContractRuns_ReviewWaitWithPRLookupFailureDoesNotVerifyAndIsRetried(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.branches.pullErrs[releaseKey(1)] = context.DeadlineExceeded
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300), Reconcile: f.branches}) //nolint:errcheck
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress when the PR lookup failed", got)
	}
	delete(f.branches.pullErrs, releaseKey(1))
	f.setPR(1, reviewPRURL, "open", "main")
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying after the retry", got)
	}
	if got := f.releases(t); len(got) != 1 {
		t.Fatalf("releases = %+v, want one", got)
	}
}

// (2) 差し戻しの後に別の PR になったとき、前の release は閉じられ（superseded）、新しい release だけが承認の対象になる。
func TestPollContractRuns_ANewPRAfterReworkSupersedesThePreviousRelease(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.setPR(2, reviewPRURL2, "open", "main")
	f.j5.checks.prs["o/r#2"] = prChecks("open", checkDone)
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(reviewPRURL2)}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	want := []releaseRow{{reviewPRURL, "closed", "superseded"}, {reviewPRURL2, "pending", ""}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
	// 承認の対象は新しい release だけ。
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	prev, err := f.s.PrepareApproval(context.Background(), f.cid)
	if err != nil || len(prev.PendingReleases) != 1 || prev.PendingReleases[0].Ref == nil || *prev.PendingReleases[0].Ref != reviewPRURL2 {
		t.Fatalf("PrepareApproval = %+v err %v, want only the release for PR 2", prev, err)
	}
}

// 直す前から残っている誤った release（閉じた古い PR を指したまま）も、次にレビュー待ちを読んだときに置き換わる。
func TestPollContractRuns_AStaleReleaseLeftFromBeforeIsReplacedOnTheNextRead(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(2, reviewPRURL2, "open", "main")
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	insertReleaseForTest(t, f.s, f.cid, reviewPRURL, OperationStatePending, "")
	f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(reviewPRURL2)}
	f.poll(t)
	want := []releaseRow{{reviewPRURL, "closed", "superseded"}, {reviewPRURL2, "pending", ""}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
}

// (3) 完了の承認から本番反映のマージ、ready を返すまでが手の補い無しに通る。
func TestPollContractRuns_ApprovalMergeAndReadyRunWithoutManualRelease(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	ctx := context.Background()
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	prev, err := f.s.PrepareApproval(ctx, f.cid)
	if err != nil || len(prev.PendingReleases) != 1 {
		t.Fatalf("PrepareApproval = %+v err %v, want the release made by the flow", prev, err)
	}
	head := "abc123"
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	open := openPR()
	open.HeadSHA = head
	merged := open
	merged.State = "merged"
	merged.MergeCommitSHA = "merge-sha"
	fake := &fakeRelease{pr: open, afterPR: &merged}
	res, err := f.s.ExecuteReleases(ctx, nil, ReleaseExecOptions{Checks: fake, Merger: fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Trigger: ReleaseExecTriggerOpExec})
	if err != nil || len(fake.mergeArgs) != 1 {
		t.Fatalf("ExecuteReleases = %+v err %v merges %v, want one merge", res, err, fake.mergeArgs)
	}
	f.client.resumeOut = contractOut(ContractStateSucceeded, nil, nil)
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "ready", "--note", ""}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
	if state, _, _, ended := f.contractRow(t); state != "succeeded" || !ended {
		t.Fatalf("contract_run state=%s ended=%v, want succeeded and ended", state, ended)
	}
}

// (4) ready の後に観測の待ち（human-merge など）を返す接続ツールでも、課題が完了のまま、答え無しの resume で読み進めて run が succeeded になる。
func TestPollContractRuns_ObserveAfterReadyOnADoneChallengeIsResumedUntilSucceeded(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	insertReleaseForTest(t, f.s, f.cid, reviewPRURL, OperationStateExecuted, "merge-sha")
	n, _ := parseChallengeID(f.cid)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(StatusDone), n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	observe := contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "observe", Decider: "any", Text: "マージを見ている"}
	})
	f.client.resumeOut = observe
	f.poll(t) // ready を返す。接続ツールは観測の待ちを返す。
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %v, want the ready only", f.client.resumes)
	}
	// 次の周: 状態を読み直し、まだ観測の待ちなら答え無しの resume を呼ぶ（課題は完了のまま）。
	f.client.queue = []ContractLaunchOutput{observe}
	f.client.resumeOut = contractOut(ContractStateSucceeded, nil, nil)
	f.poll(t)
	if len(f.client.resumes) != 2 || !reflect.DeepEqual(f.client.resumes[1], []string{"tool", "resume", "ext-1", "--choice", "", "--note", ""}) {
		t.Fatalf("resumes = %v, want an answerless resume after the ready", f.client.resumes)
	}
	if state, _, _, ended := f.contractRow(t); state != "succeeded" || !ended {
		t.Fatalf("contract_run state=%s ended=%v, want succeeded and ended", state, ended)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Fatalf("status = %s, want done", got)
	}
}

// 差し戻しで PR1 → PR2 → PR1 と戻ったとき、承認できる release が作り直される。
func TestPollContractRuns_ReturningToAnEarlierPRRecreatesAnApprovableRelease(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.setPR(2, reviewPRURL2, "open", "main")
	f.j5.checks.prs["o/r#2"] = prChecks("open", checkDone)
	f.toVerifying(t)
	for _, url := range []string{reviewPRURL2, reviewPRURL} {
		if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
			t.Fatal(err)
		}
		f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
		f.poll(t)
		f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(url)}
		f.poll(t)
	}
	pending := 0
	for _, r := range f.releases(t) {
		if r.state == "pending" && r.ref == reviewPRURL {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("releases = %+v, want one pending release for PR 1", f.releases(t))
	}
}

// succeeded の写しでも release を作って検証中へ進む。PR の取得に失敗しても、課題を着手中のまま取り残さず検証中へ進める。
func TestPollContractRuns_SucceededCreatesReleaseAndStillVerifiesWhenTheLookupFails(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		f := newReviewFlowFixture(t)
		if fail {
			f.branches.pullErrs[releaseKey(1)] = context.DeadlineExceeded
		} else {
			f.setPR(1, reviewPRURL, "open", "main")
		}
		if _, err := f.start(t); err != nil {
			t.Fatal(err)
		}
		f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) {
			st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, reviewContentArtifact}
		})}
		f.poll(t)
		if got := f.status(t, f.cid); got != StatusVerifying {
			t.Fatalf("fail=%v status = %s, want verifying", fail, got)
		}
		if n := len(f.releases(t)); (n == 1) == fail {
			t.Fatalf("fail=%v releases = %d", fail, n)
		}
	}
}

func reviewWaitOutForAll(urls ...string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: []string{"respond", "ready", "land"}}
		for _, u := range urls {
			st.Artifacts = append(st.Artifacts, ContractArtifact{Kind: "pr", Ref: u})
		}
		st.Artifacts = append(st.Artifacts, reviewContentArtifact)
	})
}

// 同じ PR のレビュー待ちを 2 回読んでも（差し戻しの後に同じ PR でまたレビュー待ちになっても）、release は 1 件のまま。
func TestPollContractRuns_ReadingTheSamePRTwiceKeepsOneRelease(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	// 差し戻し（J5 が満たさない）で着手中へ戻し、同じ PR のレビュー待ちをもう一度読む。
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(reviewPRURL)}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	want := []releaseRow{{ref: reviewPRURL, state: "pending"}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
}

// 1 回のレビュー待ちで同じ成果物（同じ PR）が重なって届いても、release は 1 件。
func TestPollContractRuns_ReviewWaitWithTheSamePRListedTwiceCreatesOneRelease(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOutForAll(reviewPRURL, reviewPRURL)}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	want := []releaseRow{{ref: reviewPRURL, state: "pending"}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
}

// 1 回のレビュー待ちに PR が複数あっても、既定ブランチ（main）宛の PR が 1 つなら release をその 1 件だけ作り、main 宛でない PR には作らない。
func TestPollContractRuns_ReviewWaitWithOneDefaultBranchPRAmongSeveralCreatesOneRelease(t *testing.T) {
	t.Parallel()
	const reviewPRURL3 = "https://github.com/o/r/pull/3"
	f := newReviewFlowFixture(t)
	f.setPR(1, reviewPRURL, "open", "main")
	f.setPR(3, reviewPRURL3, "open", "develop")
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOutForAll(reviewPRURL, reviewPRURL3)}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	want := []releaseRow{{ref: reviewPRURL, state: "pending"}}
	if got := f.releases(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %+v, want %+v（main 宛でない PR 3 には作らない）", got, want)
	}
}
