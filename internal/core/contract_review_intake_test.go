package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// このファイルは、課題が着手中のときに読んだ成果物つきのレビュー待ちを検証中へ進める前の確認（land・中身の識別・
// 反映先宛ての成果物の数・required_facts）を、実ストア・偽の ContractClient で固定する。

var requiredSelfReviewJSON = strings.Replace(reviewConnectorsJSON, `"contract_version": 1,`,
	`"contract_version": 1, "required_facts": {"self_review": ["complete"], "gate": ["pass", "skip"]},`, 1)

// intakeOut は、options・成果物・facts を指定したレビュー待ちの出力。
func intakeOut(opts []string, arts []ContractArtifact, facts map[string]string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: opts}
		st.Artifacts = arts
		st.Facts = facts
	})
}

var (
	intakeOptsWithLand = []string{"respond", "ready", "land"}
	intakeArtsOK       = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, reviewContentArtifact}
	intakeFactsMet     = map[string]string{"self_review": "complete", "gate": "pass"}
)

func newIntakeFixture(t *testing.T, connectorsJSON string) *reviewFlowFixture {
	t.Helper()
	cf := newContractFixtureWith(t, connectorsJSON)
	j := &j5Fixture{delegateFixture: cf.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("not_met", "FEEDBACK-TEXT", nil)}}
	j.j5.onInvoke = func(in JudgmentLaunchInput) { j.j5Inputs = append(j.j5Inputs, in) }
	j.checks.prs["o/r#1"] = prChecks("open", checkDone)
	f := &reviewFlowFixture{contractFixture: cf, j5: j}
	f.setPR(1, reviewPRURL, "open", "main")
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	return f
}

// readOnce は out をレビュー待ちとして 1 回読む。
func (f *reviewFlowFixture) readOnce(t *testing.T, out ContractLaunchOutput) {
	t.Helper()
	f.client.queue = []ContractLaunchOutput{out}
	f.poll(t)
}

func (f *reviewFlowFixture) lastHoldQuestion(t *testing.T) string {
	t.Helper()
	qs := f.holdQuestions(t)
	if len(qs) == 0 {
		t.Fatal("no hold")
	}
	return qs[len(qs)-1]
}

func (f *reviewFlowFixture) artifactRows(t *testing.T) int64 {
	t.Helper()
	return f.count(t, `SELECT COUNT(*) FROM run_artifact`)
}

func (f *reviewFlowFixture) lastResume(t *testing.T) []string {
	t.Helper()
	if len(f.client.resumes) == 0 {
		t.Fatal("resume was not called")
	}
	return f.client.resumes[len(f.client.resumes)-1]
}

func (f *reviewFlowFixture) lastNote(t *testing.T) string {
	t.Helper()
	r := f.lastResume(t)
	for i, a := range r {
		if a == "--note" && i+1 < len(r) {
			return r[i+1]
		}
	}
	t.Fatalf("no --note in %v", r)
	return ""
}

func (f *reviewFlowFixture) openOperations(t *testing.T) int64 {
	t.Helper()
	return f.count(t, `SELECT COUNT(*) FROM operation`)
}

// AC-79〜AC-85・AC-91: options に land が無いレビュー待ちは、検証中へ進めず保留にする。
func TestReceiveReview_WithoutLandHoldsAsLandUnavailable(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	before := f.artifactRows(t)
	f.readOnce(t, intakeOut([]string{"respond", "ready"}, intakeArtsOK, nil))
	q := f.lastHoldQuestion(t)
	t.Run("AC-79 awaiting_human", func(t *testing.T) {
		if got := f.status(t, f.cid); got != StatusAwaitingHuman {
			t.Fatalf("status = %s", got)
		}
	})
	t.Run("AC-80 task is not verifying", func(t *testing.T) {
		if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 0 {
			t.Fatalf("verifying tasks = %d", n)
		}
	})
	t.Run("AC-81 no operation", func(t *testing.T) {
		if n := f.openOperations(t); n != 0 {
			t.Fatalf("operations = %d", n)
		}
	})
	t.Run("AC-82 reason", func(t *testing.T) {
		if !strings.HasPrefix(q, holdReasonLine(holdReasonLandUnavailable)) {
			t.Fatalf("question = %q", q)
		}
	})
	t.Run("AC-83 cannot receive 入れてよい", func(t *testing.T) {
		if !strings.Contains(q, "「入れてよい」の答えを受けられない") {
			t.Fatalf("question = %q", q)
		}
	})
	t.Run("AC-84 flywheel does not land", func(t *testing.T) {
		if !strings.Contains(q, "flywheel は自分では反映しない") {
			t.Fatalf("question = %q", q)
		}
	})
	t.Run("AC-85 upgrade or withdraw", func(t *testing.T) {
		if !strings.Contains(q, "上げてから回答するか、課題を取り下げる") {
			t.Fatalf("question = %q", q)
		}
	})
	t.Run("AC-91 no run_artifact row", func(t *testing.T) {
		if got := f.artifactRows(t); got != before {
			t.Fatalf("run_artifact rows %d -> %d", before, got)
		}
	})
}

// AC-86〜AC-90: land があり、中身の識別が 0 個・2 個のレビュー待ちは保留（content_ref_invalid）にし、問いに見つかった数を書く。
func TestReceiveReview_ContentRefNotExactlyOneHolds(t *testing.T) {
	t.Parallel()
	pr := ContractArtifact{Kind: "pr", Ref: reviewPRURL}
	for name, tc := range map[string]struct {
		arts []ContractArtifact
		n    string
	}{
		"AC-86 AC-87 AC-88 none": {[]ContractArtifact{pr}, "0 個"},
		"AC-89 AC-90 two":        {[]ContractArtifact{pr, {Kind: "commit", Ref: "a1"}, {Kind: "commit", Ref: "b2"}}, "2 個"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntakeFixture(t, reviewConnectorsJSON)
			before := f.artifactRows(t)
			f.readOnce(t, intakeOut(intakeOptsWithLand, tc.arts, nil))
			if got := f.status(t, f.cid); got != StatusAwaitingHuman {
				t.Fatalf("status = %s", got)
			}
			q := f.lastHoldQuestion(t)
			if !strings.HasPrefix(q, holdReasonLine(holdReasonContentRefInvalid)) {
				t.Fatalf("question = %q", q)
			}
			if !strings.Contains(q, tc.n) {
				t.Fatalf("question lacks the count %q: %q", tc.n, q)
			}
			if got := f.artifactRows(t); got != before {
				t.Fatalf("run_artifact rows %d -> %d (AC-91)", before, got)
			}
		})
	}
}

// 中身の識別に数えない commit（role 付き）だけのときも 0 個として保留にする。
func TestReceiveReview_RoledCommitIsNotAContentRef(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: "a1", Role: "integration"}}, nil))
	if q := f.lastHoldQuestion(t); !strings.HasPrefix(q, holdReasonLine(holdReasonContentRefInvalid)) {
		t.Fatalf("question = %q", q)
	}
}

// AC-92: land も中身の識別も無いときは、先に当たる land_unavailable。
func TestReceiveReview_NoLandAndNoContentRefIsLandUnavailable(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.readOnce(t, intakeOut([]string{"respond"}, []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}}, nil))
	if q := f.lastHoldQuestion(t); !strings.HasPrefix(q, holdReasonLine(holdReasonLandUnavailable)) {
		t.Fatalf("question = %q", q)
	}
}

// AC-119・AC-120: 既定ブランチ宛ての PR を 2 つ持つレビュー待ちは保留（content_ref_invalid）で、反映の操作は作らない。問いに数を書く。
func TestReceiveReview_TwoDefaultBranchPRsHold(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.setPR(2, reviewPRURL2, "open", "main")
	f.readOnce(t, reviewWaitOutForAll(reviewPRURL, reviewPRURL2))
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("AC-119 status = %s", got)
	}
	q := f.lastHoldQuestion(t)
	if !strings.HasPrefix(q, holdReasonLine(holdReasonContentRefInvalid)) || !strings.Contains(q, "2 個") {
		t.Fatalf("AC-119 question = %q", q)
	}
	if n := f.openOperations(t); n != 0 {
		t.Fatalf("AC-120 operations = %d", n)
	}
}

// AC-108〜AC-110: land があり、中身の識別がちょうど 1 つで required_facts を満たすレビュー待ちは検証中へ進み、反映の操作が pending で作られる。
func TestReceiveReview_MetReviewWaitVerifiesAndCreatesRelease(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, intakeFactsMet))
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("AC-108 status = %s", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("AC-109 verifying tasks = %d", n)
	}
	if got, want := f.releases(t), []releaseRow{{ref: reviewPRURL, state: "pending"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AC-110 releases = %+v, want %+v", got, want)
	}
}

// AC-107: 宣言に required_facts が無い委譲先は、facts が無くても検証中へ進む。
func TestReceiveReview_WithoutRequiredFactsDeclarationVerifiesWithoutFacts(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}
}

// AC-93〜AC-98・AC-103: required_facts を満たさないレビュー待ちには、同じ契約の run へ respond を返す。
func TestReceiveReview_UnmetFactsRespondsWithTheFixedNote(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"}))
	r := f.lastResume(t)
	t.Run("AC-93 respond", func(t *testing.T) {
		want := []string{"tool", "resume", "ext-1", "--choice", "respond"}
		if len(r) < len(want) || !reflect.DeepEqual(r[:len(want)], want) {
			t.Fatalf("resume = %v", r)
		}
	})
	t.Run("AC-94 in_progress", func(t *testing.T) {
		if got := f.status(t, f.cid); got != StatusInProgress {
			t.Fatalf("status = %s", got)
		}
	})
	t.Run("AC-95 no J5 run", func(t *testing.T) {
		if n := len(f.j5.j5Runs(t)); n != 0 {
			t.Fatalf("J5 runs = %d", n)
		}
	})
	note := f.lastNote(t)
	for name, want := range map[string]string{"AC-96 name": "self_review", "AC-97 got value": "返ってきた値: incomplete", "AC-98 accepted": "受け入れる値: complete"} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(note, want) {
				t.Fatalf("note = %q, want %q", note, want)
			}
		})
	}
	t.Run("AC-103 no operation", func(t *testing.T) {
		if n := f.openOperations(t); n != 0 {
			t.Fatalf("operations = %d", n)
		}
	})
}

// AC-99・AC-100: 出力に facts が無いレビュー待ちは満たしていないものとして respond を返し、返ってきた値は「無い」。
func TestReceiveReview_NoFactsRespondsWithNone(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("AC-99 resumes = %v", f.client.resumes)
	}
	if note := f.lastNote(t); !strings.Contains(note, "self_review（返ってきた値: 無い") {
		t.Fatalf("AC-100 note = %q", note)
	}
}

// AC-101・AC-102: facts はあるが宣言の名前が無いレビュー待ちは満たしていない。満たしていない名前が 2 つなら、どちらも添え書きにある。
func TestReceiveReview_MissingNamesInFactsRespondsWithEachName(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"other": "x"}))
	if len(f.client.resumes) != 1 {
		t.Fatalf("AC-101 resumes = %v", f.client.resumes)
	}
	note := f.lastNote(t)
	if !strings.Contains(note, "self_review") || !strings.Contains(note, "gate") {
		t.Fatalf("AC-102 note = %q", note)
	}
}

// AC-104・AC-105: 満たさないレビュー待ちが 2 回続くと、2 回目は resume を呼ばず、保留（facts_unmet）にする。
func TestReceiveReview_TwoUnmetReadsInARowHold(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	unmet := intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"})
	f.readOnce(t, unmet)
	f.readOnce(t, unmet)
	if len(f.client.resumes) != 1 {
		t.Fatalf("AC-104 resumes = %v, want only the first", f.client.resumes)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("AC-104 status = %s", got)
	}
	q := f.lastHoldQuestion(t)
	if !strings.HasPrefix(q, holdReasonLine(holdReasonFactsUnmet)) {
		t.Fatalf("AC-105 question = %q", q)
	}
	if !strings.Contains(q, "self_review") {
		t.Fatalf("AC-118 question lacks the name: %q", q)
	}
	if !strings.Contains(q, "回答すると、回答の文を添えて、もう一度委譲先へ差し戻す") {
		t.Fatalf("question lacks the answer notice: %q", q)
	}
}

// AC-106: 満たさない → 満たす → 満たさない の順なら、3 つ目では respond を返す（数え直す）。
func TestReceiveReview_MetReadResetsTheUnmetCount(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	unmet := intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"})
	f.readOnce(t, unmet)
	// 満たすレビュー待ちは検証中へ進むので、課題を着手中へ戻して続ける。
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, intakeFactsMet))
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}
	f.setStatus(t, StatusInProgress)
	f.readOnce(t, unmet)
	if len(f.client.resumes) != 2 {
		t.Fatalf("AC-106 resumes = %d, want 2", len(f.client.resumes))
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// AC-73: facts.self_review が complete のレビュー待ちに respond を返した後の、facts の無いレビュー待ちは満たしていないものとして扱う。
func TestReceiveReview_FactsAreNotCarriedOverFromThePreviousRead(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, strings.Replace(reviewConnectorsJSON, `"contract_version": 1,`, `"contract_version": 1, "required_facts": {"self_review": ["complete"]},`, 1))
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "complete"}))
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}
	// 差し戻し（respond）で着手中へ戻った後に、facts の無いレビュー待ちを読む。
	f.setStatus(t, StatusInProgress)
	before := len(f.client.resumes)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if len(f.client.resumes) != before+1 {
		t.Fatalf("resumes %d -> %d, want a respond", before, len(f.client.resumes))
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// AC-111〜AC-113: land_unavailable・content_ref_invalid の保留に回答した後、同じ理由に当たれば、もう一度同じ理由で保留にする。
// 回答の文を載せた resume は呼ばない。
func TestReceiveReview_AnsweredLandUnavailableAndContentRefInvalidHoldAgain(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		out    ContractLaunchOutput
		reason holdReason
	}{
		"AC-111 AC-112 land": {intakeOut([]string{"respond"}, intakeArtsOK, nil), holdReasonLandUnavailable},
		"AC-113 content":     {intakeOut(intakeOptsWithLand, []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}}, nil), holdReasonContentRefInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntakeFixture(t, reviewConnectorsJSON)
			f.readOnce(t, tc.out)
			f.answerLatestHold(t, f.cid, "直した")
			if got := f.status(t, f.cid); got != StatusInProgress {
				t.Fatalf("status after the answer = %s", got)
			}
			f.readOnce(t, tc.out)
			if got := f.status(t, f.cid); got != StatusAwaitingHuman {
				t.Fatalf("status = %s", got)
			}
			if q := f.lastHoldQuestion(t); !strings.HasPrefix(q, holdReasonLine(tc.reason)) {
				t.Fatalf("question = %q", q)
			}
			if len(f.client.resumes) != 0 {
				t.Fatalf("resumes = %v, want none", f.client.resumes)
			}
		})
	}
}

// AC-114〜AC-117: facts_unmet の保留に回答すると回数は 0 になり、次も満たさなければ回答の文を添えて respond を返す。その次も満たさなければ保留。
func TestReceiveReview_AnsweredFactsUnmetRespondsWithTheOwnerAnswerThenHoldsAgain(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	unmet := intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"})
	f.readOnce(t, unmet)
	f.readOnce(t, unmet)
	if n := f.count(t, `SELECT facts_unmet_count FROM contract_run`); n != 2 {
		t.Fatalf("count before the answer = %d", n)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.cid, ExpectedVersion: f.version(t, f.cid), Answer: "OWNER-ANSWER-TEXT"},
		verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT facts_unmet_count FROM contract_run`); n != 0 {
		t.Fatalf("AC-114 facts_unmet_count = %d, want 0", n)
	}
	before := len(f.client.resumes)
	f.readOnce(t, unmet)
	if len(f.client.resumes) != before+1 {
		t.Fatalf("AC-115 resumes %d -> %d", before, len(f.client.resumes))
	}
	if r := f.lastResume(t); r[4] != "respond" {
		t.Fatalf("AC-115 resume = %v", r)
	}
	if note := f.lastNote(t); !strings.Contains(note, "OWNER-ANSWER-TEXT") {
		t.Fatalf("AC-116 note = %q", note)
	}
	f.readOnce(t, unmet)
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("AC-117 status = %s", got)
	}
	if q := f.lastHoldQuestion(t); !strings.HasPrefix(q, holdReasonLine(holdReasonFactsUnmet)) {
		t.Fatalf("AC-117 question = %q", q)
	}
}

// 回答の文は、委譲先へ返したあと、後の差し戻しの添え書きに繰り返さない。
func TestReceiveReview_OwnerAnswerIsNotRepeatedInLaterNotes(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	unmet := intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"})
	f.readOnce(t, unmet)
	f.readOnce(t, unmet)
	f.answerLatestHold(t, f.cid, "OWNER-ANSWER-TEXT")
	f.readOnce(t, unmet) // 回答の文つきの respond
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, intakeFactsMet))
	f.setStatus(t, StatusInProgress)
	f.readOnce(t, unmet)
	if note := f.lastNote(t); strings.Contains(note, "OWNER-ANSWER-TEXT") {
		t.Fatalf("note repeats the old answer: %q", note)
	}
}

// AC-121・AC-122: 検証するタスクの契約の実行に facts の記録があるとき、J5 の入力に区画「委譲先が値で返したこと」があり、名前と値が出る。
func TestJ5Input_HasTheDelegateFactsSection(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, intakeFactsMet))
	if _, err := f.j5.verify(t, &f.cid); err != nil {
		t.Fatal(err)
	}
	in := f.j5.stdin(t)
	if !strings.Contains(in, "## "+labelDelegateFactsSection+"\n") {
		t.Fatalf("AC-121 no section in:\n%s", in)
	}
	for _, want := range []string{"- self_review: complete", "- gate: pass"} {
		if !strings.Contains(in, want) {
			t.Fatalf("AC-122 lacks %q in:\n%s", want, in)
		}
	}
}

// AC-123: facts の記録が無いとき、J5 の入力に区画「委譲先が値で返したこと」は無い。
func TestJ5Input_HasNoDelegateFactsSectionWithoutFacts(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if _, err := f.j5.verify(t, &f.cid); err != nil {
		t.Fatal(err)
	}
	if in := f.j5.stdin(t); strings.Contains(in, labelDelegateFactsSection) {
		t.Fatalf("the section is present without facts:\n%s", in)
	}
}

// 反映先宛ての PR が 2 つで保留にした読みは、満たさなかった回数に数えない（回答の後の最初の満たさない読みで respond を返す）。
func TestReceiveReview_TwoPRsHoldDoesNotCountTowardsFactsUnmet(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.setPR(2, reviewPRURL2, "open", "main")
	two := reviewWaitOutForAll(reviewPRURL, reviewPRURL2)
	two.Status.Facts = map[string]string{"self_review": "incomplete", "gate": "pass"}
	f.readOnce(t, two)
	if n := f.count(t, `SELECT facts_unmet_count FROM contract_run`); n != 0 {
		t.Fatalf("facts_unmet_count = %d, want 0", n)
	}
}

// 満たすレビュー待ちが resume の応答で届いても、満たさなかった回数は 0 に戻る。
func TestReceiveReview_MetReadInAResumeResponseResetsTheCount(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.client.resumeOut = intakeOut(intakeOptsWithLand, intakeArtsOK, intakeFactsMet)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"}))
	if n := f.count(t, `SELECT facts_unmet_count FROM contract_run`); n != 0 {
		t.Fatalf("facts_unmet_count = %d, want 0", n)
	}
}
