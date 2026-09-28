package github

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// TestGetIssueThread_PaginatesCommentsAcrossTwoPages は AC-87 の取得側と
// コメントのページ送りを Client.GetIssueThread に対して直接検証する
// （本文は既存 GetIssue の経路の再利用、コメントは 100 件・30 件の 2 ページ、
// 計 130 件が順序どおりに返る）。
func TestGetIssueThread_PaginatesCommentsAcrossTwoPages(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "thread_comments_paginated")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	thread, err := c.GetIssueThread(context.Background(), "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("GetIssueThread: %v", err)
	}
	if thread.Issue.ExternalKey != "masanami/flywheel#100" {
		t.Fatalf("Issue.ExternalKey = %q", thread.Issue.ExternalKey)
	}
	if len(thread.Comments) != 130 {
		t.Fatalf("len(Comments) = %d, want 130 (100 + 30 across two pages)", len(thread.Comments))
	}
	for i, comment := range thread.Comments {
		wantBody := fmt.Sprintf("comment %d", i+1)
		if comment.Body != wantBody {
			t.Fatalf("Comments[%d].Body = %q, want %q (API order must be preserved)", i, comment.Body, wantBody)
		}
		if comment.Author != "commenter" {
			t.Fatalf("Comments[%d].Author = %q, want commenter", i, comment.Author)
		}
	}

	calls := readInvocationLog(t, logPath)
	if len(calls) != 3 {
		t.Fatalf("gh was invoked %d times, want 3 (1 issue GET + 2 comment pages)", len(calls))
	}
	// self-review 指摘の修正確認: 偽の gh はコメント取得の要求先（repo・
	// Issue 番号）を検証していなかった。listAllComments が別の Issue や repo
	// のコメントを取りに行っても偽の gh 側では検出できないため、この
	// アサーションで invocation log の側から要求先を直接確かめる。
	//
	// round2 self-review 指摘: 部分一致（strings.Contains）だと、番号が
	// "100" を含む別の番号（例: 1000・1100）への誤った要求も通ってしまう。
	// 本体は完全一致、コメントは "?" までの接頭辞一致で判定する。
	for i, call := range calls {
		endpoint := call[len(call)-1]
		if i == 0 {
			if endpoint != "repos/masanami/flywheel/issues/100" {
				t.Fatalf("call[0] endpoint = %q, want exactly repos/masanami/flywheel/issues/100", endpoint)
			}
			continue
		}
		if !strings.HasPrefix(endpoint, "repos/masanami/flywheel/issues/100/comments?") {
			t.Fatalf("call[%d] endpoint = %q, want a comments page for issues/100", i, endpoint)
		}
	}
	// UpstreamThreadSource.GetIssueThread の契約（#85 の申し送り。観測値は
	// コメント一覧の取得より前に得たものであること）どおり、Issue 本体の
	// 取得（-i 付き）がコメントのページ取得より先に行われることを確かめる。
	if len(calls[0]) < 2 || calls[0][1] != "-i" {
		t.Fatalf("first call = %v, want the issue GET (with -i) to happen before comment pages", calls[0])
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_AC88_BareSameRepoForm は AC-88 の "#<番号>"
// （同じリポジトリ）の書き方を core.FetchUpstreamContext 経由で検証する。
func TestFetchUpstreamContext_AC88_BareSameRepoForm(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_ac88_bare")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 1 {
		t.Fatalf("References = %+v, want 1 entry", got.References)
	}
	ref := got.References[0]
	if !ref.Fetched || ref.Reference.Repo != "masanami/flywheel" || ref.Reference.Number != 50 || ref.Issue.Body != "target body bare" {
		t.Fatalf("References[0] = %+v, want a fetched masanami/flywheel#50 with the fixture body", ref)
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_AC88_OwnerNameForm は AC-88 の
// "<owner>/<name>#<番号>" の書き方を検証する。
func TestFetchUpstreamContext_AC88_OwnerNameForm(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_ac88_ownername")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 1 {
		t.Fatalf("References = %+v, want 1 entry", got.References)
	}
	ref := got.References[0]
	if !ref.Fetched || ref.Reference.Repo != "other/repo" || ref.Reference.Number != 7 || ref.Issue.Body != "target body ownername" {
		t.Fatalf("References[0] = %+v, want a fetched other/repo#7 with the fixture body", ref)
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_AC88_URLForm は AC-88 の
// "https://github.com/<owner>/<name>/issues/<番号>" の書き方を検証する。
func TestFetchUpstreamContext_AC88_URLForm(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_ac88_url")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 1 {
		t.Fatalf("References = %+v, want 1 entry", got.References)
	}
	ref := got.References[0]
	if !ref.Fetched || ref.Reference.Repo != "third/repo" || ref.Reference.Number != 9 || ref.Issue.Body != "target body url" {
		t.Fatalf("References[0] = %+v, want a fetched third/repo#9 with the fixture body", ref)
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_AC89_SixReferences_OnlyFirstFiveFetchedThroughRealClient
// は AC-89 を実物の Client 経由で検証する: 6 件の参照があるとき、gh へは最初の
// 5 件（#201〜#205）だけが要求され、#206 は一度も要求されない。
func TestFetchUpstreamContext_AC89_SixReferences_OnlyFirstFiveFetchedThroughRealClient(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_ac89_six_refs")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 5 {
		t.Fatalf("len(References) = %d, want 5", len(got.References))
	}

	calls := readInvocationLog(t, logPath)
	for _, call := range calls {
		endpoint := call[len(call)-1]
		if strings.Contains(endpoint, "issues/206") {
			t.Fatalf("the 6th reference (#206) must never be requested, calls = %v", calls)
		}
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_ReferenceToPullRequest_IsSkippedNotFetched は
// self-review 指摘の修正確認: "#<番号>" が Pull Request を指していた場合、
// core の語彙に PR が無い（§機能全体の設計）のと URL 形式が明示的に "/pull/"
// を対象外にしているのに揃え、GetReferencedIssue はこれを「見つからない」
// として FetchUpstreamContext のスキップ経路に乗せる（PR の本文を Issue の
// 参照として取り込まない）。
func TestFetchUpstreamContext_ReferenceToPullRequest_IsSkippedNotFetched(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_reference_is_pull_request")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v (a PR reference must be skipped, not fail the whole fetch)", err)
	}
	if len(got.References) != 1 || got.References[0].Fetched || got.References[0].Reference.Number != 601 {
		t.Fatalf("References = %+v, want a single skipped (Fetched=false) entry for masanami/flywheel#601", got.References)
	}
	// self-review 指摘の修正確認（round2）: この参照先のキー (#601) に実際に
	// gh api -i が要求されたことを invocation log で直接確かめる。当初はこの
	// 確認が無く、#601 の fixture 本文の登録漏れで gh が先に 404 を返して
	// いても（PR 除外の分岐に到達していなくても）テストが通っていた
	// （恒真のテスト）。
	calls := readInvocationLog(t, logPath)
	requestedReferencedIssue := false
	for _, call := range calls {
		endpoint := call[len(call)-1]
		if endpoint == "repos/masanami/flywheel/issues/601" {
			requestedReferencedIssue = true
		}
	}
	if !requestedReferencedIssue {
		t.Fatalf("gh was never asked for exactly repos/masanami/flywheel/issues/601, calls = %v", calls)
	}
	requireOnlyGETCalls(t, logPath)
}

// TestFetchUpstreamContext_AC90_DepthOne_GrandchildAndReferencedIssueComments_NeverFetched
// は AC-90 を実物の Client 経由で検証する: 参照先 (#401) の本文がさらに参照する
// #402 は取得されず、#401 のコメントも取得されない。
func TestFetchUpstreamContext_AC90_DepthOne_GrandchildAndReferencedIssueComments_NeverFetched(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "context_ac90_depth_one")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	got, err := core.FetchUpstreamContext(context.Background(), c, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 1 || got.References[0].Reference.Number != 401 || !got.References[0].Fetched {
		t.Fatalf("References = %+v, want exactly one fetched entry for #401", got.References)
	}

	calls := readInvocationLog(t, logPath)
	for _, call := range calls {
		endpoint := call[len(call)-1]
		if strings.Contains(endpoint, "issues/402") {
			t.Fatalf("the depth-2 reference (#402) must never be requested, calls = %v", calls)
		}
		if strings.Contains(endpoint, "issues/401/comments") {
			t.Fatalf("the referenced issue's (#401) comments must never be requested, calls = %v", calls)
		}
	}
	requireOnlyGETCalls(t, logPath)
}
