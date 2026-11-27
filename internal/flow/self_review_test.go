package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// レビューのステップの遷移を、偽の claude の応答で確かめる（レビューのステップ化。docs/features/flow-customization.md の「レビューのステップ」）。

func waitEnd(f *fixture, id string) contractDoc {
	f.t.Helper()
	for i := 0; i < 1200; i++ {
		d := f.status(id)
		if d.State == "waiting" || d.State == "failed" || d.State == "succeeded" {
			f.waitRunnerExit(id)
			return f.status(id)
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatalf("run %s did not stop", id)
	return contractDoc{}
}

func (f *fixture) prBody() string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.ghDir, "body.1.md"))
	if err != nil {
		f.t.Fatalf("no PR body: %v", err)
	}
	return string(b)
}

func fixPass() string {
	return claudeStructured(map[string]any{"outcome": "pass", "summary": "fixed", "deviation_report": ""}, 1.0)
}

func commitOK() string {
	return claudeStructured(map[string]any{"outcome": "committed", "commit_sha": "abc", "summary": "committed"}, 0.5)
}

// 指摘あり → 直す → レビューへ戻る → 指摘なし → commit。直すステップには指摘の一覧が渡り、facts.self_review は complete。
func TestSelfReviewFindingsGoToFixAndBack(t *testing.T) {
	f := newFixture(t, "5.3.0")
	f.respond(3, claudeStructured(reviewResult("findings"), 1.0))
	f.respond(4, fixPass())
	f.respond(5, claudeStructured(reviewResult("clean"), 1.0))
	f.respond(6, commitOK())
	id := f.runToReview()
	joined := strings.Join(f.lightEvents(id).all, " ")
	for _, want := range []string{"implement:pass→self-review", "self-review:findings→self-review-fix", "self-review-fix:pass→self-review", "self-review:clean→commit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("transitions lack %q: %s", want, joined)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(f.fdir, "calls", "4.stdin"))
	if attached := string(stdin); !strings.Contains(attached, `"findings"`) || !strings.Contains(attached, `"claim": "typo"`) {
		t.Errorf("the fix step was not given the findings:\n%s", attached)
	}
	if got := f.status(id).Facts["self_review"]; got != "complete" {
		t.Errorf("facts.self_review = %q, want complete", got)
	}
	// ステップの記録に指摘の数が残る: レビューの 1 回目は 1 件・2 回目は 0 件、指摘の一覧を返さないステップは null。
	var reviews []int
	d := f.status(id)
	if d.Steps == nil {
		t.Fatal("no steps in the contract")
	}
	for _, s := range *d.Steps {
		switch {
		case s.Step == "self-review" && s.FindingsCount != nil:
			reviews = append(reviews, *s.FindingsCount)
		case s.Step == "self-review":
			t.Errorf("self-review seq %d has no findings_count", s.Seq)
		case s.FindingsCount != nil:
			t.Errorf("%s has findings_count %d", s.Step, *s.FindingsCount)
		}
	}
	if len(reviews) != 2 || reviews[0] != 1 || reviews[1] != 0 {
		t.Errorf("self-review findings_count = %v, want [1 0]", reviews)
	}
}

// 戻る回数の上限（ticket-light は 1）に達しても指摘が残れば、commit へ進み、残った指摘を PR に載せる。
func TestSelfReviewExhaustedGoesToCommitWithResidualFindings(t *testing.T) {
	f, extra := newLightFixture(t)
	f.respond(2, claudeStructured(reviewResult("findings"), 0.5))
	f.respond(3, fixPass())
	f.respond(4, claudeStructured(reviewResult("findings"), 0.5))
	f.respond(5, commitOK())
	id := f.startLight(extra...)
	w := waitEnd(f, id)
	if w.State != "waiting" || w.RequestedAction == nil || w.RequestedAction.Kind != "review" {
		t.Fatalf("state %s (%s), want waiting at review", w.State, w.Summary)
	}
	l := f.lightEvents(id)
	joined := strings.Join(l.all, " ")
	if !strings.Contains(joined, "self-review:findings→commit") {
		t.Errorf("the exhausted review did not go to commit: %s", joined)
	}
	if n := strings.Count(strings.Join(l.started, " "), "self-review-fix"); n != 1 {
		t.Errorf("self-review-fix started %d times, want 1 (limit 1): %v", n, l.started)
	}
	body := f.prBody()
	if !strings.Contains(body, "残指摘（全 1 件）") || !strings.Contains(body, "typo") {
		t.Errorf("the PR body does not carry the residual finding:\n%s", body)
	}
	if got := w.Facts["self_review"]; got != "complete" {
		t.Errorf("facts.self_review = %q, want complete", got)
	}
}

// 直すべき指摘なし → commit へ進み、PR の残指摘はレビューのステップの値（0 件）。レビューの未検証も PR に載る。
func TestSelfReviewCleanGoesToCommit(t *testing.T) {
	f := newFixture(t, "5.3.0")
	clean := reviewResult("clean")
	clean["codex_review"] = "failed"
	clean["unverified"] = []string{"Codex のレビューが完了しなかった（起動できない）"}
	f.respond(3, claudeStructured(clean, 0))
	id := f.runToReview()
	if got := f.lightEvents(id).transitions["self-review"]; got != "clean→commit" {
		t.Errorf("self-review: %s", got)
	}
	body := f.prBody()
	if !strings.Contains(body, "残指摘（全 0 件）") || !strings.Contains(body, "Codex のレビューが完了しなかった") {
		t.Errorf("PR body:\n%s", body)
	}
}

// レビューを最後まで行えなかったときは、PR へ進めずに self_review_incomplete で止まる。
func TestSelfReviewIncompleteFails(t *testing.T) {
	for _, light := range []bool{false, true} {
		t.Run(map[bool]string{false: "ticket", true: "ticket-light"}[light], func(t *testing.T) {
			var f *fixture
			var id string
			if light {
				var extra []string
				f, extra = newLightFixture(t)
				f.respond(2, claudeStructured(reviewResult("incomplete"), 0.5))
				id = f.startLight(extra...)
			} else {
				f = newFixture(t, "5.3.0")
				f.respond(3, claudeStructured(reviewResult("incomplete"), 0.5))
				d, errOut, code := f.start()
				if code != ExitOK || d.RunID == nil {
					t.Fatalf("start exit %d\n%s", code, errOut)
				}
				id = *d.RunID
			}
			w := waitEnd(f, id)
			if w.State != "failed" || !strings.Contains(w.Summary, "self_review_incomplete") {
				raw, _ := json.Marshal(w)
				t.Fatalf("state %s, want failed with self_review_incomplete: %s", w.State, raw)
			}
			if _, err := os.Stat(filepath.Join(f.ghDir, "body.1.md")); err == nil {
				t.Errorf("a PR was created although the review was incomplete")
			}
		})
	}
}

// outcome と self_review・findings が食い違う出力は型に合わず、指摘があるのに clean で commit へ進むことはない。
func TestSelfReviewInconsistentOutputIsInvalid(t *testing.T) {
	f := newFixture(t, "5.3.0")
	bad := reviewResult("findings")
	bad["outcome"] = "clean" // 指摘が 1 件あるのに clean
	f.respond(3, claudeStructured(bad, 1.0))
	d, errOut, code := f.start()
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	w := waitEnd(f, *d.RunID)
	joined := strings.Join(f.lightEvents(*d.RunID).all, " ")
	if strings.Contains(joined, "self-review:clean") || strings.Contains(joined, "→commit") {
		t.Errorf("an inconsistent review output was followed: %s", joined)
	}
	if w.State == "succeeded" || !strings.Contains(w.Summary, "invalid_output") {
		raw, _ := json.Marshal(w)
		t.Errorf("state %s: %s", w.State, raw)
	}
	if _, err := os.Stat(filepath.Join(f.ghDir, "body.1.md")); err == nil {
		t.Errorf("a PR was created from an inconsistent review output")
	}
}
