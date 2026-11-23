package server

import (
	"errors"
	"net/http"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// ingestHandler は POST …/ingest（画面のボタンでの取り込み）。本文は view.IngestRequest、成功の本文は view.IngestAcceptedResponse。
// issues は上流の Issue を読む口の組み立て（取り込めるものの読み取りと同じもの）。取り込みの決まり（指定の形・親と子・断る条件）は
// ターミナルの `ingest <指定>...` と同じ core の口（IngestSelected）が持つ。成立した後に、承認と同じ決まりでサイクルを始める（きっかけは ingest）。
func ingestHandler(fleet *Fleet, issues func() (core.UpstreamIssueSource, error), launch CycleStarter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		var req view.IngestRequest
		if !decodeScreenBody(w, r, &req) {
			return
		}
		if len(req.Items) == 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "取り込むものを 1 つ以上指定する")
			return
		}
		st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		var (
			res  *core.IngestSelectedResult
			cyc  core.CycleStartReason
			path = st.Workspace()
		)
		err := func() error {
			defer release()
			decl, err := core.LoadSourcesDeclaration(path)
			if err != nil {
				if errors.Is(err, core.ErrConfigNotFound) {
					return errors.Join(core.ErrValidation, errors.New("取り込み元の宣言が無い"))
				}
				return err
			}
			up, err := issues()
			if err != nil {
				return errors.Join(core.ErrUpstreamFetchFailed, err)
			}
			res, err = st.IngestSelected(r.Context(), core.ChannelUI, core.IngestSelectedInput{Sources: decl.Sources, Specs: req.Items, Upstream: up})
			if err != nil {
				return err
			}
			cyc = decideCycleStart(r.Context(), st, false)
			return nil
		}()
		if err != nil {
			if errors.Is(err, core.ErrIngestUsage) {
				err = errors.Join(core.ErrValidation, err)
			}
			writeCoreError(w, err)
			return
		}
		// 子の起動と合図の待ちは、ワークスペースの借りを返した後に行う。
		if cyc == core.CycleStartStarted {
			cyc = startDecidedCycle(path, core.CycleTriggerIngest, launch)
		}
		writeJSON(w, http.StatusOK, view.IngestAcceptedResponse{Cycle: view.FromCycleStartReason(cyc), Ingested: view.FromIngestSelectedResult(res).Ingested})
	}
}
