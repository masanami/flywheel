package core

import (
	"context"
	"errors"
	"testing"
)

// このファイルは #69「移管された Issue を missing として扱う」を検証する。
// GetIssue が改名・移管の転送を辿って要求と別の repo の Issue を返したとき
// （internal/adapters/github の repoMismatchError の経路）、core は
// ErrUpstreamIssueTransferred という 404・410 とは別の sentinel を受け取り、
// confirmOpenListAbsence がそれも missing に写す（#58 の 404・410 の分岐と同じ
// 反映）。

// --- sentinel の識別性 ---

func TestErrUpstreamIssueTransferred_IsDistinctFromNotFound(t *testing.T) {
	if errors.Is(ErrUpstreamIssueTransferred, ErrUpstreamIssueNotFound) {
		t.Fatal("ErrUpstreamIssueTransferred must not match ErrUpstreamIssueNotFound (404 と移管を区別できること)")
	}
	if errors.Is(ErrUpstreamIssueNotFound, ErrUpstreamIssueTransferred) {
		t.Fatal("ErrUpstreamIssueNotFound must not match ErrUpstreamIssueTransferred")
	}
	wrapped := errWrapForTest(ErrUpstreamIssueTransferred)
	if !errors.Is(wrapped, ErrUpstreamIssueTransferred) {
		t.Fatal("wrapped ErrUpstreamIssueTransferred must still match via errors.Is")
	}
	if errors.Is(wrapped, ErrUpstreamIssueNotFound) {
		t.Fatal("wrapped ErrUpstreamIssueTransferred must not match ErrUpstreamIssueNotFound")
	}
}

func errWrapForTest(err error) error {
	return errors.Join(errors.New("adapters/github: get issue o/r#1"), err)
}

// --- confirmOpenListAbsence: 移管も missing に写す（AC-68 と同じ反映を移管にも適用） ---

func TestIngest_CloseDetection_TransferredMarksMissing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:      map[string][]UpstreamIssue{"o/r": {}}, // 一覧には現れない
		getIssueErr: map[string]error{"o/r#1": ErrUpstreamIssueTransferred},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "missing" {
		t.Fatalf("item = %+v, want upstream_state=missing (移管も 404・410 と同じ扱い)", item)
	}
	if item.Result != IngestOutcomeUnchanged {
		t.Errorf("Result = %q, want unchanged（result は人間記入欄についての結果）", item.Result)
	}

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb == nil || sb.UpstreamState != upstreamStateMissing {
		t.Fatalf("source_binding = %+v, %v, want upstream_state=missing", sb, err)
	}
	assertCloseCheckRecorded(t, s, ch, "missing")
}

// --- 移管の反映は課題の状態を変えない（未分類・着手中・完了確認待ちのそれぞれ） ---

func TestIngest_CloseDetection_TransferredDoesNotChangeChallengeStatus(t *testing.T) {
	statuses := []Status{StatusUnclassified, StatusInProgress, StatusAwaitingCompletionApproval}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			ch := bindOpenChallenge(t, s, "bound", "o/r#1")
			cid := mustParseChallengeID(t, ch.ID)
			setChallengeStatus(t, s, cid, status)

			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{
				issues:      map[string][]UpstreamIssue{"o/r": {}},
				getIssueErr: map[string]error{"o/r#1": ErrUpstreamIssueTransferred},
			}
			runIngestForTest(t, s, []SourceEntry{src}, up)

			detail, err := s.GetChallenge(context.Background(), ch.ID)
			if err != nil || detail.Status != status {
				t.Fatalf("Status = %v, %v, want unchanged %q", detail, err, status)
			}
			if detail.SourceBinding == nil || detail.SourceBinding.UpstreamState != "missing" {
				t.Errorf("SourceBinding = %+v, want upstream_state=missing", detail.SourceBinding)
			}
		})
	}
}

// --- status の discrepancies に upstream_missing で出る（ingest からの反映を経由した end-to-end） ---

func TestIngest_Transferred_ShowsAsUpstreamMissingDiscrepancy(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:      map[string][]UpstreamIssue{"o/r": {}},
		getIssueErr: map[string]error{"o/r#1": ErrUpstreamIssueTransferred},
	}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindUpstreamMissing}) {
		t.Fatalf("discrepancy = %+v, want kinds=[upstream_missing]", d)
	}
}
