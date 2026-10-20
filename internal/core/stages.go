package core

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// このファイルは課題カードの「一言で」「位置づけ」と段階の欄（論点・予想・実物・やらないこと・図。
// 仕様 §IF / API「段階の欄の形（S4）」）の型・形の検査・対の組み立て・書き込みの口を持つ。
// 判断点の出力と edit は、同じ検査（Normalize*・Parse*JSON）を通す。

// 長さ・個数の上限（文字は Unicode のコードポイント、図だけバイト）。
const (
	MaxHeadlineRunes = 100
	MaxContextRunes  = 1000
	MaxStageRunes    = 2000
	MaxFigureBytes   = 16384

	MinIssues   = 3
	MaxIssues   = 5
	MinOptions  = 2
	MaxOptions  = 5
	MinExpected = 1
	MaxExpected = 3
	MaxActual   = 3
	MaxNonGoals = 10
)

// 固定の文・印（仕様 §アーキテクチャ決定）。テストは定数で確かめる。
const (
	// PairActualMissingMark は予想の例に対応する実物が無い位置の印。
	PairActualMissingMark = "実物なし"
	// IssuesQuestionPreamble は論点の保留の問いの冒頭の文。
	IssuesQuestionPreamble = "論点に答えてください。答えは 1 回の回答にまとめてください"
	// ExpectedEmptyHoldPreamble はサイズ M・L の計画で予想を書けなかったときの保留の問いの冒頭の文。
	ExpectedEmptyHoldPreamble = "完了したら見えるもの（予想）を書けませんでした。"
	// ActualMismatchHoldPreamble は実物を予想の例ごとに確かめられなかったときの保留の問いの冒頭の文。
	ActualMismatchHoldPreamble = "完了したら見えるもの（予想）の例ごとに実物を確かめられませんでした。"
)

// ExpectedKind は予想の例の種別。
type ExpectedKind string

// ExpectedKind の 3 値。
const (
	ExpectedKindUI       ExpectedKind = "ui"
	ExpectedKindCLI      ExpectedKind = "cli"
	ExpectedKindBehavior ExpectedKind = "behavior"
)

// StageOption は論点の選択肢。
type StageOption struct {
	Label   string  `json:"label"`
	Example *string `json:"example"`
}

// StageIssue は論点 1 つ。Recommended は Options の 1 から数えた位置。
type StageIssue struct {
	Question    string        `json:"question"`
	Options     []StageOption `json:"options"`
	Recommended int           `json:"recommended"`
}

// StageExpected は予想（完了したら見えるもの）の例 1 つ。
type StageExpected struct {
	Kind   ExpectedKind `json:"kind"`
	Scene  string       `json:"scene"`
	Before string       `json:"before"`
	After  string       `json:"after"`
}

// StageActual は実物の例 1 つ（予想の例と同じ順に書く）。
type StageActual struct {
	Scene string `json:"scene"`
	After string `json:"after"`
}

// StageFields は challenge の「一言で」「位置づけ」と段階の欄の列。NULL は nil
// （要素 0 個の配列も nil として持つ。空の配列を持たない）。
type StageFields struct {
	Headline *string
	Context  *string
	Issues   []StageIssue
	// IssuesHoldID は論点を出した保留（hold.id）への参照。回答の正本は保留の側。
	IssuesHoldID *int64
	Expected     []StageExpected
	Actual       []StageActual
	NonGoals     []string
	Figure       *string
}

// errStageShape は欄の形に合わないことを表す（errors.Is(err, ErrValidation) が真）。
func errStageShape(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

func checkText(name, s string, maxRunes int) error {
	if strings.TrimSpace(s) == "" {
		return errStageShape("%s must not be blank", name)
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return errStageShape("%s exceeds %d characters", name, maxRunes)
	}
	return nil
}

// CheckHeadline は一言でを検査し、空白だけ・上限超過を ErrValidation で拒む
// （判断点の出力の検査。edit の「空文字は NULL」は NormalizeEditText が先に扱う）。
func CheckHeadline(s string) error { return checkText("headline", s, MaxHeadlineRunes) }

// CheckContext は CheckHeadline と同じ規則で位置づけを検査する。
func CheckContext(s string) error { return checkText("context", s, MaxContextRunes) }

// CheckFigure は図（Mermaid のテキスト）を検査する。上限はバイト。
func CheckFigure(s string) error {
	if strings.TrimSpace(s) == "" {
		return errStageShape("figure must not be blank")
	}
	if len(s) > MaxFigureBytes {
		return errStageShape("figure exceeds %d bytes", MaxFigureBytes)
	}
	return nil
}

// NormalizeIssues は論点を検査し、要素 0 個なら nil を返す。
func NormalizeIssues(in []StageIssue) ([]StageIssue, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) < MinIssues || len(in) > MaxIssues {
		return nil, errStageShape("issues must have %d-%d items", MinIssues, MaxIssues)
	}
	for _, is := range in {
		if err := checkText("issues.question", is.Question, MaxStageRunes); err != nil {
			return nil, err
		}
		if len(is.Options) < MinOptions || len(is.Options) > MaxOptions {
			return nil, errStageShape("issues.options must have %d-%d items", MinOptions, MaxOptions)
		}
		for _, o := range is.Options {
			if err := checkText("issues.options.label", o.Label, MaxStageRunes); err != nil {
				return nil, err
			}
			if o.Example != nil {
				if err := checkText("issues.options.example", *o.Example, MaxStageRunes); err != nil {
					return nil, err
				}
			}
		}
		if is.Recommended < 1 || is.Recommended > len(is.Options) {
			return nil, errStageShape("issues.recommended must be within 1-%d", len(is.Options))
		}
	}
	return in, nil
}

// NormalizeExpected は予想を検査し、要素 0 個なら nil を返す。
func NormalizeExpected(in []StageExpected) ([]StageExpected, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) < MinExpected || len(in) > MaxExpected {
		return nil, errStageShape("expected must have %d-%d items", MinExpected, MaxExpected)
	}
	for _, e := range in {
		switch e.Kind {
		case ExpectedKindUI, ExpectedKindCLI, ExpectedKindBehavior:
		default:
			return nil, errStageShape("expected.kind must be ui, cli or behavior")
		}
		for name, v := range map[string]string{"expected.scene": e.Scene, "expected.before": e.Before, "expected.after": e.After} {
			if err := checkText(name, v, MaxStageRunes); err != nil {
				return nil, err
			}
		}
	}
	return in, nil
}

// NormalizeActual は実物を検査し、要素 0 個なら nil を返す。
func NormalizeActual(in []StageActual) ([]StageActual, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxActual {
		return nil, errStageShape("actual must have at most %d items", MaxActual)
	}
	for _, a := range in {
		if err := checkText("actual.scene", a.Scene, MaxStageRunes); err != nil {
			return nil, err
		}
		if err := checkText("actual.after", a.After, MaxStageRunes); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// NormalizeNonGoals はやらないことを検査し、要素 0 個なら nil を返す。
func NormalizeNonGoals(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxNonGoals {
		return nil, errStageShape("non_goals must have at most %d items", MaxNonGoals)
	}
	for _, s := range in {
		if err := checkText("non_goals", s, MaxStageRunes); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// decodeStageJSON は text を JSON の配列として厳密に（未知のキー・末尾の余りを拒む）解釈する。
// null や配列以外は拒む。
func decodeStageJSON[T any](text string) ([]T, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") {
		return nil, errStageShape("value must be a JSON array")
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	out := []T{}
	if err := dec.Decode(&out); err != nil {
		return nil, errStageShape("invalid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errStageShape("unexpected data after JSON value")
	}
	return out, nil
}

// ParseIssuesJSON は論点の JSON を解釈して検査する（Parse*JSON は edit のフラグと
// 同じ入口）。要素 0 個の配列は nil。
func ParseIssuesJSON(text string) ([]StageIssue, error) {
	v, err := decodeStageJSON[StageIssue](text)
	if err != nil {
		return nil, err
	}
	return NormalizeIssues(v)
}

// ParseExpectedJSON は予想の JSON を解釈して検査する。
func ParseExpectedJSON(text string) ([]StageExpected, error) {
	v, err := decodeStageJSON[StageExpected](text)
	if err != nil {
		return nil, err
	}
	return NormalizeExpected(v)
}

// ParseActualJSON は実物の JSON を解釈して検査する。
func ParseActualJSON(text string) ([]StageActual, error) {
	v, err := decodeStageJSON[StageActual](text)
	if err != nil {
		return nil, err
	}
	return NormalizeActual(v)
}

// ParseNonGoalsJSON はやらないことの JSON を解釈して検査する。
func ParseNonGoalsJSON(text string) ([]string, error) {
	v, err := decodeStageJSON[string](text)
	if err != nil {
		return nil, err
	}
	return NormalizeNonGoals(v)
}

// NormalizeEditText は edit の文字列の欄（headline・context・figure）を検査する。
// 空文字は NULL（nil）、空白だけ・上限超過は ErrValidation。check は CheckHeadline 等。
func NormalizeEditText(s string, check func(string) error) (*string, error) {
	if s == "" {
		return nil, nil
	}
	if err := check(s); err != nil {
		return nil, err
	}
	return &s, nil
}

// StagePair は予想の例と同じ位置の実物の例の対。足りない側は nil。
type StagePair struct {
	Expected *StageExpected
	Actual   *StageActual
}

// Pairs は予想と実物を位置で対にする（完了の承認の要約の唯一の組み立て）。多い方の個数だけ
// 並べ、足りない側は nil。どちらも空なら空（nil でないスライス）。
func Pairs(expected []StageExpected, actual []StageActual) []StagePair {
	n := max(len(expected), len(actual))
	out := make([]StagePair, n)
	for i := range out {
		if i < len(expected) {
			e := expected[i]
			out[i].Expected = &e
		}
		if i < len(actual) {
			a := actual[i]
			out[i].Actual = &a
		}
	}
	return out
}

// StageOpt は StagePatch の 1 欄。Set が false なら変えない。V は新しい値（nil が NULL）。
type StageOpt[T any] struct {
	Set bool
	V   T
}

// StageSet は新しい値 v を置き換える指定を作る。
func StageSet[T any](v T) StageOpt[T] { return StageOpt[T]{Set: true, V: v} }

// StagePatch は段階の欄の書き換えの指定。値は呼び出し元が Normalize* 済みであること。
// Issues を変え IssuesHoldID を指定しなければ、IssuesHoldID は NULL になる。
type StagePatch struct {
	Headline     StageOpt[*string]
	Context      StageOpt[*string]
	Issues       StageOpt[[]StageIssue]
	IssuesHoldID StageOpt[*int64]
	Expected     StageOpt[[]StageExpected]
	Actual       StageOpt[[]StageActual]
	NonGoals     StageOpt[[]string]
	Figure       StageOpt[*string]
}

// applyPatch は f へ p を当てた新しい値を返し、変わった列だけを列の名前をキーに
// before・after へ載せる（JSON の列は解釈した値で載せる）。
func (f StageFields) applyPatch(p StagePatch, before, after map[string]any) StageFields {
	next := f
	set := func(col string, changed bool, old, nw any) {
		if changed {
			before[col] = old
			after[col] = nw
		}
	}
	if p.Headline.Set && !reflect.DeepEqual(f.Headline, p.Headline.V) {
		set("headline", true, f.Headline, p.Headline.V)
		next.Headline = p.Headline.V
	}
	if p.Context.Set && !reflect.DeepEqual(f.Context, p.Context.V) {
		set("context", true, f.Context, p.Context.V)
		next.Context = p.Context.V
	}
	if p.Issues.Set && !reflect.DeepEqual(f.Issues, p.Issues.V) {
		set("issues", true, f.Issues, p.Issues.V)
		next.Issues = p.Issues.V
		if !p.IssuesHoldID.Set {
			next.IssuesHoldID = nil
		}
	}
	if p.IssuesHoldID.Set {
		next.IssuesHoldID = p.IssuesHoldID.V
	}
	if !reflect.DeepEqual(f.IssuesHoldID, next.IssuesHoldID) {
		set("issues_hold_id", true, f.IssuesHoldID, next.IssuesHoldID)
	}
	if p.Expected.Set && !reflect.DeepEqual(f.Expected, p.Expected.V) {
		set("expected", true, f.Expected, p.Expected.V)
		next.Expected = p.Expected.V
	}
	if p.Actual.Set && !reflect.DeepEqual(f.Actual, p.Actual.V) {
		set("actual", true, f.Actual, p.Actual.V)
		next.Actual = p.Actual.V
	}
	if p.NonGoals.Set && !reflect.DeepEqual(f.NonGoals, p.NonGoals.V) {
		set("non_goals", true, f.NonGoals, p.NonGoals.V)
		next.NonGoals = p.NonGoals.V
	}
	if p.Figure.Set && !reflect.DeepEqual(f.Figure, p.Figure.V) {
		set("figure", true, f.Figure, p.Figure.V)
		next.Figure = p.Figure.V
	}
	return next
}

// stageUpdateSQL は challenge の段階の欄の列を書く UPDATE の SET 句の断片と、その値。
const stageUpdateSQL = `headline = ?, context = ?, issues = ?, issues_hold_id = ?, expected = ?, actual = ?, non_goals = ?, figure = ?`

func jsonColumn[T any](v []T) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func strPtrColumn(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// args は stageUpdateSQL の ? に対応する値を返す。
func (f StageFields) args() ([]any, error) {
	issues, err := jsonColumn(f.Issues)
	if err != nil {
		return nil, err
	}
	expected, err := jsonColumn(f.Expected)
	if err != nil {
		return nil, err
	}
	actual, err := jsonColumn(f.Actual)
	if err != nil {
		return nil, err
	}
	nonGoals, err := jsonColumn(f.NonGoals)
	if err != nil {
		return nil, err
	}
	return []any{strPtrColumn(f.Headline), strPtrColumn(f.Context), issues, int64PtrColumn(f.IssuesHoldID), expected, actual, nonGoals, strPtrColumn(f.Figure)}, nil
}

func decodeJSONColumn[T any](col sql.NullString, name string) ([]T, error) {
	if !col.Valid {
		return nil, nil
	}
	var out []T
	if err := json.NewDecoder(bytes.NewReader([]byte(col.String))).Decode(&out); err != nil {
		return nil, fmt.Errorf("core: decode challenge.%s: %w", name, err)
	}
	return out, nil
}

// stageScan は challenge の段階の欄の列を Scan するための受け皿。
type stageScan struct {
	headline, context, issues, expected, actual, nonGoals, figure sql.NullString
	issuesHoldID                                                  sql.NullInt64
}

func (s *stageScan) dests() []any {
	return []any{&s.headline, &s.context, &s.issues, &s.issuesHoldID, &s.expected, &s.actual, &s.nonGoals, &s.figure}
}

func (s *stageScan) fields() (StageFields, error) {
	var f StageFields
	var err error
	f.Headline = nullStr(s.headline)
	f.Context = nullStr(s.context)
	f.Figure = nullStr(s.figure)
	if s.issuesHoldID.Valid {
		v := s.issuesHoldID.Int64
		f.IssuesHoldID = &v
	}
	if f.Issues, err = decodeJSONColumn[StageIssue](s.issues, "issues"); err != nil {
		return f, err
	}
	if f.Expected, err = decodeJSONColumn[StageExpected](s.expected, "expected"); err != nil {
		return f, err
	}
	if f.Actual, err = decodeJSONColumn[StageActual](s.actual, "actual"); err != nil {
		return f, err
	}
	if f.NonGoals, err = decodeJSONColumn[string](s.nonGoals, "non_goals"); err != nil {
		return f, err
	}
	return f, nil
}

// patchStages は遷移の写像（apply）が段階の欄を書くための口。当てた結果は runTransition が
// 課題の遷移・作業ログと同じトランザクションで書き、戻り値の Challenge にも写す。
func (tc *transitionCtx) patchStages(p StagePatch) {
	// 複数回呼ばれても before はトランザクション開始時の値のまま残す（最初に載せた値を上書きしない）。
	b, a := map[string]any{}, map[string]any{}
	tc.stage = tc.stage.applyPatch(p, b, a)
	for k, v := range b {
		if _, seen := tc.before[k]; !seen {
			tc.before[k] = v
		}
	}
	for k, v := range a {
		tc.after[k] = v
	}
}
