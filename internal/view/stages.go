package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは段階の欄（docs/features/m4-ui-server.md §IF / API「段階の欄の形（S4）」）の応答の型。
// 配列の欄は null か要素 1 個以上で、`[]` を返さない。

// StageOption は論点の選択肢。
type StageOption struct {
	Example *string `json:"example"`
	Label   string  `json:"label"`
}

// StageIssue は論点 1 つ。Recommended は Options の 1 から数えた位置。
type StageIssue struct {
	Options     []StageOption `json:"options"`
	Question    string        `json:"question"`
	Recommended int           `json:"recommended"`
}

// StageExpected は予想の例 1 つ。
type StageExpected struct {
	After  string `json:"after"`
	Before string `json:"before"`
	Kind   string `json:"kind"`
	Scene  string `json:"scene"`
}

// StageActual は実物の例 1 つ。
type StageActual struct {
	After string `json:"after"`
	Scene string `json:"scene"`
}

// Stages は `show --json`・① の段階の欄。IssuesAnswer は論点を出した保留の回答（無ければ null）。
type Stages struct {
	Actual       []StageActual   `json:"actual" ts:"nullable"`
	Expected     []StageExpected `json:"expected" ts:"nullable"`
	Figure       *string         `json:"figure"`
	Issues       []StageIssue    `json:"issues" ts:"nullable"`
	IssuesAnswer *string         `json:"issues_answer"`
	NonGoals     []string        `json:"non_goals" ts:"nullable"`
}

// StagePair は予想の例と同じ位置の実物の例の対。足りない側は null。
type StagePair struct {
	Actual   *StageActual   `json:"actual"`
	Expected *StageExpected `json:"expected"`
}

// nilIfEmpty は要素 0 個の配列を nil（JSON の null）にする。
func nilIfEmpty[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}

// FromStages は課題の段階の欄と論点の回答を Stages へ変換する。
func FromStages(f core.StageFields, issuesAnswer *string) Stages {
	out := Stages{Figure: f.Figure, IssuesAnswer: issuesAnswer}
	for _, is := range f.Issues {
		opts := make([]StageOption, 0, len(is.Options))
		for _, o := range is.Options {
			opts = append(opts, StageOption{Example: o.Example, Label: o.Label})
		}
		out.Issues = append(out.Issues, StageIssue{Options: opts, Question: is.Question, Recommended: is.Recommended})
	}
	for _, e := range f.Expected {
		out.Expected = append(out.Expected, fromExpected(e))
	}
	for _, a := range f.Actual {
		out.Actual = append(out.Actual, fromActual(a))
	}
	out.NonGoals = nilIfEmpty(append([]string(nil), f.NonGoals...))
	return out
}

func fromExpected(e core.StageExpected) StageExpected {
	return StageExpected{After: e.After, Before: e.Before, Kind: string(e.Kind), Scene: e.Scene}
}

func fromActual(a core.StageActual) StageActual {
	return StageActual{After: a.After, Scene: a.Scene}
}

// FromPairs は core.Pairs の結果を変換する（空でも `[]`）。
func FromPairs(ps []core.StagePair) []StagePair {
	out := make([]StagePair, 0, len(ps))
	for _, p := range ps {
		var sp StagePair
		if p.Expected != nil {
			e := fromExpected(*p.Expected)
			sp.Expected = &e
		}
		if p.Actual != nil {
			a := fromActual(*p.Actual)
			sp.Actual = &a
		}
		out = append(out, sp)
	}
	return out
}
