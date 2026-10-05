package core

// このファイルは J5（検証）の本体（親要件チケット #98 §J5 検証・§失敗・差し戻しの上限。
// 決定 M3P17・M3P18・M3P19・M3P39・M3P42）を持つ。
//
// 1 件の流れ:
//
//	① 課題・承認済みの計画・直前の委譲の run と成果物を読む
//	② 成果物に PR があれば、その PR のチェックを取得する（GET だけ）。完了していないものが
//	   1 つでもあれば J5 を起動せず、課題の状態と版を変えずに waiting_external として返す。
//	   PR が無ければ CI を調べず、入力に PR が無いことを書く
//	③ 上流の最新の状態を取得して J5 を起動する
//	④ 出力を検査し（不正なら run を invalid_output にして課題は変えない）、判定を M1 の
//	   verify（T8〜T10）へ経路 invoker・原因の run の ID つきで写す
//
// CI の完了の判定規則（checkCompleted）は core が持つ。取得と正規化は UpstreamCheckSource
// （internal/adapters/github）が担う。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NotStartedWaitingExternal は、直前の委譲の成果物の PR のチェックが完了していないため J5 を
// 起動しなかったことを表す（M3P42）。`cycle` の not_started の閉集合（NotStartedReasonValues）に含まれる。
const NotStartedWaitingExternal NotStartedReason = "waiting_external"

// --- J5 の出力スキーマ・検査 ---

type j5RawOutput struct {
	Verdict  string  `json:"verdict"`
	Reason   *string `json:"reason"`
	Feedback *string `json:"feedback"`
	Question *string `json:"question"`
}

// j5ValidatedOutput は検査を通った J5 の出力。
type j5ValidatedOutput struct {
	Verdict  J5Verdict
	Reason   string
	Feedback *string
	Question *string
}

// j5OutputSchema は J5 の `--json-schema` に渡すスキーマ（§IF / API「判断点の出力」の J5 の行）。
// 閉集合の値は J5VerdictValues から作る。
func j5OutputSchema() []byte {
	verdicts := make([]string, 0, len(J5VerdictValues))
	for _, v := range J5VerdictValues {
		verdicts = append(verdicts, string(v))
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":  map[string]any{"type": "string", "enum": verdicts},
			"reason":   map[string]any{"type": "string"},
			"feedback": map[string]any{"type": []string{"string", "null"}},
			"question": map[string]any{"type": []string{"string", "null"}},
		},
		"required":             []string{"verdict", "reason"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return b
}

// validateJ5Output は J5 の出力を検査する。閉集合の外の判定・理由の欠落・
// 判定が not_met なのに差し戻しの指摘が無い／空・判定が uncertain なのに問いが無い／空は ok=false。
func validateJ5Output(data []byte) (*j5ValidatedOutput, bool) {
	var raw j5RawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	verdict := J5Verdict(raw.Verdict)
	switch verdict {
	case J5VerdictMet, J5VerdictNotMet, J5VerdictUncertain:
	default:
		return nil, false
	}
	if raw.Reason == nil {
		return nil, false
	}
	blank := func(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }
	if verdict == J5VerdictNotMet && blank(raw.Feedback) {
		return nil, false
	}
	if verdict == J5VerdictUncertain && blank(raw.Question) {
		return nil, false
	}
	return &j5ValidatedOutput{Verdict: verdict, Reason: *raw.Reason, Feedback: raw.Feedback, Question: raw.Question}, true
}

// --- CI の判定規則 ---

// checkCompleted はチェック 1 件が完了しているか。完了していないのは、まだ実行中・待機中・
// 状態が未確定のもの。
func checkCompleted(c UpstreamCheck) bool {
	if c.Status == UpstreamCheckStatusLegacy {
		// 旧式のコミットステータスは、結果が確定していれば完了（pending・未知の値は完了していない）。
		switch c.Conclusion {
		case "success", "failure", "error":
			return true
		}
		return false
	}
	return c.Status == "completed"
}

// pendingPullRequestURL は prs のうち、完了していないチェックを持つ PR の URL（最初の 1 件）を返す。
// 待つのは open かマージ済みの PR だけ（閉じられた未マージの PR のチェックは待たない）。
// 無ければ ""。チェックが 1 件も無い PR は待たない（CI が無い）。
func pendingPullRequestURL(prs []UpstreamPullRequestChecks) string {
	for _, pr := range prs {
		if pr.State != string(artifactStateOpen) && pr.State != string(artifactStateMerged) {
			continue
		}
		for _, c := range pr.Checks {
			if !checkCompleted(c) {
				return pr.URL
			}
		}
	}
	return ""
}

// parsePullRequestURL は PR の URL（https://<host>/<owner>/<repo>/pull/<n>）を repo と番号へ分ける。
func parsePullRequestURL(raw string) (repo string, number int, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != 4 || segs[2] != "pull" || segs[0] == "" || segs[1] == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(segs[3])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return segs[0] + "/" + segs[1], n, true
}

// fetchPullRequestChecks は成果物の PR（run_artifact の kind が pr の行）のチェックを取得する。
func fetchPullRequestChecks(ctx context.Context, src UpstreamCheckSource, artifacts []runArtifactRow) ([]UpstreamPullRequestChecks, error) {
	var out []UpstreamPullRequestChecks
	for _, a := range artifacts {
		if a.Kind != artifactKindPR {
			continue
		}
		repo, n, ok := parsePullRequestURL(a.Ref)
		if !ok {
			return nil, fmt.Errorf("core: malformed pull request url %q", a.Ref)
		}
		pr, err := src.GetPullRequestChecks(ctx, repo, n)
		if err != nil {
			return nil, fmt.Errorf("core: fetch checks of %s: %w", a.Ref, err)
		}
		out = append(out, pr)
	}
	return out, nil
}

// --- J5 の対象と入力 ---

// j5Context は 1 件の検証の前に読む、課題と承認済みの計画と直前の委譲の事実。
type j5Context struct {
	Challenge    Challenge
	Plan         approvedPlan
	Holds        []Hold
	Binding      *sourceBinding
	LastDelegate *runRow
	Artifacts    []runArtifactRow
}

func (s *Store) loadJ5Context(ctx context.Context, ch Challenge) (*j5Context, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, ErrNotFound
	}
	vc := &j5Context{Challenge: ch}
	var planOK bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		p, ok, err := loadApprovedPlanAnySpec(ctx, tx, cid)
		if err != nil {
			return err
		}
		vc.Plan, planOK = p, ok
		if !ok {
			return nil
		}
		if vc.Holds, err = loadHolds(ctx, tx, cid); err != nil {
			return err
		}
		if vc.Binding, err = loadSourceBindingByChallengeID(ctx, tx, cid); err != nil {
			return err
		}
		var lastID int64
		qerr := tx.QueryRowContext(ctx,
			`SELECT id FROM run WHERE challenge_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, cid, string(runKindDelegate)).Scan(&lastID)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		if vc.LastDelegate, err = loadRunByID(ctx, tx, lastID); err != nil {
			return err
		}
		vc.Artifacts, err = listRunArtifacts(ctx, tx, lastID)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if !planOK {
		return nil, fmt.Errorf("%w: the challenge has no approved plan", ErrValidation)
	}
	return vc, nil
}

// effectiveDoneCriteria は J5 に渡す達成条件と、その出所の説明を返す。課題の達成条件が空なら、
// 承認済みの計画の構造化した出力の達成条件（M3P19）。どちらも無ければ空。
func (vc *j5Context) effectiveDoneCriteria() (criteria, origin string) {
	if c := strings.TrimSpace(vc.Challenge.DoneCriteria); c != "" {
		return c, "課題の達成条件"
	}
	if vc.Plan.HasSpec {
		var sp j2RawOutput
		if json.Unmarshal([]byte(vc.Plan.Spec), &sp) == nil && sp.DoneCriteria != nil && strings.TrimSpace(*sp.DoneCriteria) != "" {
			return strings.TrimSpace(*sp.DoneCriteria), "承認済みの計画の達成条件（課題の達成条件が空のため）"
		}
	}
	return "", "課題にも承認済みの計画にも達成条件が無い"
}

func formatReconciliationSection(vc *j5Context) string {
	if vc.LastDelegate == nil {
		return "直前の委譲の run は無い（人が検証へ進めた課題）。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "直前の委譲の run: %s（結果: %s）\n", vc.LastDelegate.ID, vc.LastDelegate.Result)
	if len(vc.Artifacts) == 0 {
		b.WriteString("照合で確かめられた成果物: なし")
		return b.String()
	}
	b.WriteString("照合で確かめられた成果物:\n")
	for _, a := range vc.Artifacts {
		fmt.Fprintf(&b, "- %s: %s", a.Kind, a.Ref)
		if a.State != "" {
			fmt.Fprintf(&b, "（状態: %s、base: %s）", a.State, a.Base)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatDelegationReportSection(vc *j5Context) string {
	if vc.LastDelegate == nil {
		return "直前の委譲の報告は無い。"
	}
	if strings.TrimSpace(vc.LastDelegate.Output) == "" {
		return "直前の委譲は報告を返さなかった（結果: " + string(vc.LastDelegate.Result) + "）。"
	}
	return vc.LastDelegate.Output
}

// formatPullRequestSection は PR の状態とチェックの結果。prs が空なら、PR が無いことを書く
// （CI は調べていない。M3P42）。
func formatPullRequestSection(prs []UpstreamPullRequestChecks) string {
	if len(prs) == 0 {
		return "直前の委譲の成果物に PR は無い。CI は調べていない。"
	}
	var b strings.Builder
	for i, pr := range prs {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "PR: %s\nタイトル: %s\n状態: %s\nbase: %s\nhead のコミット: %s\n", pr.URL, pr.Title, pr.State, pr.Base, pr.HeadSHA)
		if len(pr.Checks) == 0 {
			b.WriteString("チェック: なし\n")
			continue
		}
		b.WriteString("チェック:\n")
		for _, c := range pr.Checks {
			if c.Conclusion != "" {
				fmt.Fprintf(&b, "- %s: %s（結論: %s）\n", c.Name, c.Status, c.Conclusion)
			} else {
				fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Status)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prepareJ5 は J5 の入力（データの区画）を組み立てる。取り込み元の対応のある課題では、ここ
// （J5 の起動の直前）で上流の本文・全コメント・参照先を取得する（読んだ記録は付けない）。
// 取得に失敗したら NotStarted（upstream_fetch_failed）を返し、run を作らない。
func prepareJ5(ctx context.Context, upstream UpstreamThreadSource, vc *j5Context, prs []UpstreamPullRequestChecks) ([]JudgmentDataSection, *NotStarted) {
	ch := vc.Challenge
	urgency, priority := "", ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	criteria, origin := vc.effectiveDoneCriteria()
	if criteria == "" {
		criteria = "(なし)"
	}
	sections := []JudgmentDataSection{
		{Label: "課題", Content: fmt.Sprintf("タイトル: %s\n\n説明:\n%s\n\n緊急度: %s\n優先度: %s\n起票者: %s",
			ch.Title, ch.Description, urgency, priority, ch.Reporter)},
		{Label: "達成条件", Content: criteria + "\n\n（出所: " + origin + "）"},
		{Label: "承認済みの計画の本文", Content: vc.Plan.Body},
	}
	if vc.Plan.HasSpec {
		sections = append(sections, JudgmentDataSection{Label: "承認済みの計画の構造化した出力", Content: vc.Plan.Spec})
	}
	sections = append(sections,
		JudgmentDataSection{Label: "直前の委譲の報告", Content: formatDelegationReportSection(vc)},
		JudgmentDataSection{Label: "照合の結果", Content: formatReconciliationSection(vc)},
		JudgmentDataSection{Label: "PR の状態とチェックの結果", Content: formatPullRequestSection(prs)},
	)
	if t := formatHoldsSection(vc.Holds); t != "" {
		sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: t})
	}
	if t := formatSourceBindingSection(vc.Binding); t != "" {
		sections = append(sections, JudgmentDataSection{Label: "取り込み元の対応", Content: t})
	}
	if vc.Binding != nil {
		repo, number, ok := parseExternalKey(vc.Binding.ExternalKey)
		if !ok {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed,
				Detail: fmt.Sprintf("malformed external_key %q", vc.Binding.ExternalKey)}
		}
		uc, err := FetchUpstreamContext(ctx, upstream, repo, number)
		if err != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}
		}
		sections = append(sections, buildUpstreamInput(uc).Sections...)
	}
	return sections, nil
}

// --- J5 の対象の選び方 ---

// selectJ5AutoTargets は ID を省略した `verify --auto` が対象にする課題の内部整数 ID を、
// 優先度→ID の昇順で返す。対象は状態が検証中で、終了していない run を持たず、上流の状態が
// closed・missing・ポリシーの状態が out_of_policy でない課題。
func selectJ5AutoTargets(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusVerifying)
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

// --- J5 の出力の写像 ---

// applyJ5 は検査を通った出力を、M1 の verify（T8〜T10）へ経路 invoker・原因の run の ID つきで写す。
// 写す直前に読み直した課題が検証中でなければ（遷移表に行が無い）写さず、mapped=false を返す。
func (s *Store) applyJ5(ctx context.Context, runIDDisplay, challengeIDDisplay string, v *j5ValidatedOutput) (mapped bool, err error) {
	var op Operation
	switch v.Verdict {
	case J5VerdictMet:
		op = OpVerifyMet
	case J5VerdictNotMet:
		op = OpVerifyNotMet
	default:
		op = OpVerifyUncertain
	}
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, op, func(ctx context.Context, tc *transitionCtx) error {
		mapped = true
		if v.Verdict == J5VerdictUncertain {
			return insertHold(ctx, tc, *v.Question)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// finalizeJ5Output は run が succeeded のときに、出力を検査し直して課題へ写す。検査に落ちたら
// run の結果を invalid_output にし、課題は変えない。
func (s *Store) finalizeJ5Output(ctx context.Context, runIDDisplay, challengeIDDisplay string, structured []byte) (outcome string, status *string, result RunResult, err error) {
	v, ok := validateJ5Output(structured)
	if !ok {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", nil, "", err
		}
		return "", nil, RunResultInvalidOutput, nil
	}
	mapped, err := s.applyJ5(ctx, runIDDisplay, challengeIDDisplay, v)
	if err != nil {
		return "", nil, "", err
	}
	if !mapped {
		return string(v.Verdict), nil, RunResultSucceeded, nil
	}
	var st string
	switch v.Verdict {
	case J5VerdictMet:
		st = string(StatusAwaitingCompletionApproval)
	case J5VerdictNotMet:
		st = string(StatusInProgress)
	default:
		st = string(StatusAwaitingHuman)
	}
	return string(v.Verdict), &st, RunResultSucceeded, nil
}

// --- `verify --auto` の本体 ---

// J5AutoInput は Store.VerifyAutoJ5 の入力。
type J5AutoInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（状態の条件は適用し、上流の状態に
	// よる除外は適用しない）。nil なら selectJ5AutoTargets が選ぶ対象すべて。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	// Invoker は J5 の起動の実行者。
	Invoker JudgmentInvoker
	// Upstream は J5 の起動の直前の上流の取得（取り込み元の対応の無い課題では呼ばれない）。
	Upstream UpstreamThreadSource
	// Checks は J5 の起動の前の PR のチェックの取得（PR の無い課題では呼ばれない）。
	Checks UpstreamCheckSource
	// CycleID はこの操作が属する周。空は ErrValidation。
	CycleID string
	// Cycle が非 nil なら枠超過の状態をこの JudgmentCycle と共有する。
	Cycle *JudgmentCycle
}

// VerifyAutoJ5 は `flywheel verify --auto [<C-ID>]` の本体（J5）。対象の選び方・CI の待ち・入力・
// 出力の写像は core が持ち、呼び出し元（internal/cli）は宣言の読み込み・invoker と取得の組み立て・
// 周の開始と終了だけを行う。
//
//   - in.ChallengeID が非 nil: 検証中でなければ ErrInvalidTransition、終了していない run があれば
//     ErrRunInProgress、周の上限で起動できなければ ErrBudgetExceeded を返す。CI の待ち・上流の取得の
//     失敗は、課題を変えずに結果の NotStarted へ入れる。
//   - in.ChallengeID が nil: 対象を順に処理し、起動しなかった課題を NotStarted へ入れる。
func (s *Store) VerifyAutoJ5(ctx context.Context, in J5AutoInput) (*JudgmentAutoResult, error) {
	if in.AgentDecl == nil || in.Invoker == nil || in.Upstream == nil || in.Checks == nil || in.CycleID == "" {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		return nil, ErrValidation
	}
	result := &JudgmentAutoResult{}

	if in.ChallengeID != nil {
		ch, err := s.loadChallengeForAuto(ctx, *in.ChallengeID)
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusVerifying {
			return nil, ErrInvalidTransition
		}
		item, notStarted, err := s.verifyOne(ctx, in, nil, *ch)
		if err != nil {
			return nil, err
		}
		appendDelegateOutcome(result, item, notStarted)
		return result, nil
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, err := selectJ5AutoTargets(ctx, tx)
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
		if ch.Status != StatusVerifying {
			continue // 対象を選んだ後に状態が変わった
		}
		item, notStarted, err := s.verifyOne(ctx, in, jc, *ch)
		if err != nil {
			// 1 件の課題の問題（承認済みの計画が無い等）で、他の課題の検証を止めない。
			if errors.Is(err, ErrRunInProgress) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrValidation) {
				continue
			}
			return nil, err
		}
		appendDelegateOutcome(result, item, notStarted)
	}
	return result, nil
}

// verifyOne は課題 1 件の CI の確認と J5 を実行する。jc が非 nil なら枠超過・周の上限を NotStarted で
// 返し、nil（ID を指定した個別の操作）ならエラーで返す。item と notStarted はどちらか一方だけが非 nil
// （エラー時は両方 nil）。
func (s *Store) verifyOne(ctx context.Context, in J5AutoInput, jc *JudgmentCycle, ch Challenge) (*JudgmentAutoItem, *NotStarted, error) {
	if jc != nil && jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}
	cid, _ := parseChallengeID(ch.ID)
	var active bool
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		a, err := challengeHasActiveRun(ctx, tx, cid)
		active = a
		return err
	})); err != nil {
		return nil, nil, err
	}
	if active {
		return nil, nil, ErrRunInProgress // CI の取得や予算の評価より先に（取り違えない）
	}
	vc, err := s.loadJ5Context(ctx, ch)
	if err != nil {
		return nil, nil, err
	}

	// 成果物に PR があれば、J5 の前に CI を確かめる。PR が無ければ CI を調べない。
	prs, err := fetchPullRequestChecks(ctx, in.Checks, vc.Artifacts)
	if err != nil {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}, nil
	}
	if waitURL := pendingPullRequestURL(prs); waitURL != "" {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedWaitingExternal, Detail: waitURL}, nil
	}

	j5Budget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ5)
	if err := s.checkCycleBudget(ctx, in.CycleID, j5Budget); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}
	sections, notStarted := prepareJ5(ctx, in.Upstream, vc, prs)
	if notStarted != nil {
		return nil, notStarted, nil
	}

	planVersion := int64(vc.Plan.Version)
	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ5,
		MaxBudgetUSD: j5Budget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: j5OutputSchema(),
		Invoker:      in.Invoker,
		PlanVersion:  &planVersion,
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
		if res, err = s.RunJudgment(ctx, runIn); err != nil {
			return nil, nil, err
		}
	}
	item := &JudgmentAutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		outcome, status, finalResult, err := s.finalizeJ5Output(ctx, res.RunID, res.ChallengeID, res.StructuredOutput)
		if err != nil {
			return nil, nil, err
		}
		item.Outcome, item.Status, item.Result = outcome, status, finalResult
	}
	return item, nil, nil
}

// --- status.waiting_external ---

// WaitingExternal は CI の完了を待っている検証中の課題 1 件（status.waiting_external.challenges の
// 要素。checks は常に "pending"）。
type WaitingExternal struct {
	ChallengeID string
	PRURL       string
}

// ListWaitingExternal は、状態が検証中で終了していない run を持たず、直前の委譲の成果物の PR の
// チェックが 1 つでも完了していない課題を challenge_id の昇順で返す。チェックは GET だけで取得し、
// 状態・作業ログを変えない。取得に失敗した課題は、待っていると言い切れないため載せない。
func (s *Store) ListWaitingExternal(ctx context.Context, checks UpstreamCheckSource) ([]WaitingExternal, error) {
	out := []WaitingExternal{}
	if checks == nil {
		return out, nil
	}
	var candidates []Challenge
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		all, err := listChallengesInStatuses(ctx, tx, []Status{StatusVerifying})
		if err != nil {
			return err
		}
		for _, ch := range all {
			cid, ok := parseChallengeID(ch.ID)
			if !ok {
				continue
			}
			active, err := challengeHasActiveRun(ctx, tx, cid)
			if err != nil {
				return err
			}
			if !active {
				candidates = append(candidates, ch)
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	for _, ch := range candidates {
		vc, err := s.loadJ5Context(ctx, ch)
		if err != nil {
			if errors.Is(err, ErrValidation) {
				continue
			}
			return nil, err
		}
		prs, err := fetchPullRequestChecks(ctx, checks, vc.Artifacts)
		if err != nil {
			continue
		}
		if waitURL := pendingPullRequestURL(prs); waitURL != "" {
			out = append(out, WaitingExternal{ChallengeID: ch.ID, PRURL: waitURL})
		}
	}
	return out, nil
}
