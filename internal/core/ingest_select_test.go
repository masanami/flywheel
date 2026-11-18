package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func selectFixture(t *testing.T) (*Store, SourceEntry, *fakeUpstream) {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r", "o/q"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {
			issueFixture("o/r#1", "https://github.com/o/r/issues/1", "one", "b1", "carol", nil, nil),
			issueFixture("o/r#2", "https://github.com/o/r/issues/2", "two", "b2", "carol", []string{"eve"}, nil),
		},
		"o/q": {
			issueFixture("o/q#1", "https://github.com/o/q/issues/1", "q-one", "b3", "carol", nil, nil),
			issueFixture("o/q#7", "https://github.com/o/q/issues/7", "q-seven", "b4", "carol", nil, nil),
		},
	}}
	return s, src, up
}

func TestIngestSelected_CreatesOnlyTheChosenIssueAsAnUnclassifiedIngestChallenge(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	res, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/q#7"}, Upstream: up})
	if err != nil {
		t.Fatalf("IngestSelected() error = %v", err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/q#7" || res.Ingested[0].ChallengeID != "C-1" || res.Ingested[0].Children != 0 {
		t.Fatalf("Ingested = %+v, want only o/q#7 as C-1 with 0 children", res.Ingested)
	}
	if n := countChallenges(t, s); n != 1 {
		t.Fatalf("challenges = %d, want 1（選ばなかった Issue は課題にしない）", n)
	}
	detail, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Status != StatusUnclassified || detail.SourceBinding == nil || detail.SourceBinding.ExternalKey != "o/q#7" {
		t.Fatalf("challenge = %+v, want unclassified / origin ingest / bound to o/q#7", detail)
	}
	if origin, _ := challengeOriginOf(t, s, 1); origin != "ingest" {
		t.Fatalf("origin = %q, want ingest", origin)
	}
	id := "C-1"
	log, err := s.ListActivities(context.Background(), &id)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(log) != 1 || log[0].Action != "ingest_create" {
		t.Fatalf("activity = %+v, want exactly one ingest_create", log)
	}
}

func TestIngestSelected_ExplicitKeyMatchesCaseInsensitivelyAndRecordsTheCanonicalKey(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	res, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"O/R#1"}, Upstream: up})
	if err != nil {
		t.Fatalf("IngestSelected() error = %v", err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/r#1" {
		t.Fatalf("Ingested = %+v, want the canonical key o/r#1", res.Ingested)
	}
}

func TestIngestSelected_NumberOnlyNeedsExactlyOneIngestableIssue(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	in := IngestSelectedInput{Sources: []SourceEntry{src}, Upstream: up}

	in.Specs = []string{"1"} // o/r#1 と o/q#1 の 2 件
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, in); !errors.Is(err, ErrValidation) {
		t.Fatalf("number matching 2 error = %v, want ErrValidation", err)
	}
	in.Specs = []string{"2"} // o/r#2 は方針に合わない（0 件）
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, in); !errors.Is(err, ErrValidation) {
		t.Fatalf("number matching 0 error = %v, want ErrValidation", err)
	}
	if n := countChallenges(t, s); n != 0 {
		t.Fatalf("challenges = %d, want 0", n)
	}
	in.Specs = []string{"7"}
	res, err := s.IngestSelected(context.Background(), ChannelCLI, in)
	if err != nil || len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/q#7" {
		t.Fatalf("number matching 1 = %+v, %v, want o/q#7", res, err)
	}
}

func TestIngestSelected_SourceNarrowsOnlyTheNumberOnlySpecs(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	a := excludeOthersSource("a", []string{"o/r"}, []string{"masanami"})
	b := excludeOthersSource("b", []string{"o/q"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "a1", "b", "carol", nil, nil)},
		"o/q": {issueFixture("o/q#1", "https://github.com/o/q/issues/1", "b1", "b", "carol", nil, nil)},
	}}
	srcB := "b"
	res, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{a, b}, SourceID: &srcB, Specs: []string{"1"}, Upstream: up})
	if err != nil || len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/q#1" {
		t.Fatalf("--source b の番号だけ = %+v, %v, want o/q#1", res, err)
	}
	// 判定は全部の取り込み元を読む: --source b でも、明示の指定なら a の Issue を選べる。
	res, err = s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{a, b}, SourceID: &srcB, Specs: []string{"o/r#1"}, Upstream: up})
	if err != nil || len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/r#1" {
		t.Fatalf("明示の指定 = %+v, %v, want o/r#1", res, err)
	}
	missing := "nope"
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{a, b}, SourceID: &missing, Specs: []string{"1"}, Upstream: up}); !errors.Is(err, ErrValidation) {
		t.Fatalf("未宣言の --source error = %v, want ErrValidation", err)
	}
}

func TestIngestSelected_UsageErrorsLeaveTheStoreUntouched(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	for _, specs := range [][]string{
		nil,
		{"o/r#1", "o/r#1"},
		{"o/r#1", "O/R#1"},
		{"7", "7"},
		{"7", "o/q#7"},
		{"o/q#7", "7"},
		{"o/r#1", "abc"},
		{"o/r#0"},
	} {
		_, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: specs, Upstream: up})
		if !errors.Is(err, ErrIngestUsage) {
			t.Errorf("specs %v error = %v, want ErrIngestUsage", specs, err)
		}
	}
	if n := countChallenges(t, s); n != 0 {
		t.Fatalf("challenges = %d, want 0", n)
	}
}

func TestIngestSelected_OneUningestableSpecMakesNoChallengeAtAll(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	for _, bad := range []string{"o/r#2", "o/r#99", "x/y#1"} { // 方針に合わない・存在しない・宣言に無い
		_, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/r#1", bad}, Upstream: up})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("with %s error = %v, want ErrValidation", bad, err)
		}
	}
	if n := countChallenges(t, s); n != 0 {
		t.Fatalf("challenges = %d, want 0（どれも取り込まない）", n)
	}
}

func TestIngestSelected_AlreadyAChallengeOrAChildCannotBeSelected(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/r#1"}, Upstream: up}); err != nil {
		t.Fatalf("first IngestSelected() error = %v", err)
	}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/r#1"}, Upstream: up}); !errors.Is(err, ErrValidation) {
		t.Fatalf("already a challenge error = %v, want ErrValidation", err)
	}

	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO source_child (challenge_id, source_id, external_key, upstream_state, created_at, updated_at) VALUES (1, 's', 'o/q#1', 'open', 'x', 'x')`)
		return err
	})
	if err != nil {
		t.Fatalf("insert source_child: %v", err)
	}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/q#1"}, Upstream: up}); !errors.Is(err, ErrValidation) {
		t.Fatalf("already a child error = %v, want ErrValidation", err)
	}
	scan, err := s.ScanIngestable(context.Background(), []SourceEntry{src}, up)
	if err != nil {
		t.Fatalf("ScanIngestable() error = %v", err)
	}
	for _, c := range scan.Candidates {
		if c.Issue.ExternalKey == "o/r#1" || c.Issue.ExternalKey == "o/q#1" {
			t.Fatalf("ScanIngestable lists %s, want it excluded (一覧と取り込めるものを食い違わせない)", c.Issue.ExternalKey)
		}
	}
	if n := countChallenges(t, s); n != 1 {
		t.Fatalf("challenges = %d, want 1", n)
	}
}

func TestIngestSelected_ListFailureOfTheRepoIsUpstreamUnavailable(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	up.errs = map[string]error{"o/r": errors.New("boom")}
	_, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{src}, Specs: []string{"o/r#1"}, Upstream: up})
	if !errors.Is(err, ErrUpstreamFetchFailed) {
		t.Fatalf("error = %v, want ErrUpstreamFetchFailed", err)
	}
}

func TestIngest_FollowUpNeverCreatesAChallengeAndCountsNothingForUnboundIssues(t *testing.T) {
	t.Parallel()
	s, src, up := selectFixture(t)
	res, err := s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: up})
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if n := countChallenges(t, s); n != 0 {
		t.Fatalf("challenges = %d, want 0（追従は新しい課題を作らない）", n)
	}
	for _, sr := range res.Sources {
		for _, rr := range sr.Repos {
			if len(rr.Items) != 0 || rr.Excluded != 0 {
				t.Fatalf("repo %+v, want no items and excluded=0", rr)
			}
		}
	}
}
