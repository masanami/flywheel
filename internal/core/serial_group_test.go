package core

import (
	"reflect"
	"testing"
)

// このファイルは直列化グループの純粋な規則（AC-301〜317・329〜333 の規則の部分）を検証する。

func candN(n int, issue, rank int) planCandidate {
	return planCandidate{ID: formatChallengeID(int64(n)), Issue: issue, Rank: rank, Seq: int64(n)}
}

func shared(path string) PredictedSharedFile { return PredictedSharedFile{Path: path} }

func pairOf(a, b int, files ...PredictedSharedFile) PredictedPair {
	return PredictedPair{Issues: [2]int{a, b}, Status: predictPairPredicted, SharedFiles: files}
}

func groupIDs(gs []plannedGroup) [][]string {
	var out [][]string
	for _, g := range gs {
		var ids []string
		for _, r := range g.Running {
			ids = append(ids, r.ID)
		}
		for _, c := range g.Candidates {
			ids = append(ids, c.ID)
		}
		out = append(out, ids)
	}
	return out
}

func TestBuildRepoGroups_SharedFilesAreConnectedComponents(t *testing.T) {
	cands := []planCandidate{candN(1, 11, 1), candN(2, 12, 1), candN(3, 13, 1), candN(4, 14, 1)}
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{pairOf(11, 12, shared("a.go")), pairOf(12, 13, shared("b.go")), pairOf(13, 14)}}
	gs := buildRepoGroups("r", nil, cands, repoPrediction{Output: out})
	if got, want := groupIDs(gs), [][]string{{"C-1", "C-2", "C-3"}, {"C-4"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if got := sortedReasons(gs[0].Reasons); !reflect.DeepEqual(got, []SerialGroupReason{SerialReasonSharedFiles}) {
		t.Errorf("reasons = %v", got)
	}
	if got := sortedReasons(gs[1].Reasons); len(got) != 0 {
		t.Errorf("a group made only by the prediction result has reasons %v, want none", got)
	}
}

func TestBuildRepoGroups_MergeFriendlyAndIgnoredDoNotJoin(t *testing.T) {
	for _, mark := range []string{"merge_friendly", "ignored"} {
		f := shared("package-lock.json")
		if mark == "merge_friendly" {
			f.MergeFriendly = true
		} else {
			f.Ignored = true
		}
		out := &ConflictPredictionOutput{Pairs: []PredictedPair{pairOf(11, 12, f)}}
		gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1), candN(2, 12, 1)}, repoPrediction{Output: out})
		if len(gs) != 2 {
			t.Errorf("%s: groups = %v, want 2 separate groups", mark, groupIDs(gs))
		}
	}
	// 印の付かない共有ファイルが 1 つでもあれば結ぶ。
	ignored := shared("x")
	ignored.Ignored = true
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{pairOf(11, 12, ignored, shared("real.go"))}}
	if gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1), candN(2, 12, 1)}, repoPrediction{Output: out}); len(gs) != 1 {
		t.Errorf("groups = %v, want 1", groupIDs(gs))
	}
}

func TestBuildRepoGroups_UnknownPairAndDependencyJoin(t *testing.T) {
	first := 12
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{
		{Issues: [2]int{11, 12}, Status: predictPairUnknown},
		{Issues: [2]int{13, 14}, Status: predictPairPredicted, DependencyFirst: &first},
	}}
	gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1), candN(2, 12, 1), candN(3, 13, 1), candN(4, 14, 1)}, repoPrediction{Output: out})
	if len(gs) != 2 {
		t.Fatalf("groups = %v, want 2", groupIDs(gs))
	}
	if got := sortedReasons(gs[0].Reasons); !reflect.DeepEqual(got, []SerialGroupReason{SerialReasonUnknownPair}) {
		t.Errorf("group 1 reasons = %v", got)
	}
	if got := sortedReasons(gs[1].Reasons); !reflect.DeepEqual(got, []SerialGroupReason{SerialReasonDependency}) {
		t.Errorf("group 2 reasons = %v", got)
	}
}

func TestBuildRepoGroups_ReasonsAreInDefinitionOrderWithoutDuplicates(t *testing.T) {
	first := 12
	p := pairOf(11, 12, shared("a.go"))
	p.DependencyFirst = &first
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{p, pairOf(11, 12, shared("b.go"))}}
	gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1), candN(2, 12, 1)}, repoPrediction{Output: out})
	if got := sortedReasons(gs[0].Reasons); !reflect.DeepEqual(got, []SerialGroupReason{SerialReasonSharedFiles, SerialReasonDependency}) {
		t.Errorf("reasons = %v, want [shared_files dependency]", got)
	}
}

func TestBuildRepoGroups_DependencyEdgeOrdersBeforePriority(t *testing.T) {
	first := 12 // C-2（優先度が低い）を先に入れる
	p := pairOf(11, 12)
	p.DependencyFirst = &first
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{p}}
	gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 0), candN(2, 12, 2)}, repoPrediction{Output: out})
	if got, want := groupIDs(gs), [][]string{{"C-2", "C-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestBuildRepoGroups_NoEdgesOrdersByPriorityThenID(t *testing.T) {
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{pairOf(11, 12, shared("a")), pairOf(12, 13, shared("a"))}}
	gs := buildRepoGroups("r", nil, []planCandidate{candN(3, 13, 0), candN(1, 11, 1), candN(2, 12, 0)}, repoPrediction{Output: out})
	if got, want := groupIDs(gs), [][]string{{"C-2", "C-3", "C-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestBuildRepoGroups_CyclicEdgesAreAllIgnored(t *testing.T) {
	// 11 → 12 と 12 → 11 の循環。辺をすべて無視し、優先度・ID の昇順（C-2 は優先度が高い）にする。
	one, two := 11, 12
	p1, p2 := pairOf(11, 12), pairOf(11, 12)
	p1.DependencyFirst = &one
	p2.DependencyFirst = &two
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{p1, p2}}
	gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1), candN(2, 12, 0)}, repoPrediction{Output: out})
	if got, want := groupIDs(gs), [][]string{{"C-2", "C-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v (cycle edges ignored)", got, want)
	}
}

func TestBuildRepoGroups_FailClosedPutsEverythingInOneGroup(t *testing.T) {
	cands := []planCandidate{candN(2, 12, 1), candN(1, 11, 1)}
	running := []planRunning{{ID: "C-9", Issue: 19, Seq: 9}}
	gs := buildRepoGroups("r", running, cands, repoPrediction{FailClosed: []SerialGroupReason{SerialReasonPredictionFailed}, HeadSHA: "abc"})
	if got, want := groupIDs(gs), [][]string{{"C-9", "C-1", "C-2"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	want := []SerialGroupReason{SerialReasonPredictionFailed, SerialReasonRunningRun}
	if got := sortedReasons(gs[0].Reasons); !reflect.DeepEqual(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
	if pub := gs[0].public(); pub.PredictionHeadSHA == nil || *pub.PredictionHeadSHA != "abc" {
		t.Errorf("head sha = %v, want abc", pub.PredictionHeadSHA)
	}
}

func TestBuildRepoGroups_RunningJoinsTheComponent(t *testing.T) {
	out := &ConflictPredictionOutput{Pairs: []PredictedPair{pairOf(19, 11, shared("a")), pairOf(11, 12), pairOf(19, 12)}}
	running := []planRunning{{ID: "C-9", Issue: 19, Seq: 9}}
	gs := buildRepoGroups("r", running, []planCandidate{candN(1, 11, 1), candN(2, 12, 1)}, repoPrediction{Output: out})
	if got, want := groupIDs(gs), [][]string{{"C-9", "C-1"}, {"C-2"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if got := sortedReasons(gs[0].Reasons); !reflect.DeepEqual(got, []SerialGroupReason{SerialReasonSharedFiles, SerialReasonRunningRun}) {
		t.Errorf("reasons = %v", got)
	}
}

func TestBuildRepoGroups_NoPredictionMakesOneGroupPerCandidate(t *testing.T) {
	gs := buildRepoGroups("r", nil, []planCandidate{candN(1, 11, 1)}, repoPrediction{})
	if len(gs) != 1 || len(sortedReasons(gs[0].Reasons)) != 0 {
		t.Fatalf("groups = %+v", gs)
	}
	if buildRepoGroups("r", nil, nil, repoPrediction{}) != nil {
		t.Error("no candidates must give no groups")
	}
}
