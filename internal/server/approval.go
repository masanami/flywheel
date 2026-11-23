package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは承認の API（docs/features/m4-ui-server.md §IF / API「書き込みの API」）。
// 置くのは計画の承認と、本番反映の保留つきの完了の承認だけで、差し戻し・回答・予算の増額・
// 不可逆操作の承認のエンドポイントは置かない（ルートが無いので 404）。経路 ui で受けられる
// 操作の範囲の強制は core（登録簿）が持ち、ここでは再実装しない。

// maxApproveBody は承認の要求本文の上限（本文は {"version": N} だけ）。
const maxApproveBody = 4 << 10

// noneVerifier は方式 none の Verifier。確認の入力を求めず、actor は CLI と同じ規則で解決する。
type noneVerifier struct{}

func (noneVerifier) Method() core.Verification { return core.VerificationNone }

func (noneVerifier) Evidence() core.RelayEvidence { return core.RelayEvidence{} }

func (noneVerifier) Confirm(string, string) (string, error) { return core.CurrentActor() }

// approvalSummaryHandler は GET …/challenges/{id}/approval。release の PR の head の取得は、
// ストアの読み取りの借りを返してから行う（status と同じ）。head は承認の根拠なので、
// status のチェックのような保持（TTL）を通さずに毎回取る。
func approvalSummaryHandler(fleet *Fleet, heads func() core.UpstreamCheckSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		ctx := r.Context()
		var p *core.ApprovalPreview
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) (err error) {
			p, err = st.PrepareApprovalFor(ctx, r.PathValue("id"), agentDeclOrNil(st))
			return err
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		hs := core.ResolveReleaseHeads(ctx, p.PendingReleases, heads())
		writeJSON(w, http.StatusOK, view.FromApprovalPreview(p, hs))
	}
}

// decodeApproveBody は本文が {"version": <整数>} だけであることを厳密に検査する。
func decodeApproveBody(r *http.Request) (int, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxApproveBody))
	if err != nil {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var body struct {
		Version *int `json:"version"`
	}
	if err := dec.Decode(&body); err != nil || body.Version == nil {
		return 0, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // 本文の後ろに続きがある
		return 0, false
	}
	return *body.Version, true
}

// approveHandler は POST …/challenges/{id}/approve。計画承認待ちなら計画の承認、完了確認待ちなら
// HoldRelease=true の完了の承認を行う。成立した後に、CLI の承認と同じ決まりでサイクルを始め（きっかけは approve）、応答に cycle を足す。
func approveHandler(fleet *Fleet, checks *checkCache, cfg eventsConfig, start CycleStarter) http.HandlerFunc {
	now := cfg.now
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		version, ok := decodeApproveBody(r)
		if !ok {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, `本文は {"version": <整数>} だけを受け付ける`)
			return
		}
		ctx := r.Context()
		id := r.PathValue("id")
		var (
			d    *core.ChallengeDetail
			runs *core.CardRuns
			tree *core.TreeChallenge
			read *cardRead
			cyc  core.CycleStartReason
			path string
		)
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) error {
			var err error
			d, runs, tree, read, err = approveAndRead(ctx, st, id, version, now(), cfg)
			if err != nil {
				return err
			}
			// 承認が成立した後（本人確認と版の検査を通った後）にだけ、CLI と同じ決まりでサイクルを始める。
			cyc, path = decideCycleStart(ctx, st, false), st.Workspace()
			return nil
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		// 子の起動と合図の待ちは、ワークスペースの借りを返した後に行う（借りを持ったまま待たない）。
		if cyc == core.CycleStartStarted {
			cyc = startDecidedCycle(path, core.CycleTriggerApprove, start)
		}
		ov := read.resolve(ctx, checks)
		writeJSON(w, http.StatusOK, view.WithCycle{Body: view.FromApprovedChallengeDetailCard(d, runs.Derive(d.Challenge, ov, now()), tree), Cycle: view.FromCycleStartReason(cyc)})
	}
}

// approveAndRead は状態から承認の種類を決めて承認し、承認後の課題の全体像を読む。
// 種類の判定（①）で状態が対象外なら、版の違いより先に invalid_transition で拒否する。
// 状況（Overview）は承認の確定の後に 1 回だけ組み立てる。承認が確定した後の課題のツリーの読み直しが失敗しても、
// 承認は通っているのでエラーにせず、ツリーだけを nil にして返す（失敗はログに残す。画面は次の読み直しでツリーを出す）。
func approveAndRead(ctx context.Context, st *core.Store, id string, version int, now time.Time, cfg eventsConfig) (*core.ChallengeDetail, *core.CardRuns, *core.TreeChallenge, *cardRead, error) {
	preview, err := st.PrepareApproval(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	att, err := core.Verify(core.ChannelUI, noneVerifier{}, "", preview.ChallengeID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// 計画の承認に保留の概念は無い。完了の承認は常に本番反映の保留つき（T14）。
	_, _, err = st.ExecuteApproval(ctx, core.ApprovalRequest{
		ChallengeID:     preview.ChallengeID,
		ExpectedVersion: version,
		Decision:        core.ApprovalDecisionApproved,
		HoldRelease:     preview.Kind == core.ApprovalKindCompletion,
		Agent:           agentDeclOrNil(st),
	}, att)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	d, err := st.GetChallengeWithoutReap(ctx, preview.ChallengeID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	runs, err := st.LoadCardRuns(ctx, d.Challenge)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ov, err := cfg.overview(ctx, st)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	tree, err := cfg.treeNode(ctx, st, d.ID, now, ov)
	if err != nil {
		cfg.logFn()("approve %s: 承認は確定したが、課題のツリーの読み直しに失敗した（応答の tree は null）: %v", d.ID, err)
		tree = nil
	}
	read, err := loadCardCandidates(ctx, st, ov)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return d, runs, tree, read, nil
}
