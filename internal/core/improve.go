package core

// このファイルは `improve` の段と `flywheel improve` が呼ぶ公開 API（Store.ImproveJ8）を持つ。
//
// 起動の規則: 自動（cycle の improve の段）は起動の件数が improve_threshold 以上のときだけ J8 を 1 回呼ぶ。
// 手動は 1 件以上で呼び、0 件なら呼ばない。自動だけに J8 の連続失敗の上限（failure_limit）を当て、達したら
// 呼ばずに理由 failure_limit の NotStarted（課題の ID を持たない）で示す。入力の組み立ては judgment_j8_input.go、
// 出力の写像は judgment_j8.go が持つ。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ImproveJ8Input は Store.ImproveJ8 の入力。
type ImproveJ8Input struct {
	// Manual は `flywheel improve`（手動）。false は cycle の improve の段（自動）。
	Manual    bool
	AgentDecl *AgentDeclaration
	// ConnDecl は成果物の経路を決める宣言（nil なら経路はどの層も Issue）。
	ConnDecl *ConnectorsDeclaration
	Invoker  JudgmentInvoker
	// SlotGit が非 nil なら、knowledge の層の経路のためにワークスペースの origin を読む。nil なら空。
	SlotGit SlotGit
	// CycleID はこの操作が属する周。自動では必須。手動では空でもよい（周の上限を評価しない）。
	CycleID string
	// Cycle が非 nil なら枠超過の状態をこの JudgmentCycle と共有する。
	Cycle *JudgmentCycle
}

// ImproveJ8Result は Store.ImproveJ8 の結果。
type ImproveJ8Result struct {
	// Triggering は起動の件数（見送りの抑えの対象を除く）。
	Triggering int
	// Called は J8 を呼んだか。
	Called bool
	RunID  string
	Result RunResult
	// Filed は新しく起票した改修課題、Linked は重複の抑えで紐づけた既存の改修課題、
	// Waited は「待つ」を付けた発生。
	Filed  []string
	Linked []string
	Waited []string
	// NotStarted は呼ばなかった理由（failure_limit・rate_limited・cycle_budget）。課題の ID は持たない。
	NotStarted []NotStarted
	Note       string
}

// ImproveJ8 は J8 を（規則どおりなら）1 回呼び、出力を写す。
//
//   - 起動の件数がしきい値（自動は improve_threshold、手動は 1）に満たなければ呼ばない。
//   - 自動で J8 の連続失敗が failure_limit に達していれば呼ばず、NotStarted（failure_limit）を返す。
//   - 出力が不正なら run を invalid_output にして何も書かない。
func (s *Store) ImproveJ8(ctx context.Context, in ImproveJ8Input) (*ImproveJ8Result, error) {
	if in.AgentDecl == nil || in.Invoker == nil || (!in.Manual && in.CycleID == "") {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		return nil, ErrValidation
	}
	threshold := in.AgentDecl.ImproveThreshold
	if threshold <= 0 {
		threshold = defaultImproveThreshold
	}

	// 入力の事実と「待つ」を付ける発生は、同じ読み取りで読む。
	var jc *j8Context
	var waitIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if jc, err = loadJ8ContextTx(ctx, tx, threshold); err != nil {
			return err
		}
		waitIDs, err = loadPendingPerformanceOccurrenceIDs(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	res := &ImproveJ8Result{Triggering: jc.Triggering}

	need := threshold
	if in.Manual {
		need = 1
	}
	if jc.Triggering < need {
		return res, nil
	}

	if !in.Manual {
		limit := judgmentFailureLimit(in.AgentDecl)
		var hit *judgmentLimitHit
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			h, err := loadJ8LimitHit(ctx, tx, limit)
			hit = h
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
		if hit != nil {
			res.NotStarted = append(res.NotStarted, NotStarted{Reason: NotStartedFailureLimit,
				Detail: fmt.Sprintf("%s: %d consecutive failures (limit %d)", hit.Judgment, hit.Count, hit.Limit)})
			return res, nil
		}
	}

	runIn := RunJudgmentInput{
		Judgment:     JudgmentJ8,
		MaxBudgetUSD: in.AgentDecl.JudgmentBudgetFor(JudgmentJ8),
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     prepareJ8(jc),
		OutputSchema: j8OutputSchema(),
		Invoker:      in.Invoker,
	}
	var run *RunJudgmentResult
	cyc := in.Cycle
	if cyc == nil && in.CycleID != "" {
		cyc = NewJudgmentCycle(in.CycleID)
	}
	if cyc != nil {
		var ns *NotStarted
		run, ns, err = cyc.RunJudgment(ctx, s, runIn)
		if err != nil {
			return nil, err
		}
		if ns != nil {
			res.NotStarted = append(res.NotStarted, *ns)
			return res, nil
		}
	} else if run, err = s.RunJudgment(ctx, runIn); err != nil {
		return nil, err
	}
	res.Called, res.RunID, res.Result = true, run.RunID, run.Result
	if run.Result != RunResultSucceeded {
		return res, nil
	}

	invalid := func() (*ImproveJ8Result, error) {
		if err := s.invalidateRunOutput(ctx, run.RunID); err != nil {
			return nil, err
		}
		res.Result = RunResultInvalidOutput
		return res, nil
	}
	props, ok := validateJ8Output(run.StructuredOutput)
	if !ok {
		return invalid()
	}
	originURL, originNote := s.j8OriginURL(ctx, in, props)
	m, bad, err := s.applyJ8(ctx, run.RunID, props, jc.DetailOccurrenceIDs(), waitIDs, in.ConnDecl, originURL)
	if err != nil {
		// 写像が硬いエラーで終わった run は、succeeded のままにせず失敗に数える（毎周の呼び直しを
		// failure_limit で止める）。手動はエラーを返し、自動は結果に注記する。
		invErr := s.invalidateRunOutput(ctx, run.RunID)
		if in.Manual {
			var noteErr error
			if originNote != "" {
				noteErr = errors.New(originNote)
			}
			return nil, errors.Join(err, invErr, noteErr)
		}
		res.Result, res.Note = RunResultInvalidOutput, "mapping failed: "+err.Error()
		if invErr != nil {
			res.Note += "; invalidate failed: " + invErr.Error()
		}
		if originNote != "" {
			res.Note += "; " + originNote
		}
		return res, nil
	}
	if bad {
		return invalid()
	}
	res.Filed, res.Linked, res.Waited = m.Filed, m.Linked, m.Waited
	res.Note = fmt.Sprintf("filed %d, linked %d, waited %d", len(m.Filed), len(m.Linked), len(m.Waited))
	if originNote != "" {
		res.Note += "; " + originNote
	}
	return res, nil
}

// j8OriginURL は knowledge の層の提案があり、SlotGit が渡されているときだけ、ワークスペースの origin の
// URL を読む。読めなければ URL は空（経路は Issue になる）で、検査が失敗したときはその理由を note で返す
// （結果の注記に残す）。
func (s *Store) j8OriginURL(ctx context.Context, in ImproveJ8Input, props []J8Proposal) (url, note string) {
	if in.SlotGit == nil || in.ConnDecl == nil {
		return "", ""
	}
	for _, p := range props {
		if p.Layer != OccurrenceLayerKnowledge {
			continue
		}
		st, err := in.SlotGit.Inspect(ctx, SlotTree{Path: s.workspace})
		if err != nil {
			return "", "origin unreadable: " + err.Error()
		}
		if !st.Exists {
			return "", ""
		}
		return st.OriginURL, ""
	}
	return "", ""
}
