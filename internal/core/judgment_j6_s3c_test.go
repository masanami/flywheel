package core

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 統合先宛て（entity=task）の conflict_fix_passed は、課題の作業ログの区画（entity=challenge・operation）に入らない。
// J6 の入力は、通した衝突の解消を専用の区画で渡す。
func TestLoadJ6Context_CarriesIntegrationConflictFixPassed(t *testing.T) {
	t.Parallel()
	f := newJ6Fixture(t)
	id := f.challenge(t, StatusDone)
	cid, _ := parseChallengeID(id)
	tid := f.exec(t, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		VALUES (?, 1, 'k', 't', 'b', 'd', 'o/r', 'none', 'done', ?, ?)`, cid, formatTimestamp(time.Now()), formatTimestamp(time.Now()))
	f.exec(t, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after) VALUES (?, 'a', 'cli', 'none', 'task', ?, ?, ?, ?)`,
		formatTimestamp(time.Now()), tid, ActivityActionConflictFixPassed, `{"ref":"aaa111"}`, `{"ref":"bbb222","files":["go.sum"],"run_id":"R-5"}`)

	jc, err := f.s.loadJ6Context(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(jc.ConflictFixes) != 1 {
		t.Fatalf("ConflictFixes = %+v", jc.ConflictFixes)
	}
	var got string
	for _, sec := range prepareJ6(jc, "") {
		if sec.Label == j6ConflictFixesLabel {
			got = sec.Content
		}
	}
	for _, want := range []string{"統合先", "aaa111", "bbb222", "go.sum", "R-5"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
}

// 通した衝突の解消が無い課題には、区画を載せない。
func TestPrepareJ6_NoConflictFixes_NoSection(t *testing.T) {
	t.Parallel()
	for _, sec := range prepareJ6(&j6Context{}, "") {
		if sec.Label == j6ConflictFixesLabel {
			t.Errorf("unexpected section: %q", sec.Content)
		}
	}
}

// memory の confidence は 1 語（空白を含まない）。指示文と schema の両方で伝え、core の検査と同じ形にそろえる。
func TestJ6OutputSchema_ConfidencePatternIsSingleWord(t *testing.T) {
	t.Parallel()
	var sch map[string]any
	if err := json.Unmarshal(j6OutputSchema(), &sch); err != nil {
		t.Fatal(err)
	}
	items := sch["properties"].(map[string]any)["memory"].(map[string]any)["items"].(map[string]any)
	conf := items["properties"].(map[string]any)["confidence"].(map[string]any)
	pat, _ := conf["pattern"].(string)
	if pat == "" {
		t.Fatalf("confidence has no pattern: %+v", conf)
	}
	re := regexp.MustCompile(pat)
	for s, want := range map[string]bool{"high": true, "medium": true, "low": true, "high (実測)": false, "": false, "a b": false, "a\tb": false} {
		if re.MatchString(s) != want {
			t.Errorf("pattern(%q) = %v", s, !want)
		}
	}
}
