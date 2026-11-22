package core

// このファイルは #86（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §一括の操作（サイクル））が足す、`flywheel cycle` の本体（Store.RunCycle）を持つ。
//
// `cycle` は独自の遷移の規則を持たない（設計書 §7・D11）。各段は個別の操作
// （`ingest`・`classify --auto`・`plan --auto`）と同じ core の処理
// （Store.Ingest・ClassifyAutoJ1・PlanAutoJ2）をそのまま呼び、ここは段の順・
// 排他ロックの取得と解放・heartbeat・枠超過の段をまたぐ伝播だけを持つ。
// 段は 実行（release）→ 取り込み → 分類（J1）→ 計画（J2）→ 委譲（run）→ 検証（verify・J5）→ 学習（learn・J6）→ 改修提案（improve・J8）→ backup の順で、
// 委譲と検証の段は接続ツールの宣言（connectors.json）があるときだけ実行する。

import (
	"context"
	"errors"
	"time"
)

// CyclePhase は `cycle` の段の名前（§IF / API「cycle の JSON 出力」の phase の閉集合の
// うち S1 で実行する 3 つ。`run`・`verify` は S2）。
type CyclePhase string

// CyclePhase の値（実行する順）。
const (
	// CyclePhaseRelease は承認済みの release の実行の段（ExecuteReleases と同じ処理。最初の段）。
	CyclePhaseRelease  CyclePhase = "release"
	CyclePhaseIngest   CyclePhase = "ingest"
	CyclePhaseClassify CyclePhase = "classify"
	CyclePhasePlan     CyclePhase = "plan"
	// CyclePhaseRun は委譲の段（RunDelegation と同じ処理）。
	CyclePhaseRun CyclePhase = "run"
	// CyclePhaseVerify は検証の段（VerifyAutoJ5 と同じ処理）。
	CyclePhaseVerify CyclePhase = "verify"
	// CyclePhaseLearn は学習の段（LearnJ6 と同じ処理。improve の直前の段。接続ツールの宣言を使わないので skipped にならない）。
	CyclePhaseLearn CyclePhase = "learn"
	// CyclePhaseImprove は改修提案の段（ImproveJ8 と同じ処理。8 段目で最後の段。接続ツールの宣言を使わないので skipped にならない）。
	CyclePhaseImprove CyclePhase = "improve"
	// CyclePhaseBackup は書き出しと記憶のコミットの段（BackupWorkspace。9 段目で最後の段。skipped にならない）。
	CyclePhaseBackup CyclePhase = "backup"
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
	// Upstream は J2・J3・J5 の起動の直前の上流の取得。ConnDecl が非 nil のとき必須
	// （nil は ErrValidation）。
	Upstream UpstreamThreadSource
	// Delegate は委譲の起動の実行者。ConnDecl が非 nil のとき必須（nil は ErrValidation）。
	Delegate DelegationInvoker
	// Git はスロットの作業ツリーの検査と払い出しの口。ConnDecl が非 nil のとき必須。
	Git SlotGit
	// Reconcile は委譲の後の照合が呼ぶ、リモートのブランチと PR の取得（GET だけ）。
	// ConnDecl が非 nil のとき必須。
	Reconcile UpstreamBranchSource
	// Checks は J5 の起動の前の PR のチェックの取得（GET だけ）。ConnDecl が非 nil のとき必須。
	Checks UpstreamCheckSource
	// Diffs は J5 の起動の前の PR の差分の取得（GET だけ）。ConnDecl が非 nil のとき必須。
	Diffs UpstreamDiffSource
	// Descriptions は J5 の起動の前の成果物の説明文の取得（読み取りだけ。nil なら説明文の区画を入れない）。
	Descriptions UpstreamDescriptionSource
	// HeadGit は J5 が PR の head を読む一時の worktree の口。ConnDecl が非 nil のとき必須。
	HeadGit HeadWorktreeGit
	// Compare は release の段が、完了の課題の承認待ちの release の PR に検証の後の変更が入ったかを判定する比較の取得
	// （GET だけ）。nil なら、その判定をしない。
	Compare UpstreamCompareSource
	// Contract は form: cli の接続ツールの契約のコマンドの起動（DelegateInput.Contract と同じ）。
	Contract ContractClient
	// WorkspaceGit は backup の段が使うワークスペースの Git の口（必須。nil は ErrValidation）。
	WorkspaceGit WorkspaceGit
	// Wait は回りの間に置く待ち（nil なら実時間の待ち）。引数は宣言の cycle_poll_interval_sec の長さ。試験が差し替えて、
	// 待った回数と長さを確かめる。待ちの前後でロックの heartbeat を更新する（待ちの間は、別の goroutine も更新し続ける）。
	Wait func(ctx context.Context, d time.Duration) error
	// OnBegin が非 nil なら、周の開始（BeginCycle の成功）の直後、段を始める前に周の ID で呼ぶ。
	OnBegin func(cycleID string)
	// Continue が非 nil なら、終わった理由が settled のサイクルを終えて排他を放した後に、宣言の auto_cycle が真で、いま始められるもの
	// （CountRoundWork。回りの終わりの数えと同じ）が 1 件以上あるとき、1 回だけ呼ぶ（次のサイクルを切り離して始める処理。trigger は continue）。
	// settled 以外の終わり方では呼ばない。数えは必ず排他を放した後に行う（放す前に数えると、数えた後・放す前に記録された操作をどちらも拾わない）。
	Continue func(ctx context.Context) error
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
	// ReleaseItems は release の段の items（他の段は空）。release の段の not_started は NotStarted に入る。
	ReleaseItems []ReleaseExecItem
	// ReleaseTidied は release の段の、承認待ちの release の片付け（マージ済みを閉じた・立て直しのため課題を戻した）の結果。
	ReleaseTidied []ReleaseTidyItem
	// ImproveItems は improve の段の items（J8 を呼んだ run ごとに 1 件。他の段は空）。
	// improve の段の not_started は NotStarted に入る（課題の ID を持たない）。
	ImproveItems []ImproveItem
	// Backup は backup の段の結果（他の段は nil）。
	Backup *BackupResult
}

// ImproveItem は improve の段と `flywheel improve` の items の要素（J8 の run 1 回分）。
type ImproveItem struct {
	// Created は起票した改修課題の ID、Linked は重複の抑えで紐づけた既存の改修課題の ID。
	Created []string
	Linked  []string
	Result  RunResult
	RunID   string
}

// ImproveItems は ImproveJ8 の結果を段の items へ写す（J8 を呼ばなかったなら空）。
func (r *ImproveJ8Result) ImproveItems() []ImproveItem {
	items := []ImproveItem{}
	if r.Called {
		items = append(items, ImproveItem{
			Created: append([]string{}, r.Filed...),
			Linked:  append([]string{}, r.Linked...),
			Result:  r.Result,
			RunID:   r.RunID,
		})
	}
	return items
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
	// Continued は、次のサイクルを始める処理（CycleRunInput.Continue）を呼んだか。ContinueErr はその処理のエラー（周の成否は変えない）。
	Continued   bool
	ContinueErr error
}

// cycleStalledRounds は、作業中の実行が無いまま進みの無い回りがこの回数続いたら、回りの繰り返しを終える（stalled）。
const cycleStalledRounds = 3

// RunCycle は `flywheel cycle` の本体（§一括の操作（サイクル））。
//
//  1. サイクルの排他を取って周を開始する（生きている保持者がいれば ErrLocked。
//     何も起動・記録しない）。以後、段の実行中は 60 秒ごとにロックの heartbeat を更新する
//  2. 回りを、進められるものが無くなるまで繰り返す（回り = 承認済みの release の実行〔in.ConnDecl があれば〕→ 取り込み〔in.Ingest があれば〕→
//     分類（J1）→ 計画（J2）→ 委譲 → 検証（J5）を 1 回ずつ。計画・委譲・検証は in.ConnDecl があるときだけ）。回りの終わりの数え・進みの判定・間隔の待ち・
//     歯止めは runRounds（cycle_rounds.go）。終わりの段（学習〔J6〕→ 改修提案〔J8〕）は、最後の回りの後に 1 回だけ通す。検証の段は、委譲の段が
//     起動した委譲の run がすべて終わってから始まる（RunDelegation は合流して返る）。各段は個別の操作と同じ core の処理を呼び、前の段で状態が進んだ
//     課題は後の段の対象になる。枠超過は段・回りをまたいで伝播する（同じ JudgmentCycle を渡す）
//  3. backup の段（書き出しと記憶のコミット。ほかの段のエラー・打ち切り・ctx の取り消しがあっても実行する。
//     failed は周の成否を変えない）の後、周を終えて排他を解放する（段の途中でエラーになった周も解放する。周は aborted）
//
// 周の中の run の失敗（errored 等）は結果の items に示し、エラーにしない（§一括の操作
// 「周の中の run が失敗しても、cycle は終了コード 0 で終わり」）。返すエラーは、排他が
// 取れない（ErrLocked）・宣言や入力の不備・ストアの失敗など、周を続けられないものだけ。
func (s *Store) RunCycle(ctx context.Context, in CycleRunInput) (*CycleRunResult, error) {
	if in.Trigger == "" || in.AgentDecl == nil || in.Invoker == nil || in.WorkspaceGit == nil {
		return nil, ErrValidation
	}
	if in.ConnDecl != nil && (in.Upstream == nil || in.Delegate == nil || in.Git == nil || in.Reconcile == nil || in.Checks == nil || in.Diffs == nil || in.HeadGit == nil) {
		return nil, ErrValidation
	}
	if in.Ingest != nil && (in.Ingest.Upstream == nil || in.Ingest.Channel == "") {
		return nil, ErrValidation
	}
	// 作業ログの actor（OS のログインユーザー名）を解決できない環境は、周を始める前に
	// 拒否する（周の行だけが aborted で残る失敗にしない）。
	if _, err := s.resolveActor(); err != nil {
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
	if in.OnBegin != nil {
		in.OnBegin(cyc.ID)
	}

	// 費用の集計（周の ID で束ねる）と利用上限の印は、サイクルを通して 1 つの JudgmentCycle で持つ。
	jc := NewJudgmentCycle(cyc.ID)
	phases, endReason, runErr := s.runCyclePhases(ctx, in, cyc.ID, jc)
	if runErr != nil {
		endReason = CycleEndError
		if ctx.Err() != nil {
			endReason = CycleEndInterrupted
		}
	}

	// 周は段の途中で失敗しても、ctx が取り消されていても、必ず終えて排他を解放する
	// （開始した周と排他を残さない）。backup も同じで、ほかの段の成否にかかわらず周を閉じる前に実行する。
	// ctx が取り消されていても実行する【仮定】。backup の failed は周の成否・終了コードを変えない。
	endCtx := context.WithoutCancel(ctx)
	backup := s.BackupWorkspace(endCtx, in.WorkspaceGit, in.AgentDecl, cyc.ID)
	// 作業ログの記録だけが失敗しても周は止めない（phases を失わない）。detail に残して警告にする。
	if recordErr := s.recordBackup(endCtx, cyc.ID, backup); recordErr != nil {
		note := "backup was not recorded in the activity log: " + recordErr.Error()
		if backup.Detail != "" {
			note = backup.Detail + "; " + note
		}
		backup.Detail = note
	}
	phases = append(phases, CyclePhaseResult{Phase: CyclePhaseBackup, Backup: &backup})
	stopHeartbeat()
	endResult := CycleResultCompleted
	if runErr != nil {
		endResult = CycleResultAborted
		if endReason == CycleEndInterrupted {
			endResult = CycleResultInterrupted
		}
	}
	ended, endErr := s.EndCycleWithReason(endCtx, cyc.ID, endResult, endReason)
	if err := errors.Join(runErr, endErr); err != nil {
		if runErr != nil {
			// PartialReleaseError など runErr の型を保つ。
			return nil, runErr
		}
		return nil, err
	}

	defaults := []string{}
	if in.AgentDecl.DefaultsUsed {
		defaults = append(defaults, "agent.json")
	}
	res := &CycleRunResult{
		Cycle:              ended,
		ConfigDefaultsUsed: defaults,
		Phases:             phases,
		RateLimited:        jc.RateLimited(),
	}
	// 終わりの段を通し、排他を放した後に、いま始められるものを数え直す（settled のときだけ）。
	if in.Continue != nil && endReason == CycleEndSettled && in.AgentDecl.AutoCycle {
		work, werr := s.CountRoundWork(endCtx, in.AgentDecl, in.ConnDecl)
		switch {
		case werr != nil:
			res.ContinueErr = werr
		case work.Startable > 0:
			res.Continued = true
			res.ContinueErr = in.Continue(endCtx)
		}
	}
	return res, nil
}

// runCyclePhases は回りを、進められるものが無くなるまで繰り返し（進める段 = 反映・取り込み・分類・計画・委譲・完了の検証）、
// 最後に終わりの段（学び・改修の提案）を 1 回だけ通す。段ごとの結果は、回りをまたいで足し合わせる（対象の一覧と起動しなかった
// ものの一覧は連結し、1 つだけの値は最後の回りのもの）。終わった理由（settled・stalled・rate_limited・budget・stopped）も返す。終わりの段（学び・改修の提案）は settled・budget・stalled のときだけ通す（rate_limited・stopped は通さない）。
// どの段の結果も、途中でエラーになるまでの分は返さない（呼び出し元は runErr を返す）。ただし release の段で実行済みの結果は PartialReleaseError で運ぶ。
func (s *Store) runCyclePhases(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle) (_ []CyclePhaseResult, _ CycleEndReason, retErr error) {
	// release の段で実行済みのものがあれば、後続の段のエラーでも失わないよう PartialReleaseError で運ぶ。
	var executed []ReleaseExecItem
	defer func() {
		var pe *PartialReleaseError
		if retErr != nil && len(executed) > 0 && !errors.As(retErr, &pe) {
			retErr = &PartialReleaseError{Err: retErr, Items: executed}
		}
	}()

	var phases []CyclePhaseResult
	reason, err := s.runRounds(ctx, in, cycleID, jc, &phases, &executed)
	if err != nil {
		return nil, "", err
	}

	// 終わりの段は、settled・budget・stalled だけが通す。rate_limited（同じ利用上限に当たるため判断点を呼ばない）と
	// stopped は書き出しだけを通す。
	if reason == CycleEndRateLimited || reason == CycleEndStopped {
		return phases, reason, nil
	}

	// 学習（learn と同じ処理。J6 は接続ツールの宣言を使わないので常に実行する）。
	j6, err := s.LearnJ6(ctx, J6AutoInput{
		AgentDecl: in.AgentDecl,
		Invoker:   in.Invoker,
		CycleID:   cycleID,
		Cycle:     jc,
	})
	if err != nil {
		return nil, "", err
	}
	phases = append(phases, CyclePhaseResult{Phase: CyclePhaseLearn, Items: j6.Items, NotStarted: j6.NotStarted})

	// 改修提案（improve と同じ処理。J8 は接続ツールの宣言が無くても呼ぶ。起動の件数がしきい値に
	// 達したときだけ呼び、J8 の連続失敗が上限に達していれば呼ばない）。
	j8, err := s.ImproveJ8(ctx, ImproveJ8Input{
		AgentDecl: in.AgentDecl,
		ConnDecl:  in.ConnDecl,
		Invoker:   in.Invoker,
		SlotGit:   in.Git,
		CycleID:   cycleID,
		Cycle:     jc,
	})
	if err != nil {
		return nil, "", err
	}
	phases = append(phases, CyclePhaseResult{Phase: CyclePhaseImprove, ImproveItems: j8.ImproveItems(), NotStarted: j8.NotStarted})

	return phases, reason, nil
}

// runAdvancePhases は進める段（反映・取り込み・分類・計画・委譲・完了の検証）を、この順に 1 回ずつ通す（1 つの回り）。
// 段の中身は、S3d までの 1 回のサイクルと同じ。executed には、release の段で実行済みの項目を足していく。
func (s *Store) runAdvancePhases(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle, executed *[]ReleaseExecItem) ([]CyclePhaseResult, error) {
	var phases []CyclePhaseResult
	// boundary は段の切れ目で止める指示を読み、読んだら errCycleStopRequested を返す。
	boundary := func() error {
		if stop, err := s.cycleStopRequested(ctx, cycleID); err != nil {
			return err
		} else if stop {
			return errCycleStopRequested
		}
		return nil
	}

	// 段の切れ目で止める指示を読む（段の途中では読まない）。読んだら、通した段までの結果を返して新しい段へ進まない。
	if err := boundary(); err != nil {
		return phases, err
	}

	// 承認済みの release の実行（op exec と同じ core の処理。最初の段。接続ツールの宣言が無ければ skipped）。
	release := CyclePhaseResult{Phase: CyclePhaseRelease, Skipped: advancePhaseSkipped(in, CyclePhaseRelease), ReleaseItems: []ReleaseExecItem{}}
	if in.ConnDecl != nil {
		res, err := s.ExecuteReleases(ctx, nil, ReleaseExecOptions{
			Checks:     in.Checks,
			Compare:    in.Compare,
			Connectors: in.ConnDecl,
			Agent:      in.AgentDecl,
			Contract:   in.Contract,
			Trigger:    ReleaseExecTriggerCycle,
			CycleID:    cycleID,
		})
		if err != nil {
			if res != nil && len(res.Items) > 0 {
				return nil, &PartialReleaseError{Err: err, Items: append(append([]ReleaseExecItem{}, *executed...), res.Items...)}
			}
			return nil, err
		}
		release.ReleaseItems = res.Items
		release.ReleaseTidied = res.Tidied
		release.NotStarted = res.NotStarted
	}
	*executed = append(*executed, release.ReleaseItems...)
	phases = append(phases, release)

	if err := boundary(); err != nil {
		return phases, err
	}

	// 取り込み（M2 の ingest と同じ core の処理。経路は呼び出し元が渡す）。
	ingest := CyclePhaseResult{Phase: CyclePhaseIngest, Skipped: advancePhaseSkipped(in, CyclePhaseIngest)}
	if in.Ingest != nil {
		res, err := s.Ingest(ctx, in.Ingest.Channel, IngestInput{Sources: in.Ingest.Sources, Upstream: in.Ingest.Upstream})
		if err != nil {
			return nil, err
		}
		ingest.Ingest = res
	}
	phases = append(phases, ingest)

	if err := boundary(); err != nil {
		return phases, err
	}

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

	if err := boundary(); err != nil {
		return phases, err
	}

	// 計画（J2。plan --auto と同じ処理。接続ツールの宣言が無ければ skipped）。
	plan := CyclePhaseResult{Phase: CyclePhasePlan, Skipped: advancePhaseSkipped(in, CyclePhasePlan)}
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

	if err := boundary(); err != nil {
		return phases, err
	}

	// 委譲（flywheel run と同じ処理。着手中で承認済みの計画を持つ課題が対象）。
	run := CyclePhaseResult{Phase: CyclePhaseRun, Skipped: advancePhaseSkipped(in, CyclePhaseRun)}
	if in.ConnDecl != nil {
		res, err := s.RunDelegation(ctx, DelegateInput{
			AgentDecl: in.AgentDecl,
			ConnDecl:  in.ConnDecl,
			Judgment:  in.Invoker,
			Delegate:  in.Delegate,
			Contract:  in.Contract,
			Upstream:  in.Upstream,
			Git:       in.Git,
			Reconcile: in.Reconcile,
			Checks:    in.Checks,
			Compare:   in.Compare,
			CycleID:   cycleID,
			Cycle:     jc,
		})
		if err != nil {
			return nil, err
		}
		run.Items = res.Items
		run.NotStarted = res.NotStarted
	}
	phases = append(phases, run)

	if err := boundary(); err != nil {
		return phases, err
	}

	// 検証（verify --auto と同じ処理。委譲の段で検証中になった課題も対象になる）。
	verify := CyclePhaseResult{Phase: CyclePhaseVerify, Skipped: advancePhaseSkipped(in, CyclePhaseVerify)}
	if in.ConnDecl != nil {
		res, err := s.VerifyAutoJ5(ctx, J5AutoInput{
			AgentDecl:    in.AgentDecl,
			Invoker:      in.Invoker,
			Upstream:     in.Upstream,
			Checks:       in.Checks,
			Diffs:        in.Diffs,
			Descriptions: in.Descriptions,
			HeadGit:      in.HeadGit,
			Compare:      in.Compare,
			Connectors:   in.ConnDecl,
			CycleID:      cycleID,
			Cycle:        jc,

			MergeableRetries:  J5MergeableRetries,
			MergeableInterval: J5MergeableInterval,
		})
		if err != nil {
			return nil, err
		}
		verify.Items = res.Items
		verify.NotStarted = res.NotStarted
	}
	phases = append(phases, verify)

	return phases, nil
}

// advancePhaseSkipped は、進める段 p が宣言が無いために skipped になるか（取り込みは sources.json、release・計画・委譲・検証は
// connectors.json。分類は常に実行する）。runAdvancePhases と、止めて通さなかった段を埋める padAdvancePhases が共有する。
func advancePhaseSkipped(in CycleRunInput, p CyclePhase) bool {
	switch p {
	case CyclePhaseIngest:
		return in.Ingest == nil
	case CyclePhaseClassify:
		return false
	}
	return in.ConnDecl == nil
}
