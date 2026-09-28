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
	if verdict == J1VerdictUncertain && (raw.Question == nil || trimSpaceEmpty(*raw.Question)) {
		return nil, false
	}

	return &j1ValidatedOutput{Verdict: verdict, Priority: priority, Size: size, Reason: raw.Reason, Question: raw.Question}, true
}

// trimSpaceEmpty は s が空白だけ（空文字列を含む）かを返す（strings を
// このためだけに import せず、既存の import 済みパッケージで足りる範囲に
// 収める）。
func trimSpaceEmpty(s string) bool {
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		default:
			return false
		}
	}
	return true
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
	var raw j1RawOutput
	if err := json.Unmarshal([]byte(latest.Output), &raw); err != nil {
		// 記録済みの succeeded な run の output は validateJ1Output を通った
		// ものだけのはずだが、防御的に「除外しない」側へ倒す（fail-open は
		// ここでは「対象に含める」＝人手による classify --priority と同じ
		// 安全側）。
		return nil, nil
	}
	if J1Verdict(raw.Verdict) != J1VerdictNotMine {
		return nil, nil
	}
	if latest.ChallengeVersion != int64(version) {
		return nil, nil
	}
	return &j1NotMineInfo{RunID: latest.RunID, Reason: raw.Reason}, nil
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
// 対応の記録（あれば）・ポジション定義の本文・保留の記録である」）。取り込み
// 元の対応の記録は本チケットの範囲では課題自身の人間記入欄に含めない
// （取り込み元の対応がある課題でも、対応の記録＝source_binding 自体は
// 取り込みの内部状態であり、J1 が読むべき「対応の記録」は課題の記述に
// 現れる内容で足りると判断した。返却の「仕様への指摘」に記す）。
func (s *Store) buildJ1Sections(ctx context.Context, ch Challenge, decl *AgentDeclaration) ([]JudgmentDataSection, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, ErrNotFound
	}

	var holds []Hold
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		hs, err := loadHolds(ctx, tx, cid)
		holds = hs
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

// applyJ1Mine は判定 mine の出力を課題へ写す（M1 T2。§判断点の共通の規則
// 「判断点の出力による変更の作業ログは、経路を invoker・本人確認の方式を
// none・actor を flywheel を起動した OS のログインユーザー名とし、原因の
// run の ID を持つ」）。写す直前に課題の最新の状態を読み直し、遷移元
// （未分類）でなくなっていれば写さない（§アーキテクチャ決定 ③。mapped=false
// を返すだけでエラーにしない: 判断そのものは成功しており、run の記録は既に
// 残っている）。
func (s *Store) applyJ1Mine(ctx context.Context, runIDDisplay, challengeIDDisplay string, priority Priority) (mapped bool, err error) {
	actor, err := resolveActor()
	if err != nil {
		return false, err
	}
	runIDInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return false, ErrValidation
	}
	cid, ok := parseChallengeID(challengeIDDisplay)
	if !ok {
		return false, ErrValidation
	}

	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runIDInt, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return nil
		}
		tr, ok := Lookup(current.Status, OpClassify)
		if !ok {
			return nil
		}
		target, err := tr.Target.Resolve(Table, NoStatus)
		if err != nil {
			return err
		}

		newVersion := current.Version + 1
		nowStr := formatTimestamp(rec.at)
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET status = ?, priority = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			string(target), string(priority), newVersion, nowStr, cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: applyJ1Mine: expected to update 1 row, updated %d", affected)
		}

		before := map[string]any{"status": string(current.Status), "priority": nullablePriority(current.Priority)}
		after := map[string]any{"status": string(target), "priority": string(priority), "version": newVersion}
		if err := rec.record("challenge", cid, string(OpClassify), before, after); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	return mapped, err
}

// applyJ1Uncertain は判定 uncertain の出力を課題へ写す（M1 T11。
// applyJ1Mine と同じ規則・同じ「写せなければ mapped=false」の扱い）。
func (s *Store) applyJ1Uncertain(ctx context.Context, runIDDisplay, challengeIDDisplay, question string) (mapped bool, err error) {
	actor, err := resolveActor()
	if err != nil {
		return false, err
	}
	runIDInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return false, ErrValidation
	}
	cid, ok := parseChallengeID(challengeIDDisplay)
	if !ok {
		return false, ErrValidation
	}

	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runIDInt, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return nil
		}
		tr, ok := Lookup(current.Status, OpHold)
		if !ok {
			return nil
		}
		target, err := tr.Target.Resolve(Table, NoStatus)
		if err != nil {
			return err
		}

		newVersion := current.Version + 1
		nowStr := formatTimestamp(rec.at)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, run_id)
			 VALUES (?, ?, ?, ?, NULL, NULL, NULL, ?)`,
			cid, question, string(current.Status), nowStr, runIDInt,
		); err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET status = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			string(target), newVersion, nowStr, cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: applyJ1Uncertain: expected to update 1 row, updated %d", affected)
		}

		before := map[string]any{"status": string(current.Status)}
		after := map[string]any{"status": string(target), "question": question, "version": newVersion}
		if err := rec.record("challenge", cid, string(OpHold), before, after); err != nil {
			return err
		}
		mapped = true
		return nil
	})
	return mapped, err
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

// J1AutoItem は `classify --auto` が処理した課題 1 件の結果（§IF / API
// 「`--auto` の個別の操作…は cycle の phases の 1 要素と同じ形」の items の
// 要素）。
type J1AutoItem struct {
	ChallengeID string
	RunID       string
	Result      RunResult
	// Outcome は J1 の判定（mine/not_mine/uncertain）。run が succeeded で
	// なかった・core の再検査に落ちた（invalid_output）場合は空文字列。
	Outcome string
	// Status は写した後の課題の状態コード。写さなかった場合は nil。
	Status *string
}

// J1AutoResult は Store.ClassifyAutoJ1 の出力。
type J1AutoResult struct {
	Items      []J1AutoItem
	NotStarted []NotStarted
}

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
