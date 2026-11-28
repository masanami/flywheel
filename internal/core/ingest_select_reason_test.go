package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// reasonFixture は、断る理由ごとの Issue を 1 件ずつ持つ。o/r#1 は課題にしてある。
func reasonFixture(t *testing.T) (*Store, IngestSelectedInput, *fakeUpstream) {
	t.Helper()
	s, src, up := selectFixture(t)
	up.getIssue = map[string]UpstreamIssue{}
	closed := issueFixture("o/r#3", "https://github.com/o/r/issues/3", "closed", "b", "carol", nil, nil)
	closed.State = "closed"
	up.getIssue["o/r#3"] = closed
	// o/r#5 は立てた直後: 1 件の取得では開いているが、開いている Issue の一覧にまだ無い。
	up.getIssue["o/r#5"] = issueFixture("o/r#5", "https://github.com/o/r/issues/5", "new", "b", "carol", nil, nil)
	up.getIssueErr = map[string]error{
		"o/r#99": ErrUpstreamIssueNotFound,
		"o/q#99": ErrUpstreamIssueNotFound,
		"o/q#3":  ErrUpstreamIssueNotFound,
		"o/q#5":  ErrUpstreamIssueNotFound,
		"o/r#8":  ErrUpstreamIssueTransferred,
		"o/r#6":  errors.New("boom"),
	}
	in := IngestSelectedInput{Sources: []SourceEntry{src}, Upstream: up}
	first := in
	first.Specs = []string{"o/r#1"}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, first); err != nil {
		t.Fatalf("first IngestSelected() error = %v", err)
	}
	return s, in, up
}

const (
	reasonTaken    = "already a challenge"
	reasonClosed   = "closed"
	reasonPolicy   = "out of policy"
	reasonNotFound = "not found"
)

// assertOnlyReason は文が理由 want だけを含むことを確かめる。
func assertOnlyReason(t *testing.T, msg, want string) {
	t.Helper()
	for _, r := range []string{reasonTaken, reasonClosed, reasonPolicy, reasonNotFound} {
		if got := strings.Contains(msg, r); got != (r == want) {
			t.Errorf("message contains %q = %t, want the only reason %q: %s", r, got, want, msg)
		}
	}
}

func TestIngestSelected_RejectionNamesOneReason(t *testing.T) {
	t.Parallel()
	s, in, _ := reasonFixture(t)
	cases := []struct {
		name, spec, want, detail string
	}{
		{"already a challenge", "o/r#1", reasonTaken, ""},
		{"already a challenge, other case", "O/R#1", reasonTaken, ""},
		{"closed", "o/r#3", reasonClosed, ""},
		{"assignee policy", "o/r#2", reasonPolicy, "assignee_policy"},
		{"undeclared repository", "x/y#1", reasonPolicy, "not declared"},
		{"no such issue", "o/r#99", reasonNotFound, ""},
		{"transferred", "o/r#8", reasonNotFound, ""},
		{"not yet in the upstream list", "o/r#5", reasonNotFound, "not in the upstream list of open issues yet"},
		{"single lookup fails", "o/r#6", reasonNotFound, "boom"},
		{"number: out of policy", "2", reasonPolicy, "o/r#2"},
		{"number: closed", "3", reasonClosed, "o/r#3"},
		{"number: not yet in the upstream list", "5", reasonNotFound, "o/r#5"},
		{"number: no such issue", "99", reasonNotFound, ""},
	}
	for _, c := range cases {
		req := in
		req.Specs = []string{c.spec}
		_, err := s.IngestSelected(context.Background(), ChannelCLI, req)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", c.name, err)
			continue
		}
		assertOnlyReason(t, err.Error(), c.want)
		if !strings.Contains(err.Error(), c.spec) || !strings.Contains(err.Error(), c.detail) {
			t.Errorf("%s: message lacks %q or %q: %v", c.name, c.spec, c.detail, err)
		}
	}
	if n := countChallenges(t, s); n != 1 {
		t.Fatalf("challenges = %d, want 1", n)
	}
}

func TestIngestSelected_NumberOnlyAlreadyAChallengeNamesTheReason(t *testing.T) {
	t.Parallel()
	s, in, _ := reasonFixture(t)
	req := in
	req.Specs = []string{"o/q#1"}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, req); err != nil {
		t.Fatalf("IngestSelected(o/q#1) error = %v", err)
	}
	req.Specs = []string{"1"}
	_, err := s.IngestSelected(context.Background(), ChannelCLI, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	assertOnlyReason(t, err.Error(), reasonTaken)
}

func TestIngestSelected_EveryRejectedSpecGetsItsOwnReason(t *testing.T) {
	t.Parallel()
	s, in, _ := reasonFixture(t)
	req := in
	req.Specs = []string{"o/r#1", "o/q#7", "o/r#3", "o/r#2", "o/r#99"}
	_, err := s.IngestSelected(context.Background(), ChannelCLI, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "o/q#7") {
		t.Errorf("an ingestable spec is listed as rejected: %s", msg)
	}
	// 指定の順に、指定ごとの区切りで理由が 1 つずつ出る。
	parts := strings.Split(msg, "; \"")
	want := []struct{ spec, reason string }{
		{"o/r#1", reasonTaken}, {"o/r#3", reasonClosed}, {"o/r#2", reasonPolicy}, {"o/r#99", reasonNotFound},
	}
	if len(parts) != len(want) {
		t.Fatalf("message has %d parts, want %d: %s", len(parts), len(want), msg)
	}
	for i, w := range want {
		if !strings.Contains(parts[i], w.spec+`"`) {
			t.Errorf("part %d lacks %s: %s", i, w.spec, parts[i])
		}
		assertOnlyReason(t, parts[i], w.reason)
	}
	if n := countChallenges(t, s); n != 1 {
		t.Fatalf("challenges = %d, want 1（どれも取り込まない）", n)
	}
}

// 判定できない指定・番号が複数に当たる指定・重なる指定が混ざっても、断る指定の全部に理由が出る。
// 誤りの種類は、指定の順で最初に断る指定のもの。
func TestIngestSelected_MixedFailuresKeepEveryReasonAndTheFirstClass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		specs      []string
		listFails  string // 一覧を読めないリポジトリ
		want       error
		wantInPart []string // 指定の順の、区切りごとに含む文
	}{
		{"rejected then unavailable", []string{"o/r#99", "o/q#7"}, "o/q", ErrValidation,
			[]string{`"o/r#99" is not ingestable: not found`, "cannot decide whether o/q#7 is ingestable"}},
		{"unavailable then rejected", []string{"o/q#7", "o/r#99"}, "o/q", ErrUpstreamFetchFailed,
			[]string{"cannot decide whether o/q#7 is ingestable", `"o/r#99" is not ingestable: not found`}},
		{"rejected then ambiguous", []string{"o/r#99", "4", "o/r#3"}, "", ErrValidation,
			[]string{`"o/r#99" is not ingestable: not found`, `"4" matches 2 ingestable issues`, `"o/r#3" is not ingestable: closed`}},
		// "o/q#7" と "7" が同じ Issue を選ぶことは、上流を読むまで分からない。
		{"rejected then listed twice", []string{"o/r#99", "o/q#7", "7"}, "", ErrValidation,
			[]string{`"o/r#99" is not ingestable: not found`, `"7" selects an issue that is already listed`}},
		{"listed twice then rejected", []string{"o/q#7", "7", "o/r#3"}, "", ErrIngestUsage,
			[]string{`"7" selects an issue that is already listed`, `"o/r#3" is not ingestable: closed`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, in, up := reasonFixture(t)
			// 番号 4 は 2 つのリポジトリの取り込めるものに当たる。
			for _, repo := range []string{"o/r", "o/q"} {
				up.issues[repo] = append(up.issues[repo], issueFixture(repo+"#4", "https://github.com/"+repo+"/issues/4", "four", "b", "carol", nil, nil))
			}
			if c.listFails != "" {
				up.errs = map[string]error{c.listFails: errors.New("list failed")}
			}
			in.Specs = c.specs
			_, err := s.IngestSelected(context.Background(), ChannelCLI, in)
			if !errors.Is(err, c.want) {
				t.Fatalf("error = %v, want %v", err, c.want)
			}
			parts := strings.Split(err.Error(), "; ")
			i := 0
			for _, p := range parts {
				if i < len(c.wantInPart) && strings.Contains(p, c.wantInPart[i]) {
					i++
				}
			}
			if i != len(c.wantInPart) {
				t.Errorf("message lacks %q in order: %v", c.wantInPart, err)
			}
			if n := countChallenges(t, s); n != 1 {
				t.Errorf("challenges = %d, want 1（どれも取り込まない）", n)
			}
		})
	}
}
