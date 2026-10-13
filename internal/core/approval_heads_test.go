package core

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

type headSource struct {
	calls []string
	fail  bool
}

func (h *headSource) GetPullRequestChecks(_ context.Context, repo string, n int) (UpstreamPullRequestChecks, error) {
	h.calls = append(h.calls, repo+"#"+strconv.Itoa(n))
	if h.fail {
		return UpstreamPullRequestChecks{}, errors.New("boom")
	}
	return UpstreamPullRequestChecks{HeadSHA: "sha-" + strconv.Itoa(n)}, nil
}

func TestParseReleasePullRequestRef(t *testing.T) {
	cases := []struct {
		ref  string
		repo string
		n    int
		ok   bool
	}{
		{"https://github.com/o/r/pull/12", "o/r", 12, true},
		{"https://github.com/o/r/pull/12/files", "o/r", 12, true},
		{"https://github.com/o/r/pull/12#x", "o/r", 12, true},
		{"https://github.com/o/r/issues/12", "", 0, false},
		{"https://example.com/o/r/pull/12", "", 0, false},
		{"http://github.com/o/r/pull/12", "", 0, false},
		{"https://github.com/o/r/pull/x", "", 0, false},
		{"https://github.com/o/r/pull/0", "", 0, false},
		{"v1.2.3", "", 0, false},
	}
	for _, c := range cases {
		repo, n, ok := parseReleasePullRequestRef(c.ref)
		if repo != c.repo || n != c.n || ok != c.ok {
			t.Errorf("parse(%q) = %q,%d,%v want %q,%d,%v", c.ref, repo, n, ok, c.repo, c.n, c.ok)
		}
	}
}

func TestResolveReleaseHeads(t *testing.T) {
	ref := func(s string) *string { return &s }
	rels := []IrreversibleOperation{
		{ID: "OP-1", Ref: ref("https://github.com/o/r/pull/3/files")},
		{ID: "OP-2", Ref: ref("not-a-pr")},
		{ID: "OP-3", Ref: nil},
		{ID: "OP-4", Ref: ref("https://github.com/o/r/pull/4")},
	}
	src := &headSource{}
	got := ResolveReleaseHeads(context.Background(), rels, src)
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].HeadSHA == nil || *got[0].HeadSHA != "sha-3" || got[1].HeadSHA != nil || got[2].HeadSHA != nil || got[3].HeadSHA == nil || *got[3].HeadSHA != "sha-4" {
		t.Errorf("unexpected heads: %+v", got)
	}
	if len(src.calls) != 2 {
		t.Errorf("calls = %v, want 2 (only PR refs)", src.calls)
	}

	failing := ResolveReleaseHeads(context.Background(), rels, &headSource{fail: true})
	for _, g := range failing {
		if g.HeadSHA != nil {
			t.Errorf("head on failure = %v, want nil", *g.HeadSHA)
		}
	}
	if g := ResolveReleaseHeads(context.Background(), nil, src); g == nil || len(g) != 0 {
		t.Errorf("empty = %#v, want non-nil empty", g)
	}
	if g := ResolveReleaseHeads(context.Background(), rels, nil); len(g) != 4 || g[0].HeadSHA != nil {
		t.Errorf("nil source = %+v", g)
	}
}
