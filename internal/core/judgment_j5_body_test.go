package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeDescriptions は UpstreamDescriptionSource の偽の実装。
type fakeDescriptions struct {
	got   UpstreamArtifactDescription
	err   error
	calls []string
}

func (f *fakeDescriptions) GetArtifactDescription(_ context.Context, repo string, number int) (UpstreamArtifactDescription, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s#%d", repo, number))
	return f.got, f.err
}

// descriptionSection は J5 に渡した「成果物の説明文」の区画の中身を返す（区画が無ければ ok=false）。
func (f *prHeadFixture) descriptionSection() (string, bool) {
	for _, s := range f.j5Inputs[0].Sections {
		if s.Label == labelArtifactDescription {
			return s.Content, true
		}
	}
	return "", false
}

func (f *prHeadFixture) verifyWithDescriptions(t *testing.T, b UpstreamDescriptionSource) {
	t.Helper()
	in := f.input(f.cycle(t, 300), nil)
	in.Diffs, in.HeadGit, in.Connectors, in.Descriptions = f.diffs, f.git, worktreeConnectors(), b
	if _, err := f.s.VerifyAutoJ5(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAutoJ5_PassesTheArtifactDescription(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	b := &fakeDescriptions{got: UpstreamArtifactDescription{Text: "## 概要\n子の自己申告の本文", HeadSHA: f.sha}}
	f.verifyWithDescriptions(t, b)

	content, ok := f.descriptionSection()
	if !ok {
		t.Fatal("no artifact description section")
	}
	for _, want := range []string{j5PRURL, "子の自己申告の本文", "head " + f.sha + " の時点で取得"} {
		if !strings.Contains(content, want) {
			t.Errorf("section lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "別の head") {
		t.Errorf("unexpected head-mismatch notice:\n%s", content)
	}
	if got := f.stdin(t); !strings.Contains(got, "子の自己申告の本文") {
		t.Errorf("the description is not in the prompt:\n%s", got)
	}
	if len(b.calls) != 1 || b.calls[0] != "o/r#1" {
		t.Errorf("calls = %v", b.calls)
	}
}

func TestVerifyAutoJ5_ArtifactDescription_NotesAHeadMismatch(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	f.verifyWithDescriptions(t, &fakeDescriptions{got: UpstreamArtifactDescription{Text: "古い本文", HeadSHA: "older-head"}})

	content, ok := f.descriptionSection()
	if !ok {
		t.Fatal("no artifact description section")
	}
	for _, want := range []string{"head older-head の時点で取得", "説明文は別の head（older-head）のときに読んだもの", f.sha, "古い本文"} {
		if !strings.Contains(content, want) {
			t.Errorf("section lacks %q:\n%s", want, content)
		}
	}
}

func TestVerifyAutoJ5_ArtifactDescription_TruncatesAtTheLimit(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	text := strings.Repeat("あ", maxArtifactDescriptionRunes) + "末尾"
	f.verifyWithDescriptions(t, &fakeDescriptions{got: UpstreamArtifactDescription{Text: text, HeadSHA: f.sha}})

	content, ok := f.descriptionSection()
	if !ok {
		t.Fatal("no artifact description section")
	}
	if !strings.Contains(content, strings.Repeat("あ", maxArtifactDescriptionRunes)) || strings.Contains(content, "末尾") {
		t.Errorf("the description was not cut at %d runes", maxArtifactDescriptionRunes)
	}
	if !strings.Contains(content, "省略した") {
		t.Errorf("no omission notice:\n%s", content)
	}
	// ちょうど上限なら切らない。
	if _, cut := truncateRunes(strings.Repeat("あ", maxArtifactDescriptionRunes), maxArtifactDescriptionRunes); cut {
		t.Error("a description of exactly the limit was cut")
	}
}

func TestVerifyAutoJ5_ArtifactDescription_FetchFailureDoesNotStopJ5(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	f.verifyWithDescriptions(t, &fakeDescriptions{err: errors.New("fetch: boom")})

	content, ok := f.descriptionSection()
	if !ok || !strings.Contains(content, "説明文の取得に失敗した") || !strings.Contains(content, "fetch: boom") {
		t.Errorf("failure not recorded: %q", content)
	}
	if len(f.j5Inputs) != 1 {
		t.Fatalf("J5 launched %d times, want 1", len(f.j5Inputs))
	}
	if got := f.status(t); got != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s", got)
	}
}

func TestVerifyAutoJ5_NilDescriptionsAddsNoDescriptionSection(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	f.verifyWithDescriptions(t, nil)
	if _, ok := f.descriptionSection(); ok {
		t.Error("a description section was added without a source")
	}
}

// delegationReportSection は J5 に渡した「直前の委譲の報告」の区画の中身を返す。
func (f *prHeadFixture) delegationReportSection(t *testing.T) string {
	t.Helper()
	for _, s := range f.j5Inputs[0].Sections {
		if s.Label == "直前の委譲の報告" {
			return s.Content
		}
	}
	t.Fatal("no delegation report section")
	return ""
}

// asContractDelegation は直前の委譲の run を form: cli の契約の run（出力は契約の JSON）に書き換える。
func (f *prHeadFixture) asContractDelegation(t *testing.T) {
	t.Helper()
	if err := f.exec(`UPDATE run SET kind = 'contract', output = ? WHERE kind = 'delegate'`,
		`{"contract_version":1,"run_id":"ext-1","state":"waiting","summary":"CONTRACT-SUMMARY"}`); err != nil {
		t.Fatal(err)
	}
}

// ⑥ form: cli の委譲で成果物の PR の本文が取れたら、その本文（成果物の説明文の区画）を委譲の報告として読む。
func TestVerifyAutoJ5_ContractDelegationReportIsTheArtifactDescriptionWhenRead(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	f.asContractDelegation(t)
	f.verifyWithDescriptions(t, &fakeDescriptions{got: UpstreamArtifactDescription{Text: "## 概要\n子の自己申告の本文", HeadSHA: f.sha}})

	got := f.delegationReportSection(t)
	if !strings.Contains(got, "「"+labelArtifactDescription+"」の区画") || strings.Contains(got, "CONTRACT-SUMMARY") || strings.Contains(got, `"state"`) {
		t.Fatalf("report section = %q, want it to point at the artifact description, not the contract output", got)
	}
	if content, ok := f.descriptionSection(); !ok || !strings.Contains(content, "子の自己申告の本文") {
		t.Fatalf("artifact description section = %q (ok=%v)", content, ok)
	}
}

// ⑥ form: cli の委譲で成果物の PR の本文が取れないときは、契約の summary を委譲の報告に戻す。
func TestVerifyAutoJ5_ContractDelegationReportFallsBackToTheSummaryWhenTheDescriptionIsNotRead(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	f.asContractDelegation(t)
	f.verifyWithDescriptions(t, &fakeDescriptions{err: errors.New("gh failed")})

	got := f.delegationReportSection(t)
	if !strings.Contains(got, "CONTRACT-SUMMARY") || strings.Contains(got, "「"+labelArtifactDescription+"」の区画（") || strings.Contains(got, `"state"`) {
		t.Fatalf("report section = %q, want the contract summary as the report", got)
	}
}
