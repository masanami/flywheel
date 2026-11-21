package core

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 統合先宛て（entity=task）の conflict_fix_passed は、課題の作業ログの区画（entity=challenge・operation）に入らない。
// J6 の入力は、通したコンフリクトの解消を専用の区画で渡す。
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
	for _, want := range []string{"統合先", "T-" + strconv.FormatInt(tid, 10), "aaa111", "bbb222", "go.sum", "R-5", "通らなかった確かめ"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
}

// 反映先宛て（entity=operation）の記録は、区画に「反映先」と操作の ID で出る。
func TestPrepareJ6_ReleaseConflictFixShowsTargetAndOperationID(t *testing.T) {
	t.Parallel()
	opID, taskID := "O-7", "T-3"
	got := ""
	for _, sec := range prepareJ6(&j6Context{ConflictFixes: []ConflictFixPassed{
		{At: time.Now(), Target: ConflictFixTargetRelease, OperationID: &opID, FromRef: "aaa", ToRef: "bbb", Files: []string{"go.mod"}},
		{At: time.Now(), Target: ConflictFixTargetIntegration, TaskID: &taskID, FromRef: "ccc", ToRef: "ddd", Files: []string{"go.sum"}},
	}}, "") {
		if sec.Label == j6ConflictFixesLabel {
			got = sec.Content
		}
	}
	for _, want := range []string{"反映先 / O-7 / aaa → bbb", "統合先 / T-3 / ccc → ddd"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
}

// 通したコンフリクトの解消が無い課題には、区画を載せない。
func TestPrepareJ6_NoConflictFixes_NoSection(t *testing.T) {
	t.Parallel()
	for _, sec := range prepareJ6(&j6Context{}, "") {
		if sec.Label == j6ConflictFixesLabel {
			t.Errorf("unexpected section: %q", sec.Content)
		}
	}
}

// memory の confidence の型は、空白を含む値を先に弾く（空は許す）。追記の項目は core が確度を見ないので、空は型で
// 拒否しない。空の新規作成を拒否するのは書き込み器（memory_write.go）で、この型ではない。
// ここで確かめるのは、型の pattern を Go の正規表現で解釈した結果だけである。claude が --json-schema の pattern を
// 守らせるかは確かめていない。
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
	for s, want := range map[string]bool{"high": true, "medium": true, "low": true, "high (実測)": false, "": true, "a b": false, "a\tb": false} {
		if re.MatchString(s) != want {
			t.Errorf("pattern(%q) = %v", s, !want)
		}
	}
}
