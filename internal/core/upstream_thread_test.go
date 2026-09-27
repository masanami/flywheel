package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --- ExtractUpstreamReferences（純粋関数）のテスト ---

func TestExtractUpstreamReferences_BareSameRepo(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "See #50 for details.", nil)
	want := []UpstreamReference{{Repo: "masanami/flywheel", Number: 50}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestExtractUpstreamReferences_OwnerNameForm(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "See other/repo#7 for details.", nil)
	want := []UpstreamReference{{Repo: "other/repo", Number: 7}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestExtractUpstreamReferences_URLForm(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "See https://github.com/third/repo/issues/9 for details.", nil)
	want := []UpstreamReference{{Repo: "third/repo", Number: 9}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

// TestExtractUpstreamReferences_URLWithTrailingAnchor_IsNotConfusedWithBareRef は、
// URL の末尾に "#issuecomment-…" が続いても、それが余分な bare #N として
// 拾われないことを確かめる（"#" の直後が数字でないため upstreamRefBareRe 自体が
// 一致しない）。
func TestExtractUpstreamReferences_URLWithTrailingAnchor_IsNotConfusedWithBareRef(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "https://github.com/third/repo/issues/9#issuecomment-123", nil)
	want := []UpstreamReference{{Repo: "third/repo", Number: 9}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestExtractUpstreamReferences_BoundaryRejectsWordPrefixedHash(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "abc#1", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (abc#1 must not be captured as a reference)", refs)
	}
}

// TestExtractUpstreamReferences_RejectsHTMLEntityAndZero は、HTML の数値文字参照
// （"&#39;"）と番号 0（"#0"）を参照として拾わないことを確かめる（参照の枠 5 件を
// 無駄に消費させない）。
func TestExtractUpstreamReferences_RejectsHTMLEntityAndZero(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "it&#39;s #0 and other/repo#0 and #3", nil)
	want := []UpstreamReference{{Repo: "masanami/flywheel", Number: 3}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestExtractUpstreamReferences_BoundaryRejectsWordSuffixedNumber(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "#12abc", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (#12abc must not be captured as a reference)", refs)
	}
}

// TestExtractUpstreamReferences_BareRefAdjacentToJapaneseText_IsCaptured は
// self-review 指摘の修正確認: 日本語の文字は「英数字」ではないため、区切りの
// 空白を挟まずに隣接していても bare "#<番号>" の境界を壊さない
// （修正前は unicode.IsLetter を使っており、"詳細は#12を参照" のような書き方
// で抽出できていなかった）。
func TestExtractUpstreamReferences_BareRefAdjacentToJapaneseText_IsCaptured(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "詳細は#12を参照。", nil)
	want := []UpstreamReference{{Repo: "masanami/flywheel", Number: 12}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (Japanese text adjacent to #12 must not suppress extraction)", refs, want)
	}
}

// TestExtractUpstreamReferences_OwnerNameForm_RejectsPathPrefixedMatch は
// self-review 指摘の修正確認: "<owner>/<name>#<番号>" にも左側の境界判定を
// 適用し、パスの末尾2セグメントを repo への参照として誤って拾わない
// （修正前は "a/b/c#5" から誤って {b/c, 5} を抽出していた）。
func TestExtractUpstreamReferences_OwnerNameForm_RejectsPathPrefixedMatch(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "see a/b/c#5 for details.", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (a/b/c#5 must not be captured as other/repo#N)", refs)
	}
}

// TestExtractUpstreamReferences_OwnerNameForm_RejectsWordSuffixedNumber は
// owner/name#N 形式にも bare #N と同じ右側の境界判定が効くことを確かめる。
// TestExtractUpstreamReferences_OwnerNameForm_RejectsDotPrefixedDomainMatch は
// self-review 指摘（round2）の修正確認: 他ホストのドメイン名（"."区切り）の
// 一部を owner/name#N として誤って拾わない（"gitlab.com/group#12" から
// {com/group, 12} を抽出してしまうと、無関係な参照が maxUpstreamReferences の
// 枠を無駄に消費する）。
func TestExtractUpstreamReferences_OwnerNameForm_RejectsDotPrefixedDomainMatch(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "see gitlab.com/group#12 for details.", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (gitlab.com/group#12 must not be captured as group#12)", refs)
	}
}

func TestExtractUpstreamReferences_OwnerNameForm_RejectsWordSuffixedNumber(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "see other/repo#123abc for details.", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (other/repo#123abc must not be captured)", refs)
	}
}

// TestExtractUpstreamReferences_URLForm_RejectsWordSuffixedNumber は URL 形式
// にも同じ右側の境界判定が効くことを確かめる（末尾の "#issuecomment-…" は
// 既存の TestExtractUpstreamReferences_URLWithTrailingAnchor_IsNotConfusedWithBareRef
// が正しく許可されることを検証済み。ここでは数字にそのまま英字が続く壊れた
// 形が拒否されることを確認する）。
func TestExtractUpstreamReferences_URLForm_RejectsWordSuffixedNumber(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "see https://github.com/third/repo/issues/9abc for details.", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (a malformed trailing word after the issue number must not be captured)", refs)
	}
}

func TestExtractUpstreamReferences_HeadingWithoutDigitIsNotCaptured(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "# 見出し", nil)
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none (a heading without digits must not be captured)", refs)
	}
}

func TestExtractUpstreamReferences_DedupesCaseInsensitiveOwnerNameKey(t *testing.T) {
	body := "See other/Repo#7 and later OTHER/repo#7 again."
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, body, nil)
	want := []UpstreamReference{{Repo: "other/Repo", Number: 7}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (deduped, first occurrence kept)", refs, want)
	}
}

func TestExtractUpstreamReferences_ExcludesSelfReference(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "See #100 and also #50.", nil)
	want := []UpstreamReference{{Repo: "masanami/flywheel", Number: 50}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (self-reference #100 must be excluded)", refs, want)
	}
}

func TestExtractUpstreamReferences_ExcludesSelfReferenceCaseInsensitiveRepo(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "See Masanami/Flywheel#100 and also #50.", nil)
	want := []UpstreamReference{{Repo: "masanami/flywheel", Number: 50}}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (self-reference via owner/name form, case-insensitive, must be excluded)", refs, want)
	}
}

// TestExtractUpstreamReferences_OrderIsBodyThenCommentsInSequence は、出現順が
// 本文→コメントの順（コメントは引数の順＝作成順）で保たれることを確認する。
func TestExtractUpstreamReferences_OrderIsBodyThenCommentsInSequence(t *testing.T) {
	comments := []UpstreamComment{
		{Body: "first comment mentions #20"},
		{Body: "second comment mentions #10"},
	}
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "body mentions #30", comments)
	want := []UpstreamReference{
		{Repo: "masanami/flywheel", Number: 30},
		{Repo: "masanami/flywheel", Number: 20},
		{Repo: "masanami/flywheel", Number: 10},
	}
	if !refsEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v (order: body, then comments in argument order)", refs, want)
	}
}

func TestExtractUpstreamReferences_NoTruncationAtSix(t *testing.T) {
	refs := ExtractUpstreamReferences("masanami/flywheel", 100, "#1 #2 #3 #4 #5 #6", nil)
	if len(refs) != 6 {
		t.Fatalf("len(refs) = %d, want 6 (truncation to 5 is FetchUpstreamContext's responsibility, not extraction's)", len(refs))
	}
}

func refsEqual(a, b []UpstreamReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- FetchUpstreamContext のテスト（UpstreamThreadSource の偽の実装） ---

// fakeUpstreamThreadSource は UpstreamThreadSource の偽の実装（メモリ上の
// Issue とコメントの集合）。呼び出しの記録（threadCalls・referenceCalls）で
// 「深さ 1」「5 件の上限」「スキップが枠を消費する」を検証する。
type fakeUpstreamThreadSource struct {
	threads map[string]UpstreamIssueThread // "<repo>#<number>" -> thread
	issues  map[string]UpstreamIssue       // "<repo>#<number>" -> referenced issue body
	// notFound・transferred は、指定したキーに対して GetReferencedIssue が
	// その sentinel エラーを返すことを示す。
	notFound    map[string]bool
	transferred map[string]bool
	// otherFailure は、指定したキーに対して GetReferencedIssue がこの
	// （sentinel の付かない）エラーを返すことを示す。
	otherFailure map[string]error
	// threadErr が非 nil なら GetIssueThread は常にこのエラーを返す。
	threadErr error

	threadCalls    []string
	referenceCalls []string
}

func upstreamKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

func (f *fakeUpstreamThreadSource) GetIssueThread(_ context.Context, repo string, number int) (UpstreamIssueThread, error) {
	f.threadCalls = append(f.threadCalls, upstreamKey(repo, number))
	if f.threadErr != nil {
		return UpstreamIssueThread{}, f.threadErr
	}
	thread, ok := f.threads[upstreamKey(repo, number)]
	if !ok {
		return UpstreamIssueThread{}, ErrUpstreamIssueNotFound
	}
	return thread, nil
}

func (f *fakeUpstreamThreadSource) GetReferencedIssue(_ context.Context, repo string, number int) (UpstreamIssue, error) {
	key := upstreamKey(repo, number)
	f.referenceCalls = append(f.referenceCalls, key)
	if f.notFound[key] {
		return UpstreamIssue{}, ErrUpstreamIssueNotFound
	}
	if f.transferred[key] {
		return UpstreamIssue{}, ErrUpstreamIssueTransferred
	}
	if err, ok := f.otherFailure[key]; ok {
		return UpstreamIssue{}, err
	}
	issue, ok := f.issues[key]
	if !ok {
		return UpstreamIssue{}, ErrUpstreamIssueNotFound
	}
	return issue, nil
}

// TestFetchUpstreamContext_FakeSatisfiesInterface はコンパイルレベルの確認。
func TestFetchUpstreamContext_FakeSatisfiesInterface(_ *testing.T) {
	var _ UpstreamThreadSource = (*fakeUpstreamThreadSource)(nil)
}

func TestFetchUpstreamContext_ReturnsThreadAndFetchesSingleReference(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue:    UpstreamIssue{ExternalKey: "masanami/flywheel#100", Repo: "masanami/flywheel", Number: 100, Body: "See #50."},
				Comments: []UpstreamComment{{Body: "a comment", Author: "someone"}},
			},
		},
		issues: map[string]UpstreamIssue{
			"masanami/flywheel#50": {ExternalKey: "masanami/flywheel#50", Repo: "masanami/flywheel", Number: 50, Body: "target body"},
		},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if got.Thread.Issue.ExternalKey != "masanami/flywheel#100" {
		t.Fatalf("Thread.Issue.ExternalKey = %q", got.Thread.Issue.ExternalKey)
	}
	if len(got.Thread.Comments) != 1 || got.Thread.Comments[0].Author != "someone" {
		t.Fatalf("Thread.Comments = %+v", got.Thread.Comments)
	}
	if len(got.References) != 1 {
		t.Fatalf("References = %+v, want 1 entry", got.References)
	}
	ref := got.References[0]
	if !ref.Fetched || ref.Issue.Body != "target body" {
		t.Fatalf("References[0] = %+v, want Fetched=true with body %q", ref, "target body")
	}
}

func TestFetchUpstreamContext_ThreadFetchFailure_ReturnsError(t *testing.T) {
	src := &fakeUpstreamThreadSource{threadErr: errors.New("network timeout")}
	_, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err == nil {
		t.Fatal("want error when the issue's own thread fetch fails")
	}
}

// TestFetchUpstreamContext_ThreadFetchFailure_PreservesNotFoundSentinel は、
// GetIssueThread の失敗の sentinel の契約が GetIssue と同じ（errors.Is で
// 判定できる）ことを、課題自身のスレッドの取得経路でも確かめる
// （UpstreamThreadSource のコメントが謳う契約の直接確認。self-review 指摘）。
func TestFetchUpstreamContext_ThreadFetchFailure_PreservesNotFoundSentinel(t *testing.T) {
	src := &fakeUpstreamThreadSource{threadErr: ErrUpstreamIssueNotFound}
	_, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if !errors.Is(err, ErrUpstreamIssueNotFound) {
		t.Fatalf("FetchUpstreamContext error = %v, want errors.Is(err, ErrUpstreamIssueNotFound)", err)
	}
}

// TestFetchUpstreamContext_ThreadFetchFailure_PreservesTransferredSentinel は
// 同様に ErrUpstreamIssueTransferred も伝わることを確かめる。
func TestFetchUpstreamContext_ThreadFetchFailure_PreservesTransferredSentinel(t *testing.T) {
	src := &fakeUpstreamThreadSource{threadErr: ErrUpstreamIssueTransferred}
	_, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if !errors.Is(err, ErrUpstreamIssueTransferred) {
		t.Fatalf("FetchUpstreamContext error = %v, want errors.Is(err, ErrUpstreamIssueTransferred)", err)
	}
}

// TestFetchUpstreamContext_SixReferences_OnlyFirstFiveFetched は AC-89:
// 参照が 6 件以上あるときは出現順の最初の 5 件だけを取得する。
func TestFetchUpstreamContext_SixReferences_OnlyFirstFiveFetched(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#201 #202 #203 #204 #205 #206"},
			},
		},
		issues: map[string]UpstreamIssue{
			"masanami/flywheel#201": {Body: "b201"},
			"masanami/flywheel#202": {Body: "b202"},
			"masanami/flywheel#203": {Body: "b203"},
			"masanami/flywheel#204": {Body: "b204"},
			"masanami/flywheel#205": {Body: "b205"},
			"masanami/flywheel#206": {Body: "b206"},
		},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 5 {
		t.Fatalf("len(References) = %d, want 5", len(got.References))
	}
	if len(src.referenceCalls) != 5 {
		t.Fatalf("referenceCalls = %v, want exactly 5 calls", src.referenceCalls)
	}
	for _, key := range src.referenceCalls {
		if key == "masanami/flywheel#206" {
			t.Fatalf("the 6th reference (#206) must not be fetched, referenceCalls = %v", src.referenceCalls)
		}
	}
}

// TestFetchUpstreamContext_DepthOne_ReferencedIssuesReferencesAreNotFetched は
// AC-90: 参照先がさらに参照する Issue は取得しない（深さ 1）。
func TestFetchUpstreamContext_DepthOne_ReferencedIssuesReferencesAreNotFetched(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#401"},
			},
		},
		issues: map[string]UpstreamIssue{
			// #401（深さ1・取得される）の本文はさらに #402 を参照するが、
			// #402（深さ2）は取得されないことを確かめる。
			"masanami/flywheel#401": {Body: "see also #402"},
			"masanami/flywheel#402": {Body: "grandchild body"},
		},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 1 || got.References[0].Reference.Number != 401 {
		t.Fatalf("References = %+v, want exactly [#401]", got.References)
	}
	for _, key := range src.referenceCalls {
		if key == "masanami/flywheel#402" {
			t.Fatalf("depth-2 reference (#402) must not be fetched, referenceCalls = %v", src.referenceCalls)
		}
	}
}

// TestFetchUpstreamContext_NotFoundReference_IsSkippedNotFailed は、参照先の
// 取得が 404 で失敗しても、その参照をスキップとして記録し全体は失敗にしない
// ことを確かめる。
func TestFetchUpstreamContext_NotFoundReference_IsSkippedNotFailed(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#50 #51"},
			},
		},
		issues: map[string]UpstreamIssue{
			"masanami/flywheel#51": {Body: "b51"},
		},
		notFound: map[string]bool{"masanami/flywheel#50": true},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v (want no error; a 404'd reference must be skipped, not fail the whole fetch)", err)
	}
	if len(got.References) != 2 {
		t.Fatalf("References = %+v, want 2 entries (one skipped, one fetched)", got.References)
	}
	if got.References[0].Fetched || got.References[0].Reference.Number != 50 {
		t.Fatalf("References[0] = %+v, want Fetched=false for #50", got.References[0])
	}
	if !got.References[1].Fetched || got.References[1].Issue.Body != "b51" {
		t.Fatalf("References[1] = %+v, want Fetched=true with body %q", got.References[1], "b51")
	}
}

// TestFetchUpstreamContext_TransferredReference_IsSkippedNotFailed は、参照先が
// 移管（ErrUpstreamIssueTransferred）でも同様にスキップとして扱う。
func TestFetchUpstreamContext_TransferredReference_IsSkippedNotFailed(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#50"},
			},
		},
		transferred: map[string]bool{"masanami/flywheel#50": true},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v (want no error for a transferred reference)", err)
	}
	if len(got.References) != 1 || got.References[0].Fetched {
		t.Fatalf("References = %+v, want a single skipped (Fetched=false) entry", got.References)
	}
}

// TestFetchUpstreamContext_OtherReferenceFailure_FailsWholeFetch は、404・移管
// 以外の失敗（例: 時間切れ）は全体の失敗として返すことを確かめる。
func TestFetchUpstreamContext_OtherReferenceFailure_FailsWholeFetch(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#50"},
			},
		},
		otherFailure: map[string]error{"masanami/flywheel#50": errors.New("http 500")},
	}

	_, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err == nil {
		t.Fatal("want error when a referenced issue fetch fails with a non-404/410/transferred error")
	}
	if errors.Is(err, ErrUpstreamIssueNotFound) || errors.Is(err, ErrUpstreamIssueTransferred) {
		t.Fatalf("error must not match the skip sentinels, got %v", err)
	}
}

// TestFetchUpstreamContext_SkippedReferenceStillConsumesASlot は、6 件以上の
// 参照のうち先頭 5 件目までにスキップ（404）があっても、6 件目目の参照は
// 埋め合わせで取得されないことを確かめる（【仮定】）。
func TestFetchUpstreamContext_SkippedReferenceStillConsumesASlot(t *testing.T) {
	src := &fakeUpstreamThreadSource{
		threads: map[string]UpstreamIssueThread{
			"masanami/flywheel#100": {
				Issue: UpstreamIssue{Repo: "masanami/flywheel", Number: 100, Body: "#301 #302 #303 #304 #305 #306"},
			},
		},
		issues: map[string]UpstreamIssue{
			"masanami/flywheel#301": {Body: "b301"},
			"masanami/flywheel#302": {Body: "b302"},
			"masanami/flywheel#304": {Body: "b304"},
			"masanami/flywheel#305": {Body: "b305"},
			"masanami/flywheel#306": {Body: "b306"},
		},
		notFound: map[string]bool{"masanami/flywheel#303": true},
	}

	got, err := FetchUpstreamContext(context.Background(), src, "masanami/flywheel", 100)
	if err != nil {
		t.Fatalf("FetchUpstreamContext: %v", err)
	}
	if len(got.References) != 5 {
		t.Fatalf("len(References) = %d, want 5 (skip does not free up a slot for #306)", len(got.References))
	}
	for _, key := range src.referenceCalls {
		if key == "masanami/flywheel#306" {
			t.Fatalf("#306 must not be fetched even though #303 was skipped, referenceCalls = %v", src.referenceCalls)
		}
	}
	fetchedCount := 0
	for _, ref := range got.References {
		if ref.Fetched {
			fetchedCount++
		}
	}
	if fetchedCount != 4 {
		t.Fatalf("fetchedCount = %d, want 4 (5 attempted, 1 skipped)", fetchedCount)
	}
}
