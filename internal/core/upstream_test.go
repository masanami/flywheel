package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeUpstreamIssueSource は UpstreamIssueSource の偽の実装（メモリ上の Issue の
// 集合）。core のテストはこれで上流の取得を差し替える（実装は #55 の
// internal/adapters/github が gh で行う。本チケットでは実装を置かない）。
type fakeUpstreamIssueSource struct {
	openIssues map[string][]UpstreamIssue // repo -> issues
	issueByKey map[string]UpstreamIssue   // "<repo>#<number>" -> issue
	login      string
	loginErr   error
	// getIssueErr は、指定されていれば GetIssue の呼び出し全てに対してこの
	// エラーを返す（ErrUpstreamIssueNotFound 以外の失敗を注入するため。
	// round2 self-review 指摘: 以前は CurrentLogin 経由でしか「それ以外の
	// 失敗」を検証しておらず、GetIssue 自体が非404失敗を返すケースを
	// 見ていなかった）。
	getIssueErr error
}

func (f *fakeUpstreamIssueSource) ListOpenIssues(_ context.Context, repo string) ([]UpstreamIssue, error) {
	return f.openIssues[repo], nil
}

func (f *fakeUpstreamIssueSource) GetIssue(_ context.Context, repo string, number int) (UpstreamIssue, error) {
	if f.getIssueErr != nil {
		return UpstreamIssue{}, f.getIssueErr
	}
	key := fmt.Sprintf("%s#%d", repo, number)
	issue, ok := f.issueByKey[key]
	if !ok {
		return UpstreamIssue{}, ErrUpstreamIssueNotFound
	}
	return issue, nil
}

func (f *fakeUpstreamIssueSource) CurrentLogin(_ context.Context) (string, error) {
	if f.loginErr != nil {
		return "", f.loginErr
	}
	return f.login, nil
}

// TestUpstreamIssueSource_FakeSatisfiesInterface は UpstreamIssueSource の
// interface（取得 IF）を偽の実装が満たせることのコンパイルレベルの確認
// （実装は #55 が置く。本チケットは IF の形を固定するだけ）。
//
// self-review 指摘（正当な指摘・意図的な限界として記録する）: この偽の実装は
// テスト自身が用意した戻り値をそのまま読み返すだけであり、本番の振る舞いを
// 検証していない（自明に真になるテスト）。本チケットの範囲は「IF の形」と
// 「ErrUpstreamIssueNotFound を errors.Is で判定できること」の契約を固定する
// ことに限られ、実際に gh を呼ぶ実装は #55（internal/adapters/github）が
// 持つ。したがって #55 が実装した時点で、この偽の実装ではなく実装そのものに
// 対する契約テスト（同じ interface を満たすことの確認・404/410判定の実測）が
// 別途必要になる。ここでは IF・型・sentinel error の「形」が固まっていることを
// 固定する目的にとどめ、構造を変えない。
func TestUpstreamIssueSource_FakeSatisfiesInterface(t *testing.T) {
	var _ UpstreamIssueSource = (*fakeUpstreamIssueSource)(nil)

	fake := &fakeUpstreamIssueSource{
		openIssues: map[string][]UpstreamIssue{
			"masanami/flywheel": {
				{
					ExternalKey: "masanami/flywheel#49",
					Repo:        "masanami/flywheel",
					Number:      49,
					Title:       "t",
					Body:        "b",
					Reporter:    "masanami",
					Assignees:   []string{"masanami"},
					Labels:      []string{"priority:high"},
					State:       "open",
					URL:         "https://github.com/masanami/flywheel/issues/49",
				},
			},
		},
		issueByKey: map[string]UpstreamIssue{
			"masanami/flywheel#49": {ExternalKey: "masanami/flywheel#49", State: "open"},
		},
		login: "masanami",
	}

	ctx := context.Background()
	issues, err := fake.ListOpenIssues(ctx, "masanami/flywheel")
	if err != nil || len(issues) != 1 {
		t.Fatalf("ListOpenIssues() = %v, %v", issues, err)
	}

	issue, err := fake.GetIssue(ctx, "masanami/flywheel", 49)
	if err != nil || issue.ExternalKey != "masanami/flywheel#49" {
		t.Fatalf("GetIssue() = %v, %v", issue, err)
	}

	login, err := fake.CurrentLogin(ctx)
	if err != nil || login != "masanami" {
		t.Fatalf("CurrentLogin() = %q, %v", login, err)
	}
}

// TestUpstreamIssueSource_NotFoundIsDistinguishableFromOtherFailures は、
// 1件取得の「見つからない（404・410）」が errors.Is(err, ErrUpstreamIssueNotFound)
// で判定でき、それ以外の失敗と区別できることを検証する
// （§機能全体の設計「1件の取得の"見つからない"とそれ以外の失敗を区別できる
// エラーの形」）。
func TestUpstreamIssueSource_NotFoundIsDistinguishableFromOtherFailures(t *testing.T) {
	fake := &fakeUpstreamIssueSource{
		issueByKey: map[string]UpstreamIssue{},
	}
	_, err := fake.GetIssue(context.Background(), "masanami/flywheel", 999999)
	if !errors.Is(err, ErrUpstreamIssueNotFound) {
		t.Fatalf("GetIssue(missing) error = %v, want ErrUpstreamIssueNotFound", err)
	}

	otherErr := errors.New("network timeout")
	fakeWithOtherFailure := &fakeUpstreamIssueSource{
		issueByKey: map[string]UpstreamIssue{},
		loginErr:   otherErr,
	}
	_, err = fakeWithOtherFailure.CurrentLogin(context.Background())
	if errors.Is(err, ErrUpstreamIssueNotFound) {
		t.Fatalf("CurrentLogin() unexpectedly matched ErrUpstreamIssueNotFound: %v", err)
	}
	if !errors.Is(err, otherErr) {
		t.Fatalf("CurrentLogin() error = %v, want otherErr", err)
	}

	// GetIssue 自体が ErrUpstreamIssueNotFound とは別の失敗を返す場合も、
	// 呼び出し側がこの2つを区別できることを直接検証する
	// （round2 self-review 指摘。上の CurrentLogin 経由の確認だけでは
	// GetIssue の非404失敗を経由していなかった）。
	getIssueOtherErr := errors.New("http 500")
	fakeWithGetIssueFailure := &fakeUpstreamIssueSource{
		issueByKey:  map[string]UpstreamIssue{},
		getIssueErr: getIssueOtherErr,
	}
	_, err = fakeWithGetIssueFailure.GetIssue(context.Background(), "masanami/flywheel", 1)
	if errors.Is(err, ErrUpstreamIssueNotFound) {
		t.Fatalf("GetIssue() unexpectedly matched ErrUpstreamIssueNotFound: %v", err)
	}
	if !errors.Is(err, getIssueOtherErr) {
		t.Fatalf("GetIssue() error = %v, want getIssueOtherErr", err)
	}
}
