package core

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画・決定 M3P14・M3P22）が持つ、J2（計画）固有の出力の検査・予算の既定の
// 解決・計画の本文の整形・課題への写像・対象の選び方・`plan --auto` の本体
// （Store.PlanAutoJ2）を持つ。判断点に依らない共通の部分は judgment_common.go、
// 上流の入力の組み立ては judgment_j2_upstream.go に置く。
//
// 1 回の流れ（§機能全体の設計「1 つの run の流れ」）:
//
//	① 上流の取得（J2 の起動の直前）→ 書き込みトランザクションで run を記録（RunJudgment）
//	② トランザクションの外で子を待つ（RunJudgment）
//	③ 書き込みトランザクションで結果を記録 → 出力を再検査 → 課題の現在の状態を
//	   読み直して写す（遷移元でなくなっていれば写さず、run の出力だけを残す）

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// --- J2 の出力スキーマ・検査 ---

// j2RawOutput は J2 の構造化出力（`--json-schema` に渡した形）をそのまま受ける
// 生の形（§IF / API「判断点の出力」J2 の行）。
type j2RawOutput struct {
	Verdict         string   `json:"verdict"`
	Summary         *string  `json:"summary"`
	Steps           []string `json:"steps"`
	Repo            *string  `json:"repo"`
	Operation       *string  `json:"operation"`
	Size            *string  `json:"size"`
	DoneCriteria    *string  `json:"done_criteria"`
	BudgetImplUSD   *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD *float64 `json:"budget_review_usd"`
	CrossRepo       *bool    `json:"cross_repo"`
	RelatedRepos    []string `json:"related_repos"`
	Question        *string  `json:"question"`
}

// j2ValidatedOutput は core の閉集合・必須の組・宣言との突き合わせを通った J2 の
// 出力。plan のときは Repo〜RelatedRepos・Resolved*・Spec が、uncertain のときは
// Question が有効である。
type j2ValidatedOutput struct {
	Verdict      J2Verdict
	Summary      string
	Steps        []string
	Repo         string
	Operation    string
	Size         JudgmentSize
	DoneCriteria string
	// BudgetImplUSD・BudgetReviewUSD は出力の値そのもの（null は nil のまま）。
	BudgetImplUSD   *float64
	BudgetReviewUSD *float64
	// ResolvedImplUSD・ResolvedReviewUSD は null をサイズの既定へ解決した額
	// （§J2・決定 M3P22。人は計画の承認でこの額を承認する）。
	ResolvedImplUSD   float64
	ResolvedReviewUSD float64
	CrossRepo         bool
	RelatedRepos      []string
	Question          *string
	// Spec は構造化した出力の JSON を空白を除いて詰めたもの（task_plan.spec。
	// null の額も出力のまま残す＝「spec は構造化した出力と一致する」）。
	Spec []byte
}

// j2ValidationContext は出力の検査が突き合わせる、宣言と課題の事実。
type j2ValidationContext struct {
	Agent *AgentDeclaration
	Conn  *ConnectorsDeclaration
	// HasSource は課題が取り込み元の対応（source_binding）を持つか。持たない課題で
	// 取り込み元の差し込み（{issue_number}・{issue_url}・{external_key}）を持つ
	// 操作を選んだ出力は不正（§J2）。
	HasSource bool
}

// j2OutputSchema は J2 の `--json-schema` に渡すスキーマを返す（§IF / API
// 「判断点の出力」の J2 の行から機械的に組み立てる。閉集合の値は
// J2VerdictValues・JudgmentSizeValues の定義を直接参照し、手で書き写さない）。
// `uncertain` は question 以外を null または空にしてよいので、必須は verdict だけ
// にする（plan の必須の組は core が検査し直す）。
func j2OutputSchema() []byte {
	verdicts := make([]string, 0, len(J2VerdictValues))
	for _, v := range J2VerdictValues {
		verdicts = append(verdicts, string(v))
	}
	sizes := []any{string(JudgmentSizeS), string(JudgmentSizeM), string(JudgmentSizeL), nil}
	nullableString := map[string]any{"type": []string{"string", "null"}}
	nullableNumber := map[string]any{"type": []string{"number", "null"}}
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":           map[string]any{"type": "string", "enum": verdicts},
			"summary":           nullableString,
			"steps":             stringArray,
			"repo":              nullableString,
			"operation":         nullableString,
			"size":              map[string]any{"type": []string{"string", "null"}, "enum": sizes},
			"done_criteria":     nullableString,
			"budget_impl_usd":   nullableNumber,
			"budget_review_usd": nullableNumber,
			"cross_repo":        map[string]any{"type": []string{"boolean", "null"}},
			"related_repos":     stringArray,
			"question":          nullableString,
		},
		"required":             []string{"verdict"},
		"additionalProperties": false,
	}
	// map[string]any のキーは常にアルファベット順で出力されるため決定的。schema は
	// 常に Marshal 可能な静的なリテラルなのでエラーは起こりえない。
	b, _ := json.Marshal(schema)
	return b
}

// invocationNeedsSource は操作の invocation が取り込み元の差し込みを持つかを返す
// （{challenge_id} は取り込み元の差し込みではない）。
func invocationNeedsSource(invocation string) bool {
	for _, ph := range []string{"{issue_number}", "{issue_url}", "{external_key}"} {
		if strings.Contains(invocation, ph) {
			return true
		}
	}
	return false
}

// findConnectorRepo は name の宣言のリポジトリと、そのリポジトリの接続ツールを返す。
func (d *ConnectorsDeclaration) findConnectorRepo(name string) (*ConnectorRepo, *Connector) {
	for i := range d.Repos {
		if d.Repos[i].Name != name {
			continue
		}
		for j := range d.Connectors {
			if d.Connectors[j].ID == d.Repos[i].Connector {
				return &d.Repos[i], &d.Connectors[j]
			}
		}
		return &d.Repos[i], nil
	}
	return nil, nil
}

// validateJ2Output は data（run の StructuredOutput）を検査する
// （§結果の判別「core が閉集合と必須の組…を検査し直す」・§J2）。次のいずれかで
// ok=false（run の結果は invalid_output になる）:
//   - JSON として解釈できない・判定が plan|uncertain の外
//   - uncertain で問いが無い・空
//   - plan で: 対象リポジトリが宣言に無い／そのリポジトリの接続ツールに操作が無い／
//     形態 cli の操作／取り込み元の対応の無い課題で取り込み元の差し込みを持つ操作／
//     サイズが S・M・L の外または無い／達成条件が空／複数リポジトリにまたがるかが
//     無い／額が 0 以下／実装枠（null はサイズの既定へ解決した額）が
//     max_run_budget_usd を超える／関係するリポジトリが宣言に無い
func validateJ2Output(data []byte, vc j2ValidationContext) (*j2ValidatedOutput, bool) {
	var raw j2RawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}

	switch J2Verdict(raw.Verdict) {
	case J2VerdictUncertain:
		if raw.Question == nil || strings.TrimSpace(*raw.Question) == "" {
			return nil, false
		}
		return &j2ValidatedOutput{Verdict: J2VerdictUncertain, Question: raw.Question}, true
	case J2VerdictPlan:
	default:
		return nil, false
	}

	// cross_repo は意思決定の主体の判定（S2）が読む安全に関わる項目なので、無い
	// 出力を「またがらない」と読まず、不正とする。
	if raw.Repo == nil || raw.Operation == nil || raw.Size == nil || raw.DoneCriteria == nil || raw.CrossRepo == nil {
		return nil, false
	}
	repo, connector := vc.Conn.findConnectorRepo(*raw.Repo)
	if repo == nil || connector == nil {
		return nil, false
	}
	var op *ConnectorOperation
	for i := range connector.Operations {
		if connector.Operations[i].ID == *raw.Operation {
			op = &connector.Operations[i]
			break
		}
	}
	if op == nil || connector.Form == ConnectorFormCLI {
		return nil, false
	}
	if !vc.HasSource && invocationNeedsSource(op.Invocation) {
		return nil, false
	}

	size := JudgmentSize(*raw.Size)
	pair, ok := vc.Agent.SizeBudgetFor(size)
	if !ok || strings.TrimSpace(*raw.DoneCriteria) == "" {
		return nil, false
	}

	impl, review := pair.Impl, pair.Review
	if raw.BudgetImplUSD != nil {
		if *raw.BudgetImplUSD <= 0 {
			return nil, false
		}
		impl = *raw.BudgetImplUSD
	}
	if raw.BudgetReviewUSD != nil {
		if *raw.BudgetReviewUSD <= 0 {
			return nil, false
		}
		review = *raw.BudgetReviewUSD
	}
	if impl > vc.Agent.MaxRunBudgetUSD {
		return nil, false
	}

	for _, r := range raw.RelatedRepos {
		if related, _ := vc.Conn.findConnectorRepo(r); related == nil {
			return nil, false
		}
	}

	var spec bytes.Buffer
	if err := json.Compact(&spec, data); err != nil {
		return nil, false
	}

	v := &j2ValidatedOutput{
		Verdict:           J2VerdictPlan,
		Steps:             raw.Steps,
		Repo:              *raw.Repo,
		Operation:         *raw.Operation,
		Size:              size,
		DoneCriteria:      *raw.DoneCriteria,
		BudgetImplUSD:     raw.BudgetImplUSD,
		BudgetReviewUSD:   raw.BudgetReviewUSD,
		ResolvedImplUSD:   impl,
		ResolvedReviewUSD: review,
		RelatedRepos:      raw.RelatedRepos,
		Spec:              spec.Bytes(),
	}
	if raw.Summary != nil {
		v.Summary = *raw.Summary
	}
	v.CrossRepo = *raw.CrossRepo
	return v, true
}

// --- 計画の本文の整形 ---

func formatUSDAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatJ2PlanBody は検査済みの plan の出力を、計画の本文（Markdown）へ決定的に
// 整形する（§J2「出力を計画の本文へ決定的に整形し」）。対象リポジトリ・操作の id・
// サイズ・完了条件・実装枠・レビュー対応枠の額を必ず含む（人間は計画の承認で、この
// 額と操作を承認する＝設計書 §9）。時刻・乱数など、出力以外の値は含めない。
func formatJ2PlanBody(v *j2ValidatedOutput) string {
	var b strings.Builder
	b.WriteString("# 計画\n\n")
	// 人が承認する事実（対象・予算）を先頭に置き、出力の自由記述（要約・手順・
	// 完了条件）はその後に置く。自由記述の中に見出しや額を装った文があっても、
	// 承認する事実が先に読めるようにするため。
	b.WriteString("## 対象\n\n")
	b.WriteString("- リポジトリ: " + v.Repo + "\n")
	b.WriteString("- 操作: " + v.Operation + "\n")
	b.WriteString("- サイズ: " + string(v.Size) + "\n")
	if v.CrossRepo {
		b.WriteString("- 複数リポジトリにまたがる: はい\n")
	} else {
		b.WriteString("- 複数リポジトリにまたがる: いいえ\n")
	}
	if len(v.RelatedRepos) > 0 {
		b.WriteString("- 関係するリポジトリ: " + strings.Join(v.RelatedRepos, "、") + "\n")
	}
	b.WriteString("\n## 予算\n\n")
	b.WriteString("- 実装枠: " + formatUSDAmount(v.ResolvedImplUSD) + " USD\n")
	b.WriteString("- レビュー対応枠: " + formatUSDAmount(v.ResolvedReviewUSD) + " USD\n")
	if s := strings.TrimSpace(v.Summary); s != "" {
		b.WriteString("\n## 要約\n\n" + s + "\n")
	}
	if len(v.Steps) > 0 {
		b.WriteString("\n## 手順\n\n")
		for i, step := range v.Steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(step))
		}
	}
	b.WriteString("\n## 完了条件\n\n" + strings.TrimSpace(v.DoneCriteria) + "\n")
	return b.String()
}

// --- J2 の対象の選び方 ---

// selectJ2AutoTargets は ID を省略した `plan --auto`・`cycle` の計画の段が対象に
// する課題の内部整数 ID を、優先度→ID の昇順で返す（§判断点の共通の規則）。
// 対象は分類済で、終了していない run を持たず、上流の状態が closed・missing・
// ポリシーの状態が out_of_policy でない課題。
func selectJ2AutoTargets(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusClassified)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		active, err := challengeHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if active {
			continue
		}
		excluded, err := challengeAutoExcludedByPolicy(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if excluded {
			continue
		}
		out = append(out, cid)
	}
	return out, nil
}

// j2StillEligibleForAuto は、対象選び（selectJ2AutoTargets）から実際に起動する
// までの間に、上流・ポリシーの状態が変わっていないかを起動の直前に読み直して
// 確かめる（`plan --auto` はサイクルの排他ロックを取らないため、並行する
// 取り込みの進行と時間差が生じうる。J1 の j1StillEligibleForAuto と同じ理由）。
// 終了していない run の有無は RunJudgment の①が最終防衛として検査する。
func (s *Store) j2StillEligibleForAuto(ctx context.Context, cid int64) (bool, error) {
	var excluded bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ex, err := challengeAutoExcludedByPolicy(ctx, tx, cid)
		excluded = ex
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return !excluded, nil
}

// --- J2 の入力の組み立て ---

// j2Prepared は J2 の 1 回の起動の準備（入力の区画と、写像で使う事実）。
type j2Prepared struct {
	Sections []JudgmentDataSection
	// HasSource は課題が取り込み元の対応を持つか（出力の検査に使う）。
	HasSource bool
	// Read が非 nil のときだけ、計画を登録した後に読んだ記録を付けてよい
	// （取り込み元の対応があり、最新のコメントをすべて入力へ含めた場合）。値は
	// J2 の直前の取得で得たコメント数と更新日時（ストアの観測値ではない）。
	Read *readValues
}

// parseExternalKey は "<owner>/<name>#<番号>" を repo と番号へ分ける。
func parseExternalKey(key string) (repo string, number int, ok bool) {
	i := strings.LastIndex(key, "#")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return key[:i], n, true
}

// prepareJ2 は ch の J2 の入力を組み立てる。取り込み元の対応のある課題では、
// ここ（J2 の起動の直前）で上流の本文・全コメント・参照先の Issue を取得する
// （対応の無い課題では上流の取得を呼ばない）。取得に失敗したら NotStarted
// （upstream_fetch_failed）を返し、run を作らない＝状態と版を変えない。
func (s *Store) prepareJ2(ctx context.Context, ch Challenge, in J2AutoInput) (*j2Prepared, *NotStarted, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, nil, ErrNotFound
	}

	var holds []Hold
	var sb *sourceBinding
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		hs, err := loadHolds(ctx, tx, cid)
		if err != nil {
			return err
		}
		holds = hs
		b, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		sb = b
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}

	posContent, err := os.ReadFile(filepath.Join(s.workspace, in.AgentDecl.PositionFile))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read position_file %q: %w", ErrConfigInvalid, in.AgentDecl.PositionFile, err)
	}

	prepared := &j2Prepared{HasSource: sb != nil}

	var upstream *j2UpstreamInput
	if sb != nil {
		repo, number, ok := parseExternalKey(sb.ExternalKey)
		if !ok {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed,
				Detail: fmt.Sprintf("malformed external_key %q", sb.ExternalKey)}, nil
		}
		uc, err := FetchUpstreamContext(ctx, in.Upstream, repo, number)
		if err != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}, nil
		}
		built := buildUpstreamInput(uc)
		upstream = &built
		if built.fullyRead() {
			prepared.Read = &readValues{CommentsCount: built.ObservedCommentsCount, UpstreamUpdatedAt: built.ObservedUpdatedAt}
		}
	}

	urgency := ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	priority := ""
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	issueBody := fmt.Sprintf(
		"タイトル: %s\n\n説明:\n%s\n\n達成条件:\n%s\n\n緊急度: %s\n優先度: %s\n起票者: %s",
		ch.Title, ch.Description, ch.DoneCriteria, urgency, priority, ch.Reporter,
	)

	sections := []JudgmentDataSection{
		{Label: "課題", Content: issueBody},
		{Label: "ポジション定義", Content: string(posContent)},
	}
	if holdsText := formatHoldsSection(holds); holdsText != "" {
		sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: holdsText})
	}
	if sourceText := formatSourceBindingSection(sb); sourceText != "" {
		sections = append(sections, JudgmentDataSection{Label: "取り込み元の対応", Content: sourceText})
	}
	if upstream != nil {
		sections = append(sections, upstream.Sections...)
	}
	sections = append(sections,
		JudgmentDataSection{Label: "対象リポジトリと操作の宣言", Content: formatJ2DeclarationSection(in.ConnDecl, prepared.HasSource, in.AgentDecl)},
		JudgmentDataSection{Label: "サイズごとの予算の既定", Content: formatJ2BudgetDefaultsSection(in.AgentDecl)},
	)
	prepared.Sections = sections
	return prepared, nil, nil
}

func yesNo(b bool) string {
	if b {
		return "はい"
	}
	return "いいえ"
}

// formatJ2DeclarationSection は J2 が選べる対象リポジトリと操作の一覧
// （§J2「宣言の対象リポジトリと操作の一覧（id・形態・対話前提か・成果物の種類）」）。
// J2 が選んでも不正になる操作には注記を付ける（形態 cli は現在の委譲に使えない・
// 取り込み元の対応の無い課題では取り込み元の差し込みを持つ操作は選べない）。
func formatJ2DeclarationSection(conn *ConnectorsDeclaration, hasSource bool, agent *AgentDeclaration) string {
	var b strings.Builder
	if hasSource {
		b.WriteString("この課題は取り込み元の GitHub Issue と対応している。\n")
	} else {
		b.WriteString("この課題は取り込み元の GitHub Issue と対応していない。\n")
	}
	fmt.Fprintf(&b, "1 回の実装の枠として選べる額の上限: %s USD\n", formatUSDAmount(agent.MaxRunBudgetUSD))
	for _, r := range conn.Repos {
		b.WriteString("\nリポジトリ: " + r.Name + "（" + r.Remote + "・既定ブランチ: " + r.DefaultBranch + "）\n")
		_, connector := conn.findConnectorRepo(r.Name)
		if connector == nil {
			continue
		}
		b.WriteString("  接続ツール: " + connector.ID + "（形態: " + string(connector.Form) + "）\n")
		b.WriteString("  操作:\n")
		for _, op := range connector.Operations {
			artifacts := op.Artifacts
			if artifacts == "" {
				artifacts = "指定なし"
			}
			line := "    - id: " + op.ID + "｜対話前提: " + yesNo(op.Interactive) + "｜成果物: " + artifacts
			switch {
			case connector.Form == ConnectorFormCLI:
				line += "｜注記: この形態の操作は現在の委譲に使えないため選べない"
			case !hasSource && invocationNeedsSource(op.Invocation):
				line += "｜注記: 取り込み元の Issue の情報を必要とするため、この課題では選べない"
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// formatJ2BudgetDefaultsSection はサイズごとの予算の既定（§J2 の入力）。
func formatJ2BudgetDefaultsSection(agent *AgentDeclaration) string {
	var b strings.Builder
	b.WriteString("サイズごとの既定の額（実装の枠・レビュー対応の枠。USD）。出力で額を null にすると、選んだサイズのこの額になる:\n")
	for _, sz := range JudgmentSizeValues {
		pair, _ := agent.SizeBudgetFor(sz)
		fmt.Fprintf(&b, "  %s: 実装 %s・レビュー対応 %s\n", sz, formatUSDAmount(pair.Impl), formatUSDAmount(pair.Review))
	}
	return b.String()
}

// --- J2 の出力の写像 ---

// errJ2MappingNotEligible は、写す直前に読み直した課題が J2 の遷移元（分類済）で
// なくなっていたことを表す内部の sentinel（§アーキテクチャ決定③「読み直した状態が
// 遷移元でなくなっていれば写さない」）。ErrInvalidTransition・ErrTerminalState と
// 同じく mapped=false（エラーではない）として扱う。
var errJ2MappingNotEligible = errors.New("core: j2 mapping is no longer eligible (challenge is not classified anymore)")

// applyJ2Plan は判定 plan の出力を課題へ写す（M1 T3。PlanChallenge と同じ遷移を、
// 経路 invoker・本人確認 none・原因の run の ID つきで実行し、計画の本文と spec を
// 1 版として登録する）。T3 と T4 は同じ操作 plan なので、遷移表の照合だけでは
// 「計画承認待ちの計画の改訂〈T4〉」を弾けない。J2 の対象は分類済の課題だけなので、
// 写す直前に読み直した状態を分類済と照合する。
func (s *Store) applyJ2Plan(ctx context.Context, runIDDisplay, challengeIDDisplay, body string, spec []byte) (mapped bool, err error) {
	specText := string(spec)
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpPlan, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusClassified {
			return errJ2MappingNotEligible
		}
		if _, err := insertTaskPlanVersion(ctx, tc, body, &specText); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errJ2MappingNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// applyJ2Uncertain は判定 uncertain の出力を課題へ写す（M1 T11）。J2 の対象は
// 分類済の課題だけなので、写す直前に読み直した状態を分類済と照合する（J1 の
// applyJ1Uncertain と同じ理由）。
func (s *Store) applyJ2Uncertain(ctx context.Context, runIDDisplay, challengeIDDisplay, question string) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusClassified {
			return errJ2MappingNotEligible
		}
		if err := insertHold(ctx, tc, question); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errJ2MappingNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// j2Finalize は finalizeJ2Output が使う、起動の前に決まっていた事実。
type j2Finalize struct {
	Validation j2ValidationContext
	// Read は j2Prepared.Read（nil なら読んだ記録を付けない）。
	Read *readValues
}

// finalizeJ2Output は run が succeeded の場合に、core の再検査（閉集合・必須の組・
// 宣言との突き合わせ）→写像（plan/uncertain）→読んだ記録を行う。再検査に落ちたら
// run の結果を invalid_output へ更新し、課題は変えない。outcome は判定の文字列
// （invalid_output に落ちた場合は空）、status は写した後の課題の状態コード
// （写さなかった場合は nil）、result は最終的な run の結果、note は人が読む補足
// （読んだ記録を付けられなかったときの理由。無ければ空）。
//
// 読んだ記録（§J2・決定 M3P14・QH14）は、計画を実際に登録できたときだけ、J2 の
// 直前の取得の値で付ける。J2 の後に取り込みで観測値が進んでいても、その値は
// 使わない（読んでいないコメントを既読にしない）。計画の登録と読んだ記録は別の
// 書き込みトランザクションなので、読んだ記録の書き込みに失敗しても、登録済みの
// 計画の結果は失わない（読んだ記録が付かないだけ＝未読の更新が残る安全な側）。
// 失敗は note に残す。
func (s *Store) finalizeJ2Output(ctx context.Context, runIDDisplay, challengeIDDisplay string, structuredOutput []byte, fin j2Finalize) (outcome string, status *string, result RunResult, note string, err error) {
	validated, ok := validateJ2Output(structuredOutput, fin.Validation)
	if !ok {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", nil, "", "", err
		}
		return "", nil, RunResultInvalidOutput, "", nil
	}

	switch validated.Verdict {
	case J2VerdictPlan:
		mapped, err := s.applyJ2Plan(ctx, runIDDisplay, challengeIDDisplay, formatJ2PlanBody(validated), validated.Spec)
		if err != nil {
			return "", nil, "", "", err
		}
		if !mapped {
			return string(J2VerdictPlan), nil, RunResultSucceeded, "", nil
		}
		if fin.Read != nil {
			_, err := s.markReadWithValues(ctx, ChannelInvoker, challengeIDDisplay, markReadOptions{RunID: runIDDisplay, Values: fin.Read})
			// 課題がその間に完了した（ErrTerminalState）場合は付けない。
			if err != nil && !errors.Is(err, ErrTerminalState) {
				note = "計画は登録したが、上流を読んだ記録を付けられなかった: " + err.Error()
			}
		}
		st := string(StatusAwaitingPlanApproval)
		return string(J2VerdictPlan), &st, RunResultSucceeded, note, nil
	case J2VerdictUncertain:
		mapped, err := s.applyJ2Uncertain(ctx, runIDDisplay, challengeIDDisplay, *validated.Question)
		if err != nil {
			return "", nil, "", "", err
		}
		if mapped {
			st := string(StatusAwaitingHuman)
			return string(J2VerdictUncertain), &st, RunResultSucceeded, "", nil
		}
		return string(J2VerdictUncertain), nil, RunResultSucceeded, "", nil
	default:
		return "", nil, RunResultSucceeded, "", nil
	}
}

// --- `plan --auto` の本体 ---

// J2AutoInput は Store.PlanAutoJ2 の入力。
type J2AutoInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（§判断点の共通の
	// 規則「課題の ID を指定した個別の操作は、上の除外を適用しない」）。nil なら、
	// selectJ2AutoTargets が選ぶ対象すべてを処理する。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	// ConnDecl は宣言の接続ツール・リポジトリ（LoadConnectorsDeclaration の結果）。
	ConnDecl *ConnectorsDeclaration
	Invoker  JudgmentInvoker
	// Upstream は J2 の起動の直前の上流の取得（core の IF。実装は
	// internal/adapters/github）。取り込み元の対応の無い課題では呼ばれない。
	Upstream UpstreamThreadSource
	// CycleID はこの操作が属する周（BeginCycle が返した ID）。空は ErrValidation。
	CycleID string
	// Cycle が非 nil なら、ID を省略した対象の処理の枠超過の状態をこの
	// JudgmentCycle と共有する（J1AutoInput.Cycle と同じ規則。`cycle` の段が
	// 分類の段と同じものを渡す）。nil ならこの呼び出しの中だけのものを作る。
	Cycle *JudgmentCycle
}

// J2AutoItem は `plan --auto` が処理した課題 1 件の結果（judgment_batch.go の
// JudgmentAutoItem。`classify --auto` の J1AutoItem と同じ形）。J2 の Outcome は
// plan/uncertain。
type J2AutoItem = JudgmentAutoItem

// J2AutoResult は Store.PlanAutoJ2 の出力（JudgmentAutoResult）。
type J2AutoResult = JudgmentAutoResult

// PlanAutoJ2 は `flywheel plan --auto [<C-ID>]` の本体。対象の選び方・上流の取得・
// 起動・出力の写像・読んだ記録は core が持ち、呼び出し元（internal/cli）は宣言の
// 読み込み・invoker と上流の取得の組み立て・周の開始と終了だけを行う（P2）。
//
//   - in.ChallengeID が非 nil（ID を指定した個別の操作）: その課題が分類済でなければ
//     ErrInvalidTransition、終了していない run があれば ErrRunInProgress、周の上限で
//     起動できなければ ErrBudgetExceeded をそのまま返す。上流の取得に失敗した
//     ときは NotStarted（upstream_fetch_failed）を結果に返す（終了コードを持つ
//     エラーではない）。
//   - in.ChallengeID が nil（自動の対象）: selectJ2AutoTargets が選んだ対象を順に
//     処理する。枠超過・周の上限・上流の取得の失敗で起動しなかった課題は
//     NotStarted へ、並行して他から起動された課題（ErrRunInProgress）は静かに
//     スキップする。
func (s *Store) PlanAutoJ2(ctx context.Context, in J2AutoInput) (*J2AutoResult, error) {
	if in.AgentDecl == nil || in.ConnDecl == nil || in.Invoker == nil || in.Upstream == nil || in.CycleID == "" {
		return nil, ErrValidation
	}

	result := &J2AutoResult{}

	if in.ChallengeID != nil {
		ch, err := s.loadChallengeForAuto(ctx, *in.ChallengeID)
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusClassified {
			return nil, ErrInvalidTransition
		}
		// 上流の取得（gh の呼び出し）を無駄にしないため、終了していない run が
		// あれば取得の前に ErrRunInProgress にする（RunJudgment の①が最終防衛）。
		cid, _ := parseChallengeID(ch.ID)
		var active bool
		err = s.db.Read(ctx, func(tx *sql.Tx) error {
			a, err := challengeHasActiveRun(ctx, tx, cid)
			active = a
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
		if active {
			return nil, ErrRunInProgress
		}
		item, notStarted, err := s.planOneJ2(ctx, in, nil, *ch)
		if err != nil {
			return nil, err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
		}
		if item != nil {
			result.Items = append(result.Items, *item)
		}
		return result, nil
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, err := selectJ2AutoTargets(ctx, tx)
		targetIDs = ids
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}

	jc := in.Cycle
	if jc == nil {
		jc = NewJudgmentCycle(in.CycleID)
	}
	for _, cid := range targetIDs {
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusClassified {
			// 同じ周の中で並行して状態が変わった（例: 人間が先に計画を登録した）。
			continue
		}
		eligible, err := s.j2StillEligibleForAuto(ctx, cid)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}
		item, notStarted, err := s.planOneJ2(ctx, in, jc, *ch)
		if err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue
			}
			return nil, err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
		}
		if item != nil {
			result.Items = append(result.Items, *item)
		}
	}
	return result, nil
}

// planOneJ2 は課題 1 件の J2 を実行する。jc が非 nil なら周の枠超過・周の上限の
// 扱いを JudgmentCycle に任せ（NotStarted を返す）、nil（ID を指定した個別の操作）
// なら周の上限で起動できないとき ErrBudgetExceeded を返す。返り値の item と
// notStarted はどちらか一方だけが非 nil（エラー時は両方 nil）。
func (s *Store) planOneJ2(ctx context.Context, in J2AutoInput, jc *JudgmentCycle, ch Challenge) (*J2AutoItem, *NotStarted, error) {
	if jc != nil && jc.RateLimited() {
		// 枠超過を記録した周では、上流の取得（gh の呼び出し）もしない。
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}

	// 周の上限を使い切っているのに上流の取得（gh の GET を最大 7 回）だけ行って
	// cycle_budget で終わる無駄を避けるため、取得の前に周の残額を読むだけで確かめる
	// （最終の判定は RunJudgment の①のトランザクションが行う）。
	maxBudget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ2)
	if err := s.checkCycleBudget(ctx, in.CycleID, maxBudget); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}

	prepared, notStarted, err := s.prepareJ2(ctx, ch, in)
	if err != nil || notStarted != nil {
		return nil, notStarted, err
	}

	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ2,
		MaxBudgetUSD: maxBudget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     prepared.Sections,
		OutputSchema: j2OutputSchema(),
		Invoker:      in.Invoker,
	}
	var res *RunJudgmentResult
	if jc != nil {
		res, notStarted, err = jc.RunJudgment(ctx, s, runIn)
		if err != nil || notStarted != nil {
			return nil, notStarted, err
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		res, err = s.RunJudgment(ctx, runIn)
		if err != nil {
			return nil, nil, err
		}
	}

	item := J2AutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		outcome, status, finalResult, note, err := s.finalizeJ2Output(ctx, res.RunID, res.ChallengeID, res.StructuredOutput, j2Finalize{
			Validation: j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: prepared.HasSource},
			Read:       prepared.Read,
		})
		if err != nil {
			return nil, nil, err
		}
		item.Outcome = outcome
		item.Status = status
		item.Result = finalResult
		item.Note = note
	}
	return &item, nil, nil
}
