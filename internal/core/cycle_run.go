package core

// このファイルは #86（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §一括の操作（サイクル））が足す、`flywheel cycle` の本体（Store.RunCycle）を持つ。
//
// `cycle` は独自の遷移の規則を持たない（設計書 §7・D11）。各段は個別の操作
// （`ingest`・`classify --auto`・`plan --auto`）と同じ core の処理
// （Store.Ingest・ClassifyAutoJ1・PlanAutoJ2）をそのまま呼び、ここは段の順・
// 排他ロックの取得と解放・heartbeat・枠超過の段をまたぐ伝播だけを持つ。
// S1 の段は取り込み・分類・計画の 3 つ（委譲と検証の段は S2）。

import (
	"context"
)

// CyclePhase は `cycle` の段の名前（§IF / API「cycle の JSON 出力」の phase の閉集合の
// うち S1 で実行する 3 つ。`run`・`verify` は S2）。
type CyclePhase string

// CyclePhase の値（実行する順）。
const (
	CyclePhaseIngest   CyclePhase = "ingest"
	CyclePhaseClassify CyclePhase = "classify"
	CyclePhasePlan     CyclePhase = "plan"
)

// CycleIngestInput は取り込みの段の入力。`.flywheel/sources.json` があるときだけ
// 呼び出し元が作る（CycleRunInput.Ingest が nil なら取り込みの段は skipped）。
type CycleIngestInput struct {
	// Sources は処理する取り込み元（IngestInput.Sources と同じ。検証済みの宣言の
	// 全件）。
	Sources []SourceEntry
	// Upstream は上流（GitHub）の取得の実装。nil は ErrValidation。
	Upstream UpstreamIssueSource
	// Channel は取り込みの作業ログに記録する経路（個別の ingest と同じく、呼び出し元の
	// 入口を表す。CLI は ChannelCLI を渡す）。空は ErrValidation。分類・計画の遷移は
	// 判断点の出力による変更なので、常に経路 invoker で記録される。
	Channel Channel
}

// CycleRunInput は Store.RunCycle の入力。
type CycleRunInput struct {
	// Trigger は周の記録に残す起動の契機（自己申告。`cron`・`manual` 等。空は
	// ErrValidation。既定の `manual` への読み替えは呼び出し元〈CLI〉が行う）。
	Trigger string
	// AgentDecl は `.flywheel/agent.json`（LoadAgentDeclaration の結果）。nil は
	// ErrValidation。position_file の存在検査は呼び出し元が RequirePositionFile で
	// 行っておく（個別の操作と同じ）。
	AgentDecl *AgentDeclaration
	// Ingest が nil なら取り込みの段を skipped にする（`.flywheel/sources.json` が
	// 無い）。
	Ingest *CycleIngestInput
	// ConnDecl が nil なら計画の段を skipped にし、J2 を起動しない
	// （`.flywheel/connectors.json` が無い。エラーにしない）。
	ConnDecl *ConnectorsDeclaration
	// Invoker は判断の呼び出しの実行者。nil は ErrValidation。
	Invoker JudgmentInvoker
	// Upstream は J2 の起動の直前の上流の取得。ConnDecl が非 nil のとき必須
	// （nil は ErrValidation）。
	Upstream UpstreamThreadSource
}

// CyclePhaseResult は 1 つの段の結果（§IF / API「cycle の JSON 出力」の phases の
// 1 要素）。取り込みの段だけが Ingest を持ち、分類・計画の段は Items・NotStarted を
// 持つ。Skipped の段はどれも空。
type CyclePhaseResult struct {
	Phase      CyclePhase
	Skipped    bool
	Ingest     *IngestResult
	Items      []JudgmentAutoItem
	NotStarted []NotStarted
}

// CycleRunResult は Store.RunCycle の出力（`cycle --json` の元になる値）。
type CycleRunResult struct {
	// Cycle は終えた後の周の記録（spent_usd は終了した run の費用の合計）。
	Cycle *Cycle
	// ConfigDefaultsUsed は既定値を使った宣言の名前（S1 は agent.json だけ）。
	// 無ければ空（nil でない）。
	ConfigDefaultsUsed []string
	// Phases は段の結果（実行する順。skipped の段も含める）。
	Phases []CyclePhaseResult
	// RateLimited はこの周で枠超過を記録した run があったか。
	RateLimited bool
}

// RunCycle は `flywheel cycle` の本体（§一括の操作（サイクル））。
//
//  1. サイクルの排他を取って周を開始する（生きている保持者がいれば ErrLocked。
//     何も起動・記録しない）。以後、段の実行中は 60 秒ごとにロックの heartbeat を更新する
//  2. 取り込み（in.Ingest があれば）→ 分類（J1）→ 計画（J2。in.ConnDecl があれば）の順に
//     段を実行する。各段は個別の操作と同じ core の処理を呼び、前の段で状態が進んだ
//     課題は後の段の対象になる。枠超過は段をまたいで伝播する（同じ JudgmentCycle を渡す）
//  3. 周を終えて排他を解放する（段の途中でエラーになった周も解放する。周は aborted）
//
// 周の中の run の失敗（errored 等）は結果の items に示し、エラーにしない（§一括の操作
// 「周の中の run が失敗しても、cycle は終了コード 0 で終わり」）。返すエラーは、排他が
// 取れない（ErrLocked）・宣言や入力の不備・ストアの失敗など、周を続けられないものだけ。
func (s *Store) RunCycle(ctx context.Context, in CycleRunInput) (*CycleRunResult, error) {
	if in.Trigger == "" || in.AgentDecl == nil || in.Invoker == nil {
		return nil, ErrValidation
	}
	if in.ConnDecl != nil && in.Upstream == nil {
		return nil, ErrValidation
	}
	if in.Ingest != nil && (in.Ingest.Upstream == nil || in.Ingest.Channel == "") {
		return nil, ErrValidation
	}
	// 作業ログの actor（OS のログインユーザー名）を解決できない環境は、周を始める前に
	// 拒否する（周の行だけが aborted で残る失敗にしない）。
	if _, err := resolveActor(); err != nil {
		return nil, err
	}

	cyc, err := s.BeginCycle(ctx, BeginCycleInput{
		Trigger:   in.Trigger,
		BudgetUSD: in.AgentDecl.CycleBudgetUSD,
		Exclusive: true,
	})
	if err != nil {
		return nil, err
	}
	stopHeartbeat := s.KeepCycleLock(ctx, cyc.ID)

	jc := NewJudgmentCycle(cyc.ID)
	phases, runErr := s.runCyclePhases(ctx, in, cyc.ID, jc)

	// 周は段の途中で失敗しても、ctx が取り消されていても、必ず終えて排他を解放する
	// （開始した周と排他を残さない）。
	stopHeartbeat()
	endCtx := context.WithoutCancel(ctx)
	endResult := CycleResultCompleted
	if runErr != nil {
		endResult = CycleResultAborted
	}
	ended, endErr := s.EndCycle(endCtx, cyc.ID, endResult)
	if runErr != nil {
		return nil, runErr
	}
	if endErr != nil {
		return nil, endErr
	}

	defaults := []string{}
	if in.AgentDecl.DefaultsUsed {
		defaults = append(defaults, "agent.json")
	}
	return &CycleRunResult{
		Cycle:              ended,
		ConfigDefaultsUsed: defaults,
		Phases:             phases,
		RateLimited:        jc.RateLimited(),
	}, nil
}

// runCyclePhases は取り込み・分類・計画の順に段を実行する。どの段の結果も、途中で
// エラーになるまでの分は返さない（呼び出し元は runErr を返す）。
func (s *Store) runCyclePhases(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle) ([]CyclePhaseResult, error) {
	var phases []CyclePhaseResult

	// 取り込み（M2 の ingest と同じ core の処理。経路は呼び出し元が渡す）。
	ingest := CyclePhaseResult{Phase: CyclePhaseIngest, Skipped: in.Ingest == nil}
	if in.Ingest != nil {
		res, err := s.Ingest(ctx, in.Ingest.Channel, IngestInput{Sources: in.Ingest.Sources, Upstream: in.Ingest.Upstream})
		if err != nil {
			return nil, err
		}
		ingest.Ingest = res
	}
	phases = append(phases, ingest)

	// 分類（J1。classify --auto と同じ処理）。
	j1, err := s.ClassifyAutoJ1(ctx, J1AutoInput{
		AgentDecl: in.AgentDecl,
		Invoker:   in.Invoker,
		CycleID:   cycleID,
		Cycle:     jc,
	})
	if err != nil {
		return nil, err
	}
	phases = append(phases, CyclePhaseResult{Phase: CyclePhaseClassify, Items: j1.Items, NotStarted: j1.NotStarted})

	// 計画（J2。plan --auto と同じ処理。接続ツールの宣言が無ければ skipped）。
	plan := CyclePhaseResult{Phase: CyclePhasePlan, Skipped: in.ConnDecl == nil}
	if in.ConnDecl != nil {
		j2, err := s.PlanAutoJ2(ctx, J2AutoInput{
			AgentDecl: in.AgentDecl,
			ConnDecl:  in.ConnDecl,
			Invoker:   in.Invoker,
			Upstream:  in.Upstream,
			CycleID:   cycleID,
			Cycle:     jc,
		})
		if err != nil {
			return nil, err
		}
		plan.Items = j2.Items
		plan.NotStarted = j2.NotStarted
	}
	phases = append(phases, plan)

	return phases, nil
}
