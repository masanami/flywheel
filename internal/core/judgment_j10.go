package core

// このファイルは区切りを分ける判断点（J10）固有の出力の型と検査・入力の組み立て・対象の選び方・呼び出しを持つ
// （docs/features/request-to-plan-s3.md）。呼び出しの入口は分類の段（ClassifyAutoJ1）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// --- 出力の型と検査 ---

type j10Slice struct {
	Title     *string `json:"title"`
	Headline  *string `json:"headline"`
	Context   *string `json:"context"`
	Summary   *string `json:"summary"`
	Scope     *string `json:"scope"`
	Repo      *string `json:"repo"`
	DependsOn []int   `json:"depends_on"`
}

type j10Evidence struct {
	Path string `json:"path"`
	Note string `json:"note"`
}

type j10Output struct {
	Verdict  J10Verdict    `json:"verdict"`
	Reason   string        `json:"reason"`
	Slices   []j10Slice    `json:"slices"`
	Evidence []j10Evidence `json:"evidence"`
}

// j10OutputSchema は区切りを分ける判断点の `--json-schema` に渡すスキーマを返す。
func j10OutputSchema() []byte {
	// 各項目の description は、指示文（prompts/j10.md）の項目名と同じ言葉にする。
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	slice := object(map[string]any{
		"title": str("題名"), "headline": str("一言で"), "context": str("位置づけ"), "summary": str("要旨"),
		"scope": str("範囲"), "repo": str("リポジトリ"),
		"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "前に済ませる区切り"},
	}, "title", "headline", "context", "summary", "scope", "repo", "depends_on")
	evidence := object(map[string]any{"path": str("場所"), "note": str("手がかり")}, "path", "note")
	schema := object(map[string]any{
		"verdict":  map[string]any{"type": "string", "enum": J10VerdictValues, "description": "判定"},
		"reason":   str("理由"),
		"slices":   map[string]any{"type": []string{"array", "null"}, "items": slice, "description": "区切り"},
		"evidence": map[string]any{"type": "array", "items": evidence, "description": "根拠"},
	}, "verdict", "reason", "evidence")
	// 静的なリテラルなので Marshal は失敗しない。キーの順は常に辞書順。
	b, _ := json.Marshal(schema)
	return b
}

// checkJ10Text は s が空白だけでなく maxRunes 文字以内かを検査する。
func checkJ10Text(name, s string, maxRunes int) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s must not be blank", name)
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxRunes)
	}
	return nil
}

// validateJ10Output は区切りを分ける判断点の構造化出力を検査する。repos は区画「リポジトリ」に並べたリポジトリの名前。
// 返すエラーは、どの検査に落ちたか。根拠の場所のファイルは読まない。
func validateJ10Output(data []byte, repos []string) (*j10Output, error) {
	var out j10Output
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("not a valid output object: %w", err)
	}
	if !slices.Contains(J10VerdictValues, out.Verdict) {
		return nil, fmt.Errorf("unknown verdict %q", out.Verdict)
	}
	if err := checkJ10Text("reason", out.Reason, SliceTextMaxRunes); err != nil {
		return nil, err
	}
	if len(out.Evidence) == 0 {
		return nil, errors.New("evidence must not be empty")
	}
	for i, e := range out.Evidence {
		if strings.TrimSpace(e.Path) == "" || strings.TrimSpace(e.Note) == "" {
			return nil, fmt.Errorf("evidence[%d]: path and note must not be blank", i)
		}
		if filepath.IsAbs(e.Path) || slices.Contains(strings.Split(e.Path, "/"), "..") {
			return nil, fmt.Errorf("evidence[%d]: path must be relative and must not contain %q", i, "..")
		}
	}
	if out.Verdict == J10VerdictSingle {
		if len(out.Slices) != 0 {
			return nil, errors.New("slices must be empty when verdict is single")
		}
		return &out, nil
	}
	if len(out.Slices) < SliceMinCount || len(out.Slices) > SliceMaxCount {
		return nil, fmt.Errorf("slices must have %d to %d entries, got %d", SliceMinCount, SliceMaxCount, len(out.Slices))
	}
	for i, sl := range out.Slices {
		if err := validateJ10Slice(sl, i+1, repos); err != nil {
			return nil, fmt.Errorf("slices[%d]: %w", i, err)
		}
	}
	return &out, nil
}

// validateJ10Slice は番号 number（1 始まり）の区切りを検査する。
func validateJ10Slice(sl j10Slice, number int, repos []string) error {
	if sl.Title == nil || sl.Headline == nil || sl.Context == nil || sl.Summary == nil || sl.Scope == nil || sl.Repo == nil || sl.DependsOn == nil {
		return errors.New("a required field is missing")
	}
	if err := checkJ10Text("title", *sl.Title, SliceTitleMaxRunes); err != nil {
		return err
	}
	if err := CheckHeadline(*sl.Headline); err != nil {
		return err
	}
	if err := CheckContext(*sl.Context); err != nil {
		return err
	}
	if err := checkJ10Text("summary", *sl.Summary, SliceTextMaxRunes); err != nil {
		return err
	}
	if err := checkJ10Text("scope", *sl.Scope, SliceTextMaxRunes); err != nil {
		return err
	}
	if !slices.Contains(repos, *sl.Repo) {
		return fmt.Errorf("unknown repo %q", *sl.Repo)
	}
	seen := map[int]bool{}
	for _, d := range sl.DependsOn {
		// 自分より前の番号だけを許すので、依存の輪はできない。
		if d < 1 || d >= number || seen[d] {
			return fmt.Errorf("depends_on of slice %d has an invalid number %d", number, d)
		}
		seen[d] = true
	}
	return nil
}

// --- 対象の選び方 ---

// selectJ10AutoTargets は分類の段が区切りを分ける判断点の対象にする課題の内部整数 ID を、優先度→ID の昇順で返す。
// 対象は、区切りを分ける記録が pending の分類済の課題で、終了していない run を持たず、取り込み元の食い違いで外れないもの。
// 食い違いで外した課題は excluded に理由つきで返す。
func selectJ10AutoTargets(ctx context.Context, tx *sql.Tx) (out []int64, excluded []NotStarted, err error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusClassified)
	if err != nil {
		return nil, nil, err
	}
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		state, _, found, err := sliceSplitState(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if !found || state != SliceSplitStatePending {
			continue
		}
		active, err := challengeHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if active {
			continue
		}
		ex, err := challengeAutoExclusion(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if ex != nil {
			excluded = append(excluded, *ex)
			continue
		}
		out = append(out, cid)
	}
	return out, excluded, nil
}

// --- 入力の組み立て ---

// j10NoValue は区画の値が無いときの表示。
const j10NoValue = "なし"

// j10RepoSection は区画「リポジトリ」の本文。元のクローンの場所は宣言の slots.base（持たないリポジトリは「なし」）。
func j10RepoSection(repos []ConnectorRepo) string {
	if len(repos) == 0 {
		return j10NoValue
	}
	entries := make([]string, 0, len(repos))
	for _, r := range repos {
		base := r.Slots.Base
		if base == "" {
			base = j10NoValue
		}
		entries = append(entries, fmt.Sprintf("名前: %s\nremote: %s\n元のクローンの場所: %s", r.Name, r.Remote, base))
	}
	return strings.Join(entries, "\n\n")
}

// buildJ10Sections は区切りを分ける判断点の入力の 5 つの区画を、決まった並びで組み立てる。
func (s *Store) buildJ10Sections(ctx context.Context, ch Challenge, decl *AgentDeclaration, conn *ConnectorsDeclaration) ([]JudgmentDataSection, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, ErrNotFound
	}
	var sb *sourceBinding
	var classifyReason string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if sb, err = loadSourceBindingByChallengeID(ctx, tx, cid); err != nil {
			return err
		}
		_, classifyReason, _, err = sliceSplitState(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	position, err := os.ReadFile(filepath.Join(s.workspace, decl.PositionFile))
	if err != nil {
		return nil, fmt.Errorf("%w: read position_file %q: %w", ErrConfigInvalid, decl.PositionFile, err)
	}
	source := j10NoValue
	if sb != nil {
		source = "URL: " + sb.URL + "\n外部キー: " + sb.ExternalKey
	}
	if classifyReason == "" {
		classifyReason = j10NoValue
	}
	return []JudgmentDataSection{
		{Label: "課題", Content: judgmentChallengeSection(ch)},
		{Label: "取り込み元の対応", Content: source},
		{Label: "分類の理由", Content: classifyReason},
		{Label: "リポジトリ", Content: j10RepoSection(conn.Repos)},
		{Label: "ポジション定義", Content: string(position)},
	}, nil
}

// --- 呼び出し ---

// splitAutoJ10 は、区切りを分ける記録が pending の課題ごとに区切りを分ける判断点を 1 回呼び、結果を result に足す。
// in.ConnDecl が nil なら呼ばない。
func (s *Store) splitAutoJ10(ctx context.Context, in J1AutoInput, jc *JudgmentCycle, result *J1AutoResult) error {
	if in.ConnDecl == nil {
		return nil
	}
	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, excluded, err := selectJ10AutoTargets(ctx, tx)
		targetIDs = ids
		result.NotStarted = append(result.NotStarted, excluded...)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return err
	}
	for _, cid := range targetIDs {
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
		if err != nil {
			return err
		}
		// 対象を選んだ後に、状態・記録・取り込み元が変わりうるので、起動の直前に確かめる。
		pending, err := s.sliceSplitIsPending(ctx, ch.ID)
		if err != nil {
			return err
		}
		if ch.Status != StatusClassified || !pending {
			continue
		}
		var excluded *NotStarted
		err = s.db.Read(ctx, func(tx *sql.Tx) (err error) {
			excluded, err = challengeAutoExclusion(ctx, tx, cid)
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return err
		}
		if excluded != nil {
			result.NotStarted = append(result.NotStarted, *excluded)
			continue
		}
		held, skip, err := s.holdForJudgmentFailureLimit(ctx, *ch, JudgmentJ10, StatusClassified, in.AgentDecl, in.CycleID)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if held != nil {
			result.NotStarted = append(result.NotStarted, *held)
			continue
		}
		if err := s.splitOneJ10(ctx, in, jc, *ch, result); err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue
			}
			return err
		}
	}
	return nil
}

// splitAfterClassifyOne は、ID を指定した分類の判断点の実行 classified の後に、その課題の記録が pending なら
// 区切りを分ける判断点を呼ぶ。分類の判断点はすでに写し終えているので、周の上限で起動できないときはエラーにせず
// NotStarted にする。分類の判断点の実行が枠超過なら起動しない。
func (s *Store) splitAfterClassifyOne(ctx context.Context, in J1AutoInput, classified *RunJudgmentResult, result *J1AutoResult) error {
	if in.ConnDecl == nil {
		return nil
	}
	pending, err := s.sliceSplitIsPending(ctx, classified.ChallengeID)
	if err != nil || !pending {
		return err
	}
	if classified.RateLimited {
		result.NotStarted = append(result.NotStarted, NotStarted{ChallengeID: classified.ChallengeID, Reason: NotStartedRateLimited})
		return nil
	}
	ch, err := s.loadChallengeForAuto(ctx, classified.ChallengeID)
	if err != nil {
		return err
	}
	if ch.Status != StatusClassified {
		return nil
	}
	jc := in.Cycle
	if jc == nil {
		jc = NewJudgmentCycle(in.CycleID)
	}
	return s.splitOneJ10(ctx, in, jc, *ch, result)
}

// splitOneJ10 は課題 1 件の区切りを分ける判断点を呼び、結果を result に足す。jc が非 nil なら周の枠超過・周の上限の
// 扱いを JudgmentCycle に任せ（NotStarted にする）、nil なら周の上限で起動できないとき ErrBudgetExceeded を返す。
func (s *Store) splitOneJ10(ctx context.Context, in J1AutoInput, jc *JudgmentCycle, ch Challenge, result *J1AutoResult) error {
	sections, err := s.buildJ10Sections(ctx, ch, in.AgentDecl, in.ConnDecl)
	if err != nil {
		return err
	}
	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ10,
		MaxBudgetUSD: in.AgentDecl.JudgmentBudgetFor(JudgmentJ10),
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: j10OutputSchema(),
		Invoker:      in.Invoker,
	}
	var res *RunJudgmentResult
	if jc != nil {
		var notStarted *NotStarted
		res, notStarted, err = jc.RunJudgment(ctx, s, runIn)
		if err != nil {
			return err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
			return nil
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		if res, err = s.RunJudgment(ctx, runIn); err != nil {
			return err
		}
	}
	item := JudgmentAutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		repos := make([]string, 0, len(in.ConnDecl.Repos))
		for _, r := range in.ConnDecl.Repos {
			repos = append(repos, r.Name)
		}
		// 検査を通った出力は、ここでは記録にも課題にも写さない（記録は pending のまま）。
		out, invalid := validateJ10Output(res.StructuredOutput, repos)
		if invalid != nil {
			if err := s.invalidateRunOutputWithReason(ctx, res.RunID, "J10: "+invalid.Error()); err != nil {
				return err
			}
			item.Result = RunResultInvalidOutput
		} else {
			item.Outcome = string(out.Verdict)
		}
	}
	result.Items = append(result.Items, item)
	return nil
}
