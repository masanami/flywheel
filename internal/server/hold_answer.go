package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// maxHoldAnswerBody は選ぶだけの回答の本文（option と check）の上限。
const maxHoldAnswerBody = 64 << 10

// decodeHoldAnswerBody は本文が {"check": <文字列>, "option": <文字列>} だけであることを厳密に検査する。
// 知らないキー（添え書き・計画を作り直す指定など）は受けない。値の中身（識別が保留にあるか・照合の値が合うか）は core が決める。
func decodeHoldAnswerBody(r *http.Request) (view.HoldAnswerRequest, bool) {
	var req view.HoldAnswerRequest
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxHoldAnswerBody))
	if err != nil {
		return req, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // 本文の後ろに続きがある
		return req, false
	}
	return req, true
}

// holdAnswerHandler は POST …/challenges/{id}/holds/{hold}/answer（画面のボタンで選ぶだけの回答）。本文は view.HoldAnswerRequest、成功の本文は view.HoldAnswerResponse。
// 経路 ui・本人確認 none で記録する。経路 ui の回答の限り・照合・誤りの種類は core が決める（ここで再実装しない）。誤りの状態コードは apierror.go の表のとおり。
// 成立した後に、ターミナルの回答と同じ決まりでサイクルを始める（きっかけは answer）。
func holdAnswerHandler(fleet *Fleet, launch CycleStarter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		req, ok := decodeHoldAnswerBody(r)
		if !ok {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, `本文は {"check": <文字列>, "option": <文字列>} だけを受け付ける`)
			return
		}
		ctx := r.Context()
		id := r.PathValue("id")
		var (
			hold *core.Hold
			cyc  core.CycleStartReason
			path string
		)
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) error {
			att, err := core.Verify(core.ChannelUI, noneVerifier{}, "", id)
			if err != nil {
				return err
			}
			_, hold, err = st.ExecuteAnswer(ctx, core.AnswerRequest{
				ChallengeID: id, Option: req.Option, HoldID: r.PathValue("hold"), HoldCheck: req.Check,
				Agent: agentDeclOrNil(st),
			}, att)
			if err != nil {
				return err
			}
			// 回答が成立した後にだけ、CLI と同じ決まりでサイクルを始める。
			cyc, path = decideCycleStart(ctx, st, false), st.Workspace()
			return nil
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		// 子の起動と合図の待ちは、ワークスペースの借りを返した後に行う。
		if cyc == core.CycleStartStarted {
			cyc = startDecidedCycle(path, core.CycleTriggerAnswer, launch)
		}
		writeJSON(w, http.StatusOK, view.HoldAnswerResponse{Cycle: view.FromCycleStartReason(cyc), Hold: view.FromHold(*hold)})
	}
}
