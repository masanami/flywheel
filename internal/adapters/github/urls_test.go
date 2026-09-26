package github

import "testing"

func TestListIssuesURL_IncludesStateOpenPerPage100AndPage(t *testing.T) {
	got := listIssuesURL("masanami/flywheel", 2)
	want := "repos/masanami/flywheel/issues?state=open&per_page=100&page=2"
	if got != want {
		t.Errorf("listIssuesURL = %q, want %q", got, want)
	}
}

func TestGetIssueURL(t *testing.T) {
	got := getIssueURL("masanami/flywheel", 49)
	want := "repos/masanami/flywheel/issues/49"
	if got != want {
		t.Errorf("getIssueURL = %q, want %q", got, want)
	}
}
