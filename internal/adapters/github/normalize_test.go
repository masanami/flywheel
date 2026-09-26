package github

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizeIssue_MapsAllFields(t *testing.T) {
	raw := []byte(`{
		"number": 49,
		"title": "上流の close を検出する",
		"body": "本文です",
		"user": {"login": "Masanami"},
		"assignees": [{"login": "masanami"}, {"login": "someone-else"}],
		"labels": [{"name": "priority:high"}, "needs-triage"],
		"state": "open",
		"html_url": "https://github.com/masanami/flywheel/issues/49",
		"repository_url": "https://api.github.com/repos/masanami/flywheel"
	}`)

	issue, err := normalizeIssue(raw)
	if err != nil {
		t.Fatalf("normalizeIssue: %v", err)
	}

	if issue.Repo != "masanami/flywheel" {
		t.Errorf("Repo = %q, want masanami/flywheel", issue.Repo)
	}
	if issue.Number != 49 {
		t.Errorf("Number = %d, want 49", issue.Number)
	}
	if issue.ExternalKey != "masanami/flywheel#49" {
		t.Errorf("ExternalKey = %q", issue.ExternalKey)
	}
	if issue.Title != "上流の close を検出する" {
		t.Errorf("Title = %q", issue.Title)
	}
	if issue.Body != "本文です" {
		t.Errorf("Body = %q", issue.Body)
	}
	if issue.Reporter != "Masanami" {
		t.Errorf("Reporter = %q", issue.Reporter)
	}
	if len(issue.Assignees) != 2 || issue.Assignees[0] != "masanami" || issue.Assignees[1] != "someone-else" {
		t.Errorf("Assignees = %v", issue.Assignees)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "priority:high" || issue.Labels[1] != "needs-triage" {
		t.Errorf("Labels = %v", issue.Labels)
	}
	if issue.State != "open" {
		t.Errorf("State = %q", issue.State)
	}
	if issue.URL != "https://github.com/masanami/flywheel/issues/49" {
		t.Errorf("URL = %q", issue.URL)
	}
}

func TestNormalizeIssue_NullBodyBecomesEmptyString(t *testing.T) {
	raw := []byte(`{
		"number": 1, "title": "t", "body": null, "user": {"login": "u"},
		"assignees": [], "labels": [], "state": "open",
		"html_url": "https://github.com/masanami/flywheel/issues/1",
		"repository_url": "https://api.github.com/repos/masanami/flywheel"
	}`)
	issue, err := normalizeIssue(raw)
	if err != nil {
		t.Fatalf("normalizeIssue: %v", err)
	}
	if issue.Body != "" {
		t.Errorf("Body = %q, want empty string", issue.Body)
	}
}

func TestNormalizeIssue_NoAssigneesOrLabels_ReturnsEmptySlicesNotNil(t *testing.T) {
	raw := []byte(`{
		"number": 1, "title": "t", "body": "b", "user": {"login": "u"},
		"assignees": [], "labels": [], "state": "open",
		"html_url": "https://github.com/masanami/flywheel/issues/1",
		"repository_url": "https://api.github.com/repos/masanami/flywheel"
	}`)
	issue, err := normalizeIssue(raw)
	if err != nil {
		t.Fatalf("normalizeIssue: %v", err)
	}
	if issue.Assignees == nil {
		t.Error("Assignees is nil, want non-nil empty slice")
	}
	if issue.Labels == nil {
		t.Error("Labels is nil, want non-nil empty slice")
	}
}

func TestNormalizeIssue_ClosedState(t *testing.T) {
	raw := []byte(`{
		"number": 1, "title": "t", "body": "b", "user": {"login": "u"},
		"assignees": [], "labels": [], "state": "closed",
		"html_url": "https://github.com/masanami/flywheel/issues/1",
		"repository_url": "https://api.github.com/repos/masanami/flywheel"
	}`)
	issue, err := normalizeIssue(raw)
	if err != nil {
		t.Fatalf("normalizeIssue: %v", err)
	}
	if issue.State != "closed" {
		t.Errorf("State = %q, want closed", issue.State)
	}
}

func TestNormalizeIssue_UnknownState_Fails(t *testing.T) {
	raw := []byte(`{
		"number": 1, "title": "t", "body": "b", "user": {"login": "u"},
		"assignees": [], "labels": [], "state": "merged",
		"html_url": "https://github.com/masanami/flywheel/issues/1",
		"repository_url": "https://api.github.com/repos/masanami/flywheel"
	}`)
	_, err := normalizeIssue(raw)
	if err == nil {
		t.Fatal("want error for state other than open/closed")
	}
}

func TestNormalizeIssue_UnparseableRepositoryURL_Fails(t *testing.T) {
	raw := []byte(`{
		"number": 1, "title": "t", "body": "b", "user": {"login": "u"},
		"assignees": [], "labels": [], "state": "open",
		"html_url": "https://github.com/masanami/flywheel/issues/1",
		"repository_url": "https://api.github.com/not-a-repos-path"
	}`)
	_, err := normalizeIssue(raw)
	if err == nil {
		t.Fatal("want error for unparseable repository_url")
	}
}

func TestNormalizeIssue_MalformedJSON_Fails(t *testing.T) {
	_, err := normalizeIssue([]byte(`{not json`))
	if err == nil {
		t.Fatal("want error for malformed JSON")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("want wrapped json.SyntaxError, got %v", err)
	}
}
