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
			p, err = st.PrepareApproval(ctx, r.PathValue("id"))
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
// HoldRelease=true の完了の承認を行う。
func approveHandler(fleet *Fleet, checks *checkCache, now func() time.Time) http.HandlerFunc {
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
			read *cardRead
		)
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) error {
			var err error
			if d, runs, read, err = approveAndRead(ctx, st, id, version); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		ov := read.resolve(ctx, checks)
		writeJSON(w, http.StatusOK, view.FromChallengeDetailCard(d, runs.Derive(d.Challenge, ov, now())))
	}
}

// approveAndRead は状態から承認の種類を決めて承認し、承認後の課題の全体像を読む。
// 種類の判定（①）で状態が対象外なら、版の違いより先に invalid_transition で拒否する。
func approveAndRead(ctx context.Context, st *core.Store, id string, version int) (*core.ChallengeDetail, *core.CardRuns, *cardRead, error) {
	preview, err := st.PrepareApproval(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	att, err := core.Verify(core.ChannelUI, noneVerifier{}, "", preview.ChallengeID)
	if err != nil {
		return nil, nil, nil, err
	}
	// 計画の承認に保留の概念は無い。完了の承認は常に本番反映の保留つき（T14）。
	_, _, err = st.ExecuteApproval(ctx, core.ApprovalRequest{
		ChallengeID:     preview.ChallengeID,
		ExpectedVersion: version,
		Decision:        core.ApprovalDecisionApproved,
		HoldRelease:     preview.Kind == core.ApprovalKindCompletion,
	}, att)
	if err != nil {
		return nil, nil, nil, err
	}
	d, err := st.GetChallengeWithoutReap(ctx, preview.ChallengeID)
	if err != nil {
		return nil, nil, nil, err
	}
	runs, err := st.LoadCardRuns(ctx, d.Challenge)
	if err != nil {
		return nil, nil, nil, err
	}
	read, err := loadCardOverview(ctx, st)
	if err != nil {
		return nil, nil, nil, err
	}
	return d, runs, read, nil
}
