package core

// このファイルは #84（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J1 分類・§クリティカル設計決定 5〈M3H5〉）が持つ、J1（分類）固有の出力の
// 検査・課題への写像・対象の選び方・`classify --auto` の本体
// （Store.ClassifyAutoJ1）を持つ。判断点に依らない共通の部分は
// judgment_common.go に置く。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// --- J1 の出力スキーマ・検査 ---

// j1RawOutput は J1 の構造化出力（`--json-schema` に渡した形）をそのまま
// 受ける生の形（§IF / API「判断点の出力」J1 の行）。
type j1RawOutput struct {
	Verdict  string  `json:"verdict"`
	Priority *string `json:"priority"`
	Size     *string `json:"size"`
	Reason   string  `json:"reason"`
	Question *string `json:"question"`
}

// j1ValidatedOutput は core の閉集合・必須の組の検査を通った J1 の出力
// （§invoker の共通の規則「判断点の出力に未知の値・閉集合の外の値…があれば
// invalid_output にする」・§J1「判定が mine の出力は、優先度を必須とする」
// 「判定が uncertain の出力は、問いを必須とする」）。
type j1ValidatedOutput struct {
	Verdict  J1Verdict
	Priority *Priority
	Size     *JudgmentSize
	Reason   string
	Question *string
}

// j1OutputSchema は J1 の `--json-schema` に渡すスキーマを返す（§IF / API
// 「判断点の出力」の J1 の行から機械的に組み立てる。閉集合の値は
// core.J1VerdictValues・core.Priority の3値・core.JudgmentSizeValues の
// 定義を直接参照し、手で書き写さない）。
func j1OutputSchema() []byte {
	verdicts := make([]string, 0, len(J1VerdictValues))
	for _, v := range J1VerdictValues {
		verdicts = append(verdicts, string(v))
	}
	priorities := []any{string(PriorityP0), string(PriorityP1), string(PriorityP2), nil}
	sizes := []any{string(JudgmentSizeS), string(JudgmentSizeM), string(JudgmentSizeL), nil}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":  map[string]any{"type": "string", "enum": verdicts},
			"priority": map[string]any{"type": []string{"string", "null"}, "enum": priorities},
			"size":     map[string]any{"type": []string{"string", "null"}, "enum": sizes},
			"reason":   map[string]any{"type": "string"},
			"question": map[string]any{"type": []string{"string", "null"}},
		},
		"required":             []string{"verdict", "reason"},
		"additionalProperties": false,
	}
	// encoding/json は map[string]any のキーを常にアルファベット順で出力する
	// ため、この関数の戻り値は呼び出しのたびに決定的である。エラーは
	// schema が常に json.Marshal 可能な静的なリテラルであるため起こりえない。
	b, _ := json.Marshal(schema)
	return b
}

// validateJ1Output は data（run の StructuredOutput）を検査する
// （§結果の判別「core が閉集合と必須の組…を検査し直す」）。閉集合の外の値・
// 必須の組（mine→priority・uncertain→question）の欠落・JSON として解釈
// できない場合は ok=false を返す。
func validateJ1Output(data []byte) (*j1ValidatedOutput, bool) {
	var raw j1RawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}

	verdict := J1Verdict(raw.Verdict)
	switch verdict {
	case J1VerdictMine, J1VerdictNotMine, J1VerdictUncertain:
	default:
		return nil, false
	}

	var priority *Priority
	if raw.Priority != nil {
		p, ok := ParsePriority(*raw.Priority)
		if !ok {
			return nil, false
		}
		priority = &p
	}

	var size *JudgmentSize
	if raw.Size != nil {
		switch JudgmentSize(*raw.Size) {
		case JudgmentSizeS, JudgmentSizeM, JudgmentSizeL:
			s := JudgmentSize(*raw.Size)
			size = &s
		default:
			return nil, false
		}
	}

	if verdict == J1VerdictMine && priority == nil {
		return nil, false
	}
	if verdict == J1VerdictUncertain && (raw.Question == nil || strings.TrimSpace(*raw.Question) == "") {
		return nil, false
	}

	return &j1ValidatedOutput{Verdict: verdict, Priority: priority, Size: size, Reason: raw.Reason, Question: raw.Question}, true
}

// --- J1 の対象の選び方 ---

// j1NotMineInfo は、対象の課題が直近の J1 の判定で not_mine となり、その後
// 版が変わっていない（＝自動の対象から外れている）ことを表す。
type j1NotMineInfo struct {
	RunID  string
	Reason string
}

// j1NotMineIfCurrent は challengeID・version の課題が §J1「判定が not_mine
// で、その判定をした時点から課題の版が変わっていない課題は、周の中で自動で
// 選ぶ J1 の対象から外す」に当たるかを判定する。当たらなければ nil を返す
// （直近の succeeded な J1 の run が無い・verdict が not_mine でない・版が
// 変わっている、のいずれか）。
func j1NotMineIfCurrent(ctx context.Context, tx *sql.Tx, challengeID int64, version int) (*j1NotMineInfo, error) {
	latest, err := latestSucceededJudgmentOutput(ctx, tx, challengeID, JudgmentJ1)
	if err != nil || latest == nil {
		return nil, err
	}
	// code-reviewer 指摘（round1 CONFIRMED）: RunJudgment ③（succeeded の記録）
	// と finalizeJ1Output の再検査（invalidateRunOutput への書き換え）は別の
	// 書き込みトランザクションのため、その間（または再検査そのものが失敗
	// した場合）は閉集合外の出力が succeeded のまま残りうる。ここでも
	// validateJ1Output を通し、再検査に落ちる出力を not_mine として扱わない
	// （fail-open で「除外しない」側へ倒す。生の json.Unmarshal と verdict の
	// 素通しの一致だけでは、優先度が閉集合外の壊れた mine 出力さえ
	// not_mine と誤認しうる）。
	validated, ok := validateJ1Output([]byte(latest.Output))
	if !ok || validated.Verdict != J1VerdictNotMine {
		return nil, nil
	}
	if latest.ChallengeVersion != int64(version) {
		return nil, nil
	}
	return &j1NotMineInfo{RunID: latest.RunID, Reason: validated.Reason}, nil
}

// selectJ1AutoTargets は ID を省略した `classify --auto`・`cycle` の分類の段が
// 対象にする課題の内部整数 ID を、優先度→ID の昇順で返す（§判断点の共通の
// 規則）。
func selectJ1AutoTargets(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusUnclassified)
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
		notMine, err := j1NotMineIfCurrent(ctx, tx, cid, ch.Version)
		if err != nil {
			return nil, err
		}
		if notMine != nil {
			continue
		}
		out = append(out, cid)
	}
	return out, nil
}

// j1StillEligibleForAuto は、ID を省略した対象選び（selectJ1AutoTargets）が
// cid を選んでから実際に起動するまでの間に、上流・ポリシーの状態や
// not_mine の判定が変わっていないかを、起動の直前に読み直して確かめる
// （`classify --auto` はサイクルの排他ロックを取らないため、並行する別の
// `classify --auto`・`cycle`・取り込みの進行と時間差が生じうる。
// code-reviewer 指摘・round1 PLAUSIBLE）。終了していない run の有無は
// RunJudgment の①トランザクションが最終防衛として検査するためここでは
// 見ない（二重化しない）。
func (s *Store) j1StillEligibleForAuto(ctx context.Context, cid int64, version int) (bool, error) {
	var eligible bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		excluded, err := challengeAutoExcludedByPolicy(ctx, tx, cid)
		if err != nil {
			return err
		}
		if excluded {
			eligible = false
			return nil
		}
		notMine, err := j1NotMineIfCurrent(ctx, tx, cid, version)
		if err != nil {
			return err
		}
		eligible = notMine == nil
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return eligible, nil
}

// TriageItem は `status` の `needs_human.triage` の 1 件（§IF / API
// 「status.needs_human.triage（S1）: [{"challenge_id", "run_id",
// "reason"}]」）。
type TriageItem struct {
	ChallengeID string
	RunID       string
	Reason      string
}

// listJ1Triage は §J1「判定が not_mine で自動の対象から外れている課題は、
// status の needs_human.triage に、理由と run の ID つきで出る」の一覧を
// challenge_id の昇順で返す（loadChallengesByStatusSorted は未分類の課題の
// 優先度が常に未設定であることから、結果として ID 昇順と一致する）。
func listJ1Triage(ctx context.Context, tx *sql.Tx) ([]TriageItem, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusUnclassified)
	if err != nil {
		return nil, err
	}
	out := make([]TriageItem, 0)
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		info, err := j1NotMineIfCurrent(ctx, tx, cid, ch.Version)
		if err != nil {
			return nil, err
		}
		if info == nil {
			continue
		}
		out = append(out, TriageItem{ChallengeID: ch.ID, RunID: info.RunID, Reason: info.Reason})
	}
	return out, nil
}

// --- J1 の入力の組み立て ---

// buildJ1Sections は ch・decl から J1 の標準入力のデータの区画の一覧を組み立
// てる（§J1「J1 の入力は、課題（人間記入欄・緊急度・起票者）・取り込み元の
// 対応の記録（あれば）・ポジション定義の本文・保留の記録である」）。
// 取り込み元の対応の記録（URL・外部キー・上流の状態・ポリシーの状態）は
// source_binding が無い課題（`create` で作った課題）では省く
// （design-reviewer 指摘・round1 CONFIRMED: 当初は課題の記述で足りると
// 判断していたが、取り込んだ課題の description に対応の記録が必ず現れる
// とは限らず、受入基準を満たしていなかった）。
func (s *Store) buildJ1Sections(ctx context.Context, ch Challenge, decl *AgentDeclaration) ([]JudgmentDataSection, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, ErrNotFound
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
		return nil, err
	}

	posPath := filepath.Join(s.workspace, decl.PositionFile)
	posContent, err := os.ReadFile(posPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read position_file %q: %w", ErrConfigInvalid, decl.PositionFile, err)
	}

	urgency := ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	issueBody := fmt.Sprintf(
		"タイトル: %s\n\n説明:\n%s\n\n達成条件:\n%s\n\n緊急度: %s\n起票者: %s",
		ch.Title, ch.Description, ch.DoneCriteria, urgency, ch.Reporter,
	)

	sections := []JudgmentDataSection{
		{Label: "課題", Content: issueBody},
		{Label: "ポジション定義", Content: string(posContent)},
	}
	// design-reviewer 指摘（round1 CONFIRMED）: §J1「J1 の入力は…取り込み元の
	// 対応の記録（あれば）…である」を満たすため、対応（source_binding）が
	// あれば区画を足す。無い課題（`create` で作った課題）は区画を作らない。
	if sourceText := formatSourceBindingSection(sb); sourceText != "" {
		sections = append(sections, JudgmentDataSection{Label: "取り込み元の対応", Content: sourceText})
	}
	if holdsText := formatHoldsSection(holds); holdsText != "" {
		sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: holdsText})
	}
	return sections, nil
}

// --- J1 の出力の写像 ---

// invalidateRunOutput は runIDDisplay（"R-<n>"）の run の結果を
// invalid_output へ更新する（§結果の判別「structured_output が無い、または
// その run の出力スキーマと core の閉集合の検査に合わない…invalid_output」の
// 後半: invoker は structured_output の有無と JSON としての形だけを見て
// succeeded と判別するため、core の閉集合・必須の組の再検査に落ちた場合は
// ここで結果を上書きする。§機能全体の設計「① 書き込みトランザクションで…
// run を記録する」の run と同じ行を更新するだけで、課題の状態・版は変えない
// 〈決定済みの設計〉）。
func (s *Store) invalidateRunOutput(ctx context.Context, runIDDisplay string) error {
	id, ok := parseRunID(runIDDisplay)
	if !ok {
		return ErrValidation
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE run SET result = ? WHERE id = ?`, string(runResultInvalidOutput), id)
		return err
	})
	return classifyReadWriteErr(err)
}

// errJ1MappingNotEligible は、写す直前に読み直した課題が J1 の遷移元
// （未分類）でなくなっていたことを表す内部の sentinel（applyJ1Uncertain
// だけが使う。§アーキテクチャ決定③「読み直した状態が遷移元でなくなって
// いれば写さない」）。ErrInvalidTransition・ErrTerminalState と同じく
// mapped=false（エラーではない）として扱う。
var errJ1MappingNotEligible = errors.New("core: j1 mapping is no longer eligible (challenge is not unclassified anymore)")

// applyJ1Mine は判定 mine の出力を課題へ写す（M1 T2。ClassifyChallenge と
// 同じ遷移を、経路 invoker・本人確認 none・原因の run の ID つきで実行する
// （transitionAsInvoker。§機能全体の設計「判断点の出力を core の遷移へ写す
// ときは、M1 の core の公開 API…と同じ遷移を呼ぶ」・self-review 指摘: 以前は
// runTransition の判定順序〈終端検査・Lookup・楽観的 UPDATE・作業ログの
// 記録〉をこの関数と applyJ1Uncertain がそれぞれ複製しており、
// RequiresVerification の不変条件検査を経由しなかった）。T2 の遷移表は
// From: 未分類の1行しか持たないため、Lookup(current.Status, OpClassify) の
// 成功自体が「読み直した状態が未分類である」ことを保証する（§アーキテクチャ
// 決定③はこの Lookup の失敗〈ErrInvalidTransition〉で満たされる）。
func (s *Store) applyJ1Mine(ctx context.Context, runIDDisplay, challengeIDDisplay string, priority Priority) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpClassify, func(_ context.Context, tc *transitionCtx) error {
		tc.before["priority"] = nullablePriority(tc.current.Priority)
		tc.after["priority"] = nullablePriority(&priority)
		tc.priority = &priority
		mapped = true
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

// applyJ1Uncertain は判定 uncertain の出力を課題へ写す（M1 T11。
// HoldChallenge と同じ遷移を transitionAsInvoker 経由で実行する）。T11 の
// 遷移表は未分類・分類済・着手中・検証中の4行を持つため、
// Lookup(current.Status, OpHold) の成功だけでは「読み直した状態が未分類で
// なくなっていれば写さない」を保証できない（J1 の対象は常に未分類の課題
// だけであり、他の3状態からの人間対応待ちへの遷移は M1 の hold・verify が
// 別に担う）。そのため、写す直前に読み直した状態を明示的に未分類と照合する
// （code-reviewer 指摘・round1 CONFIRMED: この照合が無いと、J1 の実行中に
// 人間が別の操作でこの課題を分類済・着手中・検証中へ進めていても、
// uncertain の出力がその課題を人間対応待ちへ進め、from_status の食い違う
// 保留を作ってしまっていた）。
func (s *Store) applyJ1Uncertain(ctx context.Context, runIDDisplay, challengeIDDisplay, question string) (mapped bool, err error) {
	_, err = s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusUnclassified {
			return errJ1MappingNotEligible
		}
		if err := insertHold(ctx, tc, question); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errJ1MappingNotEligible) {
			return false, nil
		}
		return false, err
	}
	return mapped, nil
}

// finalizeJ1Output は run が succeeded の場合に、core の再検査（閉集合・
// 必須の組）→写像（mine/uncertain/not_mine）を行う（§結果の判別「core が
// 閉集合と必須の組…を検査し直す」・§クリティカル設計決定「invalid_output の
// 写し方」: 再検査に落ちたら run の結果を invalid_output へ更新し、課題は
// 変えない）。outcome は判定の文字列（invalid_output に落ちた場合は空文字列）、
// status は写した後の課題の状態コード（写さなかった／変えなかった場合は
// nil）、result は最終的な run の結果（再検査に落ちれば invalid_output、
// それ以外は succeeded のまま）。呼び出し元（buildJ1Item）はこの result を
// 表示に使う（RunJudgment が返した時点の succeeded をそのまま使うと、ここで
// invalid_output へ更新した事実が J1AutoItem に反映されない）。
func (s *Store) finalizeJ1Output(ctx context.Context, runIDDisplay, challengeIDDisplay string, structuredOutput []byte) (outcome string, status *string, result RunResult, err error) {
	validated, ok := validateJ1Output(structuredOutput)
	if !ok {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", nil, "", err
		}
		return "", nil, RunResultInvalidOutput, nil
	}

	switch validated.Verdict {
	case J1VerdictMine:
		mapped, err := s.applyJ1Mine(ctx, runIDDisplay, challengeIDDisplay, *validated.Priority)
		if err != nil {
			return "", nil, "", err
		}
		if mapped {
			st := string(StatusClassified)
			return string(J1VerdictMine), &st, RunResultSucceeded, nil
		}
		return string(J1VerdictMine), nil, RunResultSucceeded, nil
	case J1VerdictUncertain:
		mapped, err := s.applyJ1Uncertain(ctx, runIDDisplay, challengeIDDisplay, *validated.Question)
		if err != nil {
			return "", nil, "", err
		}
		if mapped {
			st := string(StatusAwaitingHuman)
			return string(J1VerdictUncertain), &st, RunResultSucceeded, nil
		}
		return string(J1VerdictUncertain), nil, RunResultSucceeded, nil
	case J1VerdictNotMine:
		return string(J1VerdictNotMine), nil, RunResultSucceeded, nil
	default:
		return "", nil, RunResultSucceeded, nil
	}
}

// --- `classify --auto` の本体 ---

// J1AutoInput は Store.ClassifyAutoJ1 の入力。
type J1AutoInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（§判断点の
	// 共通の規則「課題の ID を指定した個別の操作は、上の除外を適用しない」）。
	// nil なら、selectJ1AutoTargets が選ぶ対象すべてを処理する。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	Invoker     JudgmentInvoker
	// CycleID はこの操作が属する周（`BeginCycle` が返した ID）。空文字列は
	// ErrValidation。
	CycleID string
}

// J1AutoItem は `classify --auto` が処理した課題 1 件の結果（judgment_batch.go の
// JudgmentAutoItem。`plan --auto` の J2AutoItem と同じ形なので 1 つの型を共有する）。
type J1AutoItem = JudgmentAutoItem

// J1AutoResult は Store.ClassifyAutoJ1 の出力（JudgmentAutoResult）。
type J1AutoResult = JudgmentAutoResult

// ClassifyAutoJ1 は `flywheel classify --auto [<C-ID>]` の本体。対象の選び方・
// 起動・出力の写像は core が持ち、呼び出し元（internal/cli）は宣言の読み込み・
// invoker の組み立て・周の開始と終了だけを行う（P2）。
//
//   - in.ChallengeID が非 nil（ID を指定した個別の操作）: その課題が
//     未分類でなければ ErrInvalidTransition（§判断点の共通の規則「判断の
//     呼び出しの対象にするのは、状態がその判断点の遷移元で…課題に限る」は
//     ID 指定でも適用する）。周の上限で起動できなければ ErrBudgetExceeded、
//     終了していない run があれば ErrRunInProgress をそのまま返す（CLI が
//     終了コード 1 の budget_exceeded・run_in_progress へ写す）。
//   - in.ChallengeID が nil（自動の対象）: selectJ1AutoTargets が選んだ対象を
//     順に処理する。枠超過・周の上限で起動できなかった課題は NotStarted へ、
//     並行して他から起動された課題（ErrRunInProgress）は静かにスキップする
//     （終了コードを持つ個別の呼び出しではないため）。
func (s *Store) ClassifyAutoJ1(ctx context.Context, in J1AutoInput) (*J1AutoResult, error) {
	if in.AgentDecl == nil || in.Invoker == nil || in.CycleID == "" {
		return nil, ErrValidation
	}

	result := &J1AutoResult{}
	maxBudget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ1)
	timeoutSec := in.AgentDecl.TimeoutSec.Judgment

	if in.ChallengeID != nil {
		ch, err := s.loadChallengeForAuto(ctx, *in.ChallengeID)
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusUnclassified {
			return nil, ErrInvalidTransition
		}
		sections, err := s.buildJ1Sections(ctx, *ch, in.AgentDecl)
		if err != nil {
			return nil, err
		}
		cycleID := in.CycleID
		res, err := s.RunJudgment(ctx, RunJudgmentInput{
			ChallengeID:  ch.ID,
			Judgment:     JudgmentJ1,
			MaxBudgetUSD: maxBudget,
			TimeoutSec:   timeoutSec,
			Sections:     sections,
			OutputSchema: j1OutputSchema(),
			Invoker:      in.Invoker,
			CycleID:      &cycleID,
		})
		if err != nil {
			return nil, err
		}
		item, err := s.buildJ1Item(ctx, res)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
		return result, nil
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, err := selectJ1AutoTargets(ctx, tx)
		targetIDs = ids
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}

	jc := NewJudgmentCycle(in.CycleID)
	for _, cid := range targetIDs {
		ch, err := s.loadChallengeForAuto(ctx, formatChallengeID(cid))
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusUnclassified {
			// 同じ周の中で並行して状態が変わった（例: 人間が classify
			// --priority で先に分類した）。静かに読み飛ばす。
			continue
		}
		// code-reviewer 指摘（round1 PLAUSIBLE）: selectJ1AutoTargets が対象を
		// 選んだ時点と、実際にこの課題を起動する時点の間に、並行する別の
		// `classify --auto`・取り込みが進みうる（このコマンド自身は
		// サイクルの排他ロックを取らない＝§サイクルの排他「個別の操作は
		// サイクルのロックを取らない」）。除外条件（上流の状態・ポリシーの
		// 状態・not_mine）を起動の直前にもう一度確かめる。
		eligible, err := s.j1StillEligibleForAuto(ctx, cid, ch.Version)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}
		sections, err := s.buildJ1Sections(ctx, *ch, in.AgentDecl)
		if err != nil {
			return nil, err
		}
		res, notStarted, err := jc.RunJudgment(ctx, s, RunJudgmentInput{
			ChallengeID:  ch.ID,
			Judgment:     JudgmentJ1,
			MaxBudgetUSD: maxBudget,
			TimeoutSec:   timeoutSec,
			Sections:     sections,
			OutputSchema: j1OutputSchema(),
			Invoker:      in.Invoker,
		})
		if err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue
			}
			return nil, err
		}
		if notStarted != nil {
			result.NotStarted = append(result.NotStarted, *notStarted)
			continue
		}
		item, err := s.buildJ1Item(ctx, res)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

// loadChallengeForAuto は id（"C-<n>"）の課題を読む（ErrNotFound は
// parseChallengeID・loadChallenge のどちらの理由でも同じ値になる）。
func (s *Store) loadChallengeForAuto(ctx context.Context, id string) (*Challenge, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var ch *Challenge
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		ch = c
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return ch, nil
}

// buildJ1Item は RunJudgment の結果から J1AutoItem を組み立てる。succeeded
// なら finalizeJ1Output で写す。
func (s *Store) buildJ1Item(ctx context.Context, res *RunJudgmentResult) (J1AutoItem, error) {
	item := J1AutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		outcome, status, finalResult, err := s.finalizeJ1Output(ctx, res.RunID, res.ChallengeID, res.StructuredOutput)
		if err != nil {
			return J1AutoItem{}, err
		}
		item.Outcome = outcome
		item.Status = status
		item.Result = finalResult
	}
	return item, nil
}
