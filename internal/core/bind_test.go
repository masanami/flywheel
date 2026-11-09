package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// このファイルは bind・unbind（取り込み元の Issue の無い課題を、あとから GitHub Issue に
// 紐づける・外す。docs/features/m2-github-issue-ingest.md §課題と Issue の紐づけ）を、実ストア・
// 偽の上流の取得（fakeUpstream）で検証する。

// --- フィクスチャ ---

type bindFixture struct {
	s       *Store
	sources []SourceEntry
	up      *fakeUpstream
}

func newBindFixture(t *testing.T) *bindFixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return &bindFixture{
		s:       s,
		sources: []SourceEntry{excludeOthersSource("src-a", []string{"o/r", "Org/Mixed"}, []string{"masanami"})},
		up: &fakeUpstream{
			issues:   map[string][]UpstreamIssue{},
			login:    "masanami",
			getIssue: map[string]UpstreamIssue{},
		},
	}
}

// putIssue は上流の偽に open の Issue を置く（GetIssue と、repo の一覧の両方から見える）。
func (f *bindFixture) putIssue(key, title, body string) UpstreamIssue {
	repo, number, _ := splitExternalKey(key)
	issue := issueFixture(key, fmt.Sprintf("https://github.com/%s/issues/%d", repo, number), title, body, "carol", nil, nil)
	issue.Repo = repo
	issue.Number = number
	issue.Comments = 3
	issue.UpdatedAt = "2026-10-01T10:00:00Z"
	f.up.getIssue[key] = issue
	f.up.issues[repo] = append(f.up.issues[repo], issue)
	return issue
}

func (f *bindFixture) newChallenge(t *testing.T, title, description string) *Challenge {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Description: description})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	return ch
}

func (f *bindFixture) bind(id, key string) (*Challenge, error) {
	return f.s.BindChallenge(context.Background(), ChannelCLI, BindInput{ChallengeID: id, ExternalKey: key, Sources: f.sources, Upstream: f.up})
}

func (f *bindFixture) unbind(id string) (*Challenge, error) {
	return f.s.UnbindChallenge(context.Background(), ChannelCLI, id)
}

func (f *bindFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// bindState は「何も記録されない」の検査のための、課題の版（課題が無ければ -1）・作業ログの件数・対応の件数。
type bindState struct {
	version    int
	activities int
	bindings   int
}

func (f *bindFixture) state(t *testing.T, id string) bindState {
	t.Helper()
	version := -1
	if ch, err := f.s.GetChallenge(context.Background(), id); err == nil {
		version = ch.Version
	}
	return bindState{
		version:    version,
		activities: f.count(t, `SELECT COUNT(*) FROM activity`),
		bindings:   f.count(t, `SELECT COUNT(*) FROM source_binding`),
	}
}

func decodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

// --- bind の正常系 ---

func TestBind_RecordsBindingActivityAndVersion_WithoutReplacingTitleAndDescription(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#7", "UP-TITLE", "UP-BODY")
	ch := f.newChallenge(t, "OWN-TITLE", "OWN-DESC")
	before := f.state(t, ch.ID)

	got, err := f.bind(ch.ID, "o/r#7")
	if err != nil {
		t.Fatalf("BindChallenge: %v", err)
	}
	if got.Version != ch.Version+1 || got.Title != "OWN-TITLE" || got.Description != "OWN-DESC" {
		t.Errorf("returned challenge = %+v, want version %d with the title and description unchanged", got, ch.Version+1)
	}

	d, err := f.s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != before.version+1 || d.Title != "OWN-TITLE" || d.Description != "OWN-DESC" {
		t.Errorf("stored challenge = %+v, want version+1 and the title and description unchanged", d.Challenge)
	}
	sb := d.SourceBinding
	if sb == nil {
		t.Fatal("no source binding after bind")
	}
	if sb.ExternalKey != "o/r#7" || sb.SourceID != "src-a" || sb.URL != "https://github.com/o/r/issues/7" ||
		sb.UpstreamState != "open" || sb.PolicyState != "in_policy" ||
		sb.CommentsCount != 3 || sb.UpstreamUpdatedAt != "2026-10-01T10:00:00Z" ||
		sb.ReadCommentsCount != 0 || sb.ReadUpstreamUpdatedAt != "2026-10-01T10:00:00Z" {
		t.Errorf("source binding = %+v", sb)
	}
	if sb.Fingerprint != boundFingerprint || sb.Fingerprint == Fingerprint("UP-BODY") || !hasKnownFingerprintVersion(sb.Fingerprint) {
		t.Errorf("fingerprint = %q, want the bind marker %q (version 2, never equal to a computed fingerprint)", sb.Fingerprint, boundFingerprint)
	}

	act := lastActivity(t, f.s, ch.ID)
	if act.Entity != "challenge" || act.Action != "bind" || act.Channel != string(ChannelCLI) || act.Verification != string(VerificationNone) || act.Actor != "alice" {
		t.Errorf("activity = %+v, want challenge/bind via cli with no verification by alice", act)
	}
	b, a := decodeObject(t, act.Before), decodeObject(t, act.After)
	if b["title"] != "OWN-TITLE" || b["description"] != "OWN-DESC" || len(b) != 2 {
		t.Errorf("activity before = %v, want the title and description before binding", b)
	}
	if a["external_key"] != "o/r#7" || a["version"] != float64(before.version+1) {
		t.Errorf("activity after = %v, want external_key and the new version", a)
	}
	if got := f.state(t, ch.ID); got.activities != before.activities+1 || got.bindings != before.bindings+1 {
		t.Errorf("state = %+v, want exactly one activity and one binding more than %+v", got, before)
	}
}

func TestBind_RepoMatchIgnoresCaseAndUsesTheUpstreamCanonicalKey(t *testing.T) {
	f := newBindFixture(t)
	// 宣言は "Org/Mixed"。利用者は小文字で書き、上流の応答の正規の表記は "Org/Mixed#4"。
	f.putIssue("Org/Mixed#4", "T", "B")
	ch := f.newChallenge(t, "t", "d")

	if _, err := f.bind(ch.ID, "org/mixed#4"); err != nil {
		t.Fatalf("BindChallenge: %v", err)
	}
	d, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if d.SourceBinding == nil || d.SourceBinding.ExternalKey != "Org/Mixed#4" {
		t.Errorf("source binding = %+v, want the key in the upstream's canonical spelling", d.SourceBinding)
	}
}

func TestBind_IssueKeyedInDifferentCaseIsStillTheSameIssue(t *testing.T) {
	// 他の課題が "o/r#1" に対応しているとき、"O/R#1" と書いて bind しても、上流の正規の表記
	// （"o/r#1"）で④を判定するので拒まれる。
	f := newBindFixture(t)
	f.putIssue("o/r#1", "T", "B")
	first := f.newChallenge(t, "first", "d")
	if _, err := f.bind(first.ID, "o/r#1"); err != nil {
		t.Fatal(err)
	}
	second := f.newChallenge(t, "second", "d")
	if _, err := f.bind(second.ID, "O/R#1"); !errors.Is(err, ErrIssueBoundElsewhere) {
		t.Fatalf("error = %v, want ErrIssueBoundElsewhere", err)
	}
}

func TestBind_TerminalAndManualChallengesAreNotRestricted(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#1", "T", "B")
	ch := f.newChallenge(t, "t", "d")
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = 1`, string(StatusDone))
		return err
	}); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := f.bind(ch.ID, "o/r#1"); err != nil {
		t.Fatalf("BindChallenge on a terminal challenge: %v", err)
	}
	d, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if d.Status != StatusDone || f.count(t, `SELECT COUNT(*) FROM challenge WHERE id = 1 AND origin = 'manual'`) != 1 {
		t.Errorf("status = %s, want done and origin manual both unchanged", d.Status)
	}
}

// --- bind の拒否 ---

type bindRejectionCase struct {
	name  string
	setup func(t *testing.T, f *bindFixture) (id, key string)
	want  error
	other error // errors.Is でも一致してほしいもの（nil なら見ない）
}

func bindRejectionCases() []bindRejectionCase {
	cases := []bindRejectionCase{
		{
			name: "challenge already has a source binding",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.putIssue("o/r#1", "T", "B")
				f.putIssue("o/r#2", "T", "B")
				ch := f.newChallenge(t, "t", "d")
				if _, err := f.bind(ch.ID, "o/r#1"); err != nil {
					t.Fatalf("first bind: %v", err)
				}
				return ch.ID, "o/r#2"
			},
			want:  ErrAlreadyBound,
			other: ErrValidation,
		},
		{
			name: "issue is already bound to another challenge",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.putIssue("o/r#1", "T", "B")
				first := f.newChallenge(t, "first", "d")
				if _, err := f.bind(first.ID, "o/r#1"); err != nil {
					t.Fatalf("first bind: %v", err)
				}
				return f.newChallenge(t, "second", "d").ID, "o/r#1"
			},
			want:  ErrIssueBoundElsewhere,
			other: ErrValidation,
		},
		{
			name: "issue's repo is not in any declared source",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.putIssue("x/undeclared#1", "T", "B")
				return f.newChallenge(t, "t", "d").ID, "x/undeclared#1"
			},
			want:  ErrRepoNotDeclared,
			other: ErrValidation,
		},
		{
			name: "issue is closed",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				issue := f.putIssue("o/r#1", "T", "B")
				issue.State = "closed"
				f.up.getIssue["o/r#1"] = issue
				return f.newChallenge(t, "t", "d").ID, "o/r#1"
			},
			want:  ErrIssueNotOpen,
			other: ErrValidation,
		},
		{
			name: "issue does not exist upstream (404)",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.up.getIssueErr = map[string]error{"o/r#1": ErrUpstreamIssueNotFound}
				return f.newChallenge(t, "t", "d").ID, "o/r#1"
			},
			want:  ErrUpstreamIssueNotFound,
			other: ErrValidation,
		},
		{
			name: "issue was transferred",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.up.getIssueErr = map[string]error{"o/r#1": ErrUpstreamIssueTransferred}
				return f.newChallenge(t, "t", "d").ID, "o/r#1"
			},
			want:  ErrUpstreamIssueTransferred,
			other: ErrValidation,
		},
		{
			name: "response is for another repository",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				issue := f.putIssue("o/r#1", "T", "B")
				issue.ExternalKey = "x/y#1"
				f.up.getIssue["o/r#1"] = issue
				return f.newChallenge(t, "t", "d").ID, "o/r#1"
			},
			want:  ErrUpstreamIssueTransferred,
			other: ErrValidation,
		},
		{
			name: "fetching the issue fails for another reason",
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				f.up.getIssueErr = map[string]error{"o/r#1": errors.New("boom")}
				return f.newChallenge(t, "t", "d").ID, "o/r#1"
			},
			want: ErrUpstreamFetchFailed,
		},
		{
			name: "challenge does not exist",
			setup: func(_ *testing.T, f *bindFixture) (string, string) {
				f.putIssue("o/r#1", "T", "B")
				return "C-99", "o/r#1"
			},
			want: ErrNotFound,
		},
		{
			name: "malformed challenge id",
			setup: func(_ *testing.T, f *bindFixture) (string, string) {
				f.putIssue("o/r#1", "T", "B")
				return "nope", "o/r#1"
			},
			want: ErrNotFound,
		},
	}
	for _, key := range []string{"", "o/r", "o/r#", "o/r#0", "o/r#x", "#5", "or#5", "o/#5", "/r#5", "a/b/c#5", "o/r #5"} {
		key := key
		cases = append(cases, bindRejectionCase{
			name: fmt.Sprintf("malformed external key %q", key),
			setup: func(t *testing.T, f *bindFixture) (string, string) {
				return f.newChallenge(t, "t", "d").ID, key
			},
			want: ErrValidation,
		})
	}
	return cases
}

func TestBind_Rejections_RecordNothing(t *testing.T) {
	for _, tc := range bindRejectionCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindFixture(t)
			id, key := tc.setup(t, f)
			before := f.state(t, id)

			_, err := f.bind(id, key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("BindChallenge error = %v, want %v", err, tc.want)
			}
			if tc.other != nil && !errors.Is(err, tc.other) {
				t.Errorf("error %v should also be %v", err, tc.other)
			}
			if after := f.state(t, id); after != before {
				t.Errorf("state = %+v, want %+v (nothing recorded)", after, before)
			}
		})
	}
}

func TestBind_RejectionReasonsAreDistinguishableByMessage(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#1", "T", "B")
	f.putIssue("o/r#2", "T", "B")
	closed := f.putIssue("o/r#3", "T", "B")
	closed.State = "closed"
	f.up.getIssue["o/r#3"] = closed
	f.putIssue("x/y#4", "T", "B")

	bound := f.newChallenge(t, "bound", "d")
	if _, err := f.bind(bound.ID, "o/r#1"); err != nil {
		t.Fatal(err)
	}
	free := f.newChallenge(t, "free", "d")

	msgs := map[string]string{}
	sentinels := map[string]error{"already": ErrAlreadyBound, "elsewhere": ErrIssueBoundElsewhere, "undeclared": ErrRepoNotDeclared, "closed": ErrIssueNotOpen}
	for name, call := range map[string]func() error{
		"already":    func() error { _, err := f.bind(bound.ID, "o/r#2"); return err },
		"elsewhere":  func() error { _, err := f.bind(free.ID, "o/r#1"); return err },
		"undeclared": func() error { _, err := f.bind(free.ID, "x/y#4"); return err },
		"closed":     func() error { _, err := f.bind(free.ID, "o/r#3"); return err },
	} {
		err := call()
		if !errors.Is(err, sentinels[name]) {
			t.Fatalf("%s: error = %v, want %v", name, err, sentinels[name])
		}
		msgs[name] = err.Error()
	}
	// 理由の文言そのもの（sentinel の Error()）が互いに異なる。
	seen := map[string]string{}
	for name, s := range sentinels {
		if other, dup := seen[s.Error()]; dup {
			t.Errorf("%s and %s share the reason text %q", name, other, s.Error())
		}
		seen[s.Error()] = name
	}
	if !strings.Contains(msgs["elsewhere"], bound.ID) {
		t.Errorf("message %q should name the challenge that holds the issue (%s)", msgs["elsewhere"], bound.ID)
	}
	if !strings.Contains(msgs["undeclared"], "x/y") {
		t.Errorf("message %q should name the repo", msgs["undeclared"])
	}
}

func TestBind_AlreadyBoundChallenge_DoesNotCallUpstream(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#1", "T", "B")
	f.putIssue("o/r#2", "T", "B")
	ch := f.newChallenge(t, "t", "d")
	if _, err := f.bind(ch.ID, "o/r#1"); err != nil {
		t.Fatal(err)
	}
	calls := len(f.up.getIssueCalls)
	if _, err := f.bind(ch.ID, "o/r#2"); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("error = %v, want ErrAlreadyBound", err)
	}
	if len(f.up.getIssueCalls) != calls {
		t.Error("the upstream was called although the challenge is already bound")
	}
}

func TestBind_UndeclaredRepo_DoesNotCallUpstream(t *testing.T) {
	f := newBindFixture(t)
	ch := f.newChallenge(t, "t", "d")
	if _, err := f.bind(ch.ID, "x/undeclared#1"); !errors.Is(err, ErrRepoNotDeclared) {
		t.Fatalf("error = %v, want ErrRepoNotDeclared", err)
	}
	if len(f.up.getIssueCalls) != 0 {
		t.Errorf("the upstream was called for an undeclared repo: %v", f.up.getIssueCalls)
	}
}

// bindRaceUpstream は GetIssue の応答を返した後に onGet を呼ぶ（取得と書き込みの間に
// 別の書き込みが入る状況を作る）。
type bindRaceUpstream struct {
	*fakeUpstream
	onGet func()
}

func (r *bindRaceUpstream) GetIssue(ctx context.Context, repo string, number int) (UpstreamIssue, error) {
	issue, err := r.fakeUpstream.GetIssue(ctx, repo, number)
	if r.onGet != nil {
		r.onGet()
	}
	return issue, err
}

func TestBind_RereadsConditionsInsideTheWriteTransaction(t *testing.T) {
	t.Run("the issue gets bound to another challenge after the fetch", func(t *testing.T) {
		f := newBindFixture(t)
		f.putIssue("o/r#1", "T", "B")
		mine := f.newChallenge(t, "mine", "d")
		other := f.newChallenge(t, "other", "d")
		race := &bindRaceUpstream{fakeUpstream: f.up, onGet: func() {
			if _, err := createSourceBindingForTest(f.s, other.ID, time.Now(), validBindingInput("o/r#1")); err != nil {
				t.Errorf("racing binding: %v", err)
			}
		}}
		before := f.state(t, mine.ID)
		_, err := f.s.BindChallenge(context.Background(), ChannelCLI, BindInput{ChallengeID: mine.ID, ExternalKey: "o/r#1", Sources: f.sources, Upstream: race})
		if !errors.Is(err, ErrIssueBoundElsewhere) {
			t.Fatalf("error = %v, want ErrIssueBoundElsewhere", err)
		}
		after := f.state(t, mine.ID)
		// 割り込んだ対応の 1 件だけが増えている。こちらの課題の版・作業ログは変わらない。
		if after.version != before.version || after.activities != before.activities || after.bindings != before.bindings+1 {
			t.Errorf("state changed: before %+v after %+v", before, after)
		}
	})
	t.Run("the challenge gets bound to another issue after the fetch", func(t *testing.T) {
		f := newBindFixture(t)
		f.putIssue("o/r#1", "T", "B")
		mine := f.newChallenge(t, "mine", "d")
		race := &bindRaceUpstream{fakeUpstream: f.up, onGet: func() {
			if _, err := createSourceBindingForTest(f.s, mine.ID, time.Now(), validBindingInput("o/r#9")); err != nil {
				t.Errorf("racing binding: %v", err)
			}
		}}
		_, err := f.s.BindChallenge(context.Background(), ChannelCLI, BindInput{ChallengeID: mine.ID, ExternalKey: "o/r#1", Sources: f.sources, Upstream: race})
		if !errors.Is(err, ErrAlreadyBound) {
			t.Fatalf("error = %v, want ErrAlreadyBound", err)
		}
		if got := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'bind'`); got != 0 {
			t.Errorf("bind activities = %d, want 0", got)
		}
	})
}

// --- bind の後の取り込み ---

func TestBind_ThenIngestUpdatesTheBoundChallengeWithoutCreatingANewOne(t *testing.T) {
	f := newBindFixture(t)
	// 本文は課題の説明と同じ。それでも fingerprint が本物と一致しないので、見出しが置き換わる。
	f.putIssue("o/r#7", "UP-TITLE", "SAME-BODY")
	ch := f.newChallenge(t, "OWN-TITLE", "SAME-BODY")
	if _, err := f.bind(ch.ID, "o/r#7"); err != nil {
		t.Fatalf("BindChallenge: %v", err)
	}
	versionAfterBind := f.state(t, ch.ID).version

	res := runIngestForTest(t, f.s, f.sources, f.up)
	item := findIngestItem(res, "o/r#7")
	if item == nil || item.Result != IngestOutcomeUpdated || item.ChallengeID != ch.ID {
		t.Fatalf("item = %+v, want updated for %s", item, ch.ID)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM challenge`); n != 1 {
		t.Errorf("challenges = %d, want 1 (no new challenge)", n)
	}
	d, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if d.Title != "UP-TITLE" || d.Description != "SAME-BODY" || d.Version != versionAfterBind+1 {
		t.Errorf("challenge = %+v, want the upstream title and version %d", d.Challenge, versionAfterBind+1)
	}
	if d.SourceBinding.Fingerprint != Fingerprint("SAME-BODY") {
		t.Errorf("fingerprint = %q, want the real one after the ingest", d.SourceBinding.Fingerprint)
	}

	res = runIngestForTest(t, f.s, f.sources, f.up)
	if item := findIngestItem(res, "o/r#7"); item == nil || item.Result != IngestOutcomeUnchanged {
		t.Errorf("second ingest item = %+v, want unchanged", item)
	}
}

func TestBind_ThenIngestReplacesDescriptionAndUrgency(t *testing.T) {
	f := newBindFixture(t)
	issue := f.putIssue("o/r#7", "UP-TITLE", "UP-BODY")
	ch := f.newChallenge(t, "OWN-TITLE", "OWN-DESC")
	if _, err := f.bind(ch.ID, "o/r#7"); err != nil {
		t.Fatal(err)
	}
	f.sources[0].UrgencyLabels = map[string]string{"prio": "高"}
	issue.Labels = []string{"prio"}
	f.up.issues["o/r"] = []UpstreamIssue{issue}

	runIngestForTest(t, f.s, f.sources, f.up)
	d, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if d.Title != "UP-TITLE" || d.Description != "UP-BODY" || d.Urgency == nil || *d.Urgency != UrgencyHigh {
		t.Errorf("challenge = %+v, want upstream title/description and urgency 高", d.Challenge)
	}
}

// --- bind の後の J2 ---

func TestBind_ThenJ2CanSelectAnOperationThatNeedsTheSource(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "bind then plan", "P1")
	implOutput := j2OutputFor(true, nil) // flywheel/impl は {issue_number} を持つ操作

	// 対応が無いうちは取り込み元の差し込みを持つ操作を選べない（出力は検査で落ちる）。
	var launched JudgmentLaunchInput
	inv := succeededInvoker(implOutput)
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2 before bind: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("before bind: items = %+v, want invalid_output", res.Items)
	}
	if text := allSectionsText(launched.Sections); !strings.Contains(text, "取り込み元の Issue の情報を必要とするため、この課題では選べない") {
		t.Errorf("before bind: the input lacks the note that source-dependent operations cannot be chosen:\n%s", text)
	}

	// bind した後は、同じ出力が通り、注記も外れる。
	up := &fakeUpstream{getIssue: map[string]UpstreamIssue{}}
	up.getIssue["o/r#7"] = UpstreamIssue{ExternalKey: "o/r#7", Repo: "o/r", Number: 7, Title: "UP-TITLE", Body: "UP-BODY", State: "open", URL: "https://github.com/o/r/issues/7", UpdatedAt: "2026-09-25T09:00:00Z"}
	if _, err := f.s.BindChallenge(context.Background(), ChannelCLI, BindInput{
		ChallengeID: ch.ID, ExternalKey: "o/r#7", Sources: []SourceEntry{excludeOthersSource("src", []string{"o/r"}, nil)}, Upstream: up,
	}); err != nil {
		t.Fatalf("BindChallenge: %v", err)
	}
	f.upstream.threads["o/r#7"] = UpstreamIssueThread{Issue: UpstreamIssue{ExternalKey: "o/r#7", Repo: "o/r", Number: 7, Title: "UP-TITLE", Body: "UP-BODY", State: "open", UpdatedAt: "2026-09-25T09:00:00Z"}}

	launched = JudgmentLaunchInput{}
	inv = succeededInvoker(implOutput)
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	res, err = f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2 after bind: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "plan" {
		t.Fatalf("after bind: items = %+v, want a registered plan", res.Items)
	}
	text := allSectionsText(launched.Sections)
	if strings.Contains(text, "この課題では選べない") || !strings.Contains(text, "この課題は取り込み元の GitHub Issue と対応している") {
		t.Errorf("after bind: the declaration section still treats the challenge as unbound:\n%s", text)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", d.Status)
	}
}

// --- unbind ---

func TestUnbind_RemovesBindingRecordsActivityAndBumpsVersion(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#7", "UP-TITLE", "UP-BODY")
	ch := f.newChallenge(t, "OWN-TITLE", "OWN-DESC")
	if _, err := f.bind(ch.ID, "o/r#7"); err != nil {
		t.Fatal(err)
	}
	runIngestForTest(t, f.s, f.sources, f.up) // 本物の fingerprint に更新される
	boundDetail, _ := f.s.GetChallenge(context.Background(), ch.ID)
	before := f.state(t, ch.ID)

	got, err := f.unbind(ch.ID)
	if err != nil {
		t.Fatalf("UnbindChallenge: %v", err)
	}
	if got.Version != before.version+1 {
		t.Errorf("returned version = %d, want %d", got.Version, before.version+1)
	}
	d, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if d.SourceBinding != nil {
		t.Errorf("source binding after unbind = %+v, want none", d.SourceBinding)
	}
	if d.Version != before.version+1 || d.Title != boundDetail.Title || d.Description != boundDetail.Description || d.Status != boundDetail.Status {
		t.Errorf("challenge after unbind = %+v, want only the version changed", d.Challenge)
	}
	if got := f.state(t, ch.ID); got.activities != before.activities+1 || got.bindings != before.bindings-1 {
		t.Errorf("state = %+v, want one activity more and one binding less than %+v", got, before)
	}
	act := lastActivity(t, f.s, ch.ID)
	if act.Entity != "challenge" || act.Action != "unbind" || act.Channel != string(ChannelCLI) || act.Verification != string(VerificationNone) {
		t.Errorf("activity = %+v, want challenge/unbind via cli with no verification", act)
	}
	b, a := decodeObject(t, act.Before), decodeObject(t, act.After)
	if b["external_key"] != "o/r#7" || b["source_id"] != "src-a" || b["fingerprint"] != Fingerprint("UP-BODY") {
		t.Errorf("activity before = %v, want the removed binding", b)
	}
	if a["version"] != float64(before.version+1) {
		t.Errorf("activity after = %v, want the new version", a)
	}
}

func TestUnbind_Rejections_RecordNothing(t *testing.T) {
	f := newBindFixture(t)
	ch := f.newChallenge(t, "t", "d")
	before := f.state(t, ch.ID)

	if _, err := f.unbind(ch.ID); !errors.Is(err, ErrNotBound) || !errors.Is(err, ErrValidation) {
		t.Errorf("unbind of an unbound challenge: error = %v, want ErrNotBound (a validation failure)", err)
	}
	for _, id := range []string{"C-99", "bogus", ""} {
		if _, err := f.unbind(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("unbind of %q: error = %v, want ErrNotFound", id, err)
		}
	}
	if after := f.state(t, ch.ID); after != before {
		t.Errorf("state = %+v, want %+v", after, before)
	}
}

func TestUnbind_ThenIngestCreatesANewChallengeAndLeavesTheOriginalAlone(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#7", "UP-TITLE", "UP-BODY") // assignee なし＝exclude-others のポリシーに合う
	ch := f.newChallenge(t, "OWN-TITLE", "OWN-DESC")
	if _, err := f.bind(ch.ID, "o/r#7"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.unbind(ch.ID); err != nil {
		t.Fatal(err)
	}
	original, _ := f.s.GetChallenge(context.Background(), ch.ID)

	res := runIngestForTest(t, f.s, f.sources, f.up)
	item := findIngestItem(res, "o/r#7")
	if item == nil || item.Result != IngestOutcomeCreated || item.ChallengeID == ch.ID {
		t.Fatalf("item = %+v, want created as a new challenge", item)
	}
	after, _ := f.s.GetChallenge(context.Background(), ch.ID)
	if after.Version != original.Version || after.Title != "OWN-TITLE" || after.Description != "OWN-DESC" || after.SourceBinding != nil {
		t.Errorf("original challenge = %+v (binding %+v), want unchanged and unbound", after.Challenge, after.SourceBinding)
	}
}

func TestBindUnbindBind_TheFreedIssueCanBeBoundToAnotherChallenge(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#7", "T", "B")
	first := f.newChallenge(t, "first", "d")
	second := f.newChallenge(t, "second", "d")
	if _, err := f.bind(first.ID, "o/r#7"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.unbind(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bind(second.ID, "o/r#7"); err != nil {
		t.Fatalf("bind of the freed issue to another challenge: %v", err)
	}
}

// --- 親子・書き出し／再構築との整合 ---

func TestUnbind_DoesNotDisturbParentSyncOrExport(t *testing.T) {
	f := newBindFixture(t)
	parentIssue := f.putIssue("o/r#1", "PARENT", "pb")
	childIssue := f.putIssue("o/r#2", "CHILD", "cb")
	childIssue.ParentExternalKey = "o/r#1"
	f.up.getIssue["o/r#2"] = childIssue
	f.up.issues["o/r"] = []UpstreamIssue{parentIssue, childIssue}

	runIngestForTest(t, f.s, f.sources, f.up) // 2 つの課題ができ、親子が付く
	if n := f.count(t, `SELECT COUNT(*) FROM challenge_parent WHERE challenge_id = 2 AND parent_id = 1`); n != 1 {
		t.Fatalf("parent link rows = %d, want 1 before unbind", n)
	}
	if _, err := f.unbind("C-2"); err != nil {
		t.Fatalf("UnbindChallenge: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM challenge_parent`); n != 1 {
		t.Errorf("parent links after unbind = %d, want 1 (unbind does not touch parent links)", n)
	}
	// 外した後の取り込みは、親子の同期でも失敗しない。
	runIngestForTest(t, f.s, f.sources, f.up)

	// 書き出した内容は、そのまま新しいストアへ再構築できる。
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, f.s)); err != nil {
		t.Fatalf("Import after unbind: %v", err)
	}
	if d, err := dst.GetChallenge(context.Background(), "C-1"); err != nil || d.SourceBinding == nil || d.SourceBinding.ExternalKey != "o/r#1" {
		t.Errorf("imported parent = %+v, %v", d, err)
	}
}

func TestBind_ExportImportRoundTripKeepsTheBindMarker(t *testing.T) {
	f := newBindFixture(t)
	f.putIssue("o/r#7", "T", "B")
	ch := f.newChallenge(t, "t", "d")
	if _, err := f.bind(ch.ID, "o/r#7"); err != nil {
		t.Fatal(err)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, f.s)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d, err := dst.GetChallenge(context.Background(), ch.ID)
	if err != nil || d.SourceBinding == nil || d.SourceBinding.Fingerprint != boundFingerprint {
		t.Errorf("imported binding = %+v, %v, want the bind marker", d, err)
	}
}
