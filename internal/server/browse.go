package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは閲覧の API（docs/features/m4-ui-server.md §IF / API「閲覧の API」）。
// どれもストアへ書かない（中断した run を回収する経路は使わない）。

// getOnly は GET 以外を 404 で拒否する。
func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
		return false
	}
	return true
}

// withStore は {name} のワークスペースのストアを借りて h を呼ぶ。借りられなければ
// その理由のエラーを返す。
func withStore(fleet *Fleet, h func(w http.ResponseWriter, r *http.Request, st *core.Store)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
		if le != nil {
			writeError(w, le.Status, le.Code, le.Message)
			return
		}
		defer release()
		h(w, r, st)
	}
}

// cardRead は、ストアの読み取りの借りを持つ間に読むもの（gh の取得は含まない）。
type cardRead struct {
	ov    *core.Overview
	cands []core.WaitingExternalCandidate
}

// readForCards は name のストアを借りて fn を呼び、fn が返したら借りを返す。
func readForCards(ctx context.Context, fleet *Fleet, name string, fn func(st *core.Store) error) error {
	st, release, le := fleet.Acquire(ctx, name)
	if le != nil {
		return le
	}
	defer release()
	return fn(st)
}

// loadCardOverview は status と同じ規則の Overview を読む。借りを持つのは基本の Overview と
// waiting_external の候補の導出の間だけで、gh によるチェックの取得は借りを返してから行う
// （遅い gh が状態更新の Lock を待たせないように）。
func loadCardOverview(ctx context.Context, st *core.Store) (*cardRead, error) {
	ov, err := baseOverview(ctx, st)
	if err != nil {
		return nil, err
	}
	cands, err := st.WaitingExternalCandidates(ctx)
	if err != nil {
		return nil, err
	}
	return &cardRead{ov: ov, cands: cands}, nil
}

func (c *cardRead) resolve(ctx context.Context, checks *checkCache) *core.Overview {
	c.ov.WaitingExternal = core.ResolveWaitingExternal(ctx, c.cands, checks.checkSource())
	return c.ov
}

// writeLookupOrCoreError は借りられなかった（*LookupError）か、core のエラーかに応じて書く。
func writeLookupOrCoreError(w http.ResponseWriter, err error) {
	var le *LookupError
	if errors.As(err, &le) {
		writeError(w, le.Status, le.Code, le.Message)
		return
	}
	writeCoreError(w, err)
}

// baseOverview は waiting_external を入れない Overview（宣言を読めなくても動かす。CLI の status と同じ）。
func baseOverview(ctx context.Context, st *core.Store) (*core.Overview, error) {
	decl, derr := core.LoadAgentDeclaration(st.Workspace())
	if derr != nil {
		decl = nil
	}
	return st.GetOverviewFor(ctx, decl)
}

func challengesHandler(fleet *Fleet, checks *checkCache, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		ctx := r.Context()
		opt := core.ListOptions{}
		if v := r.URL.Query().Get("status"); v != "" {
			opt.Status = &v
		}
		var cs []core.Challenge
		var runs []*core.CardRuns
		var read *cardRead
		// ストアを読むのは 1 回の借りの中だけ（1 つの一貫した読み取り）。gh の取得は借りを返してから行う。
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) (err error) {
			if cs, err = st.ListChallenges(ctx, opt); err != nil {
				return err
			}
			for _, c := range cs {
				cr, err := st.LoadCardRuns(ctx, c)
				if err != nil {
					return err
				}
				runs = append(runs, cr)
			}
			read, err = loadCardOverview(ctx, st)
			return err
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		ov := read.resolve(ctx, checks)
		t := now()
		out := view.ListCardResponse{Challenges: make([]view.ChallengeCard, 0, len(cs))}
		for i, c := range cs {
			out.Challenges = append(out.Challenges, view.FromChallengeCard(c, runs[i].Derive(c, ov, t)))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func challengeHandler(fleet *Fleet, checks *checkCache, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		ctx := r.Context()
		var d *core.ChallengeDetail
		var runs *core.CardRuns
		var read *cardRead
		err := readForCards(ctx, fleet, r.PathValue("name"), func(st *core.Store) (err error) {
			if d, err = st.GetChallengeWithoutReap(ctx, r.PathValue("id")); err != nil {
				return err
			}
			if runs, err = st.LoadCardRuns(ctx, d.Challenge); err != nil {
				return err
			}
			read, err = loadCardOverview(ctx, st)
			return err
		})
		if err != nil {
			writeLookupOrCoreError(w, err)
			return
		}
		ov := read.resolve(ctx, checks)
		writeJSON(w, http.StatusOK, view.FromChallengeDetailCard(d, runs.Derive(d.Challenge, ov, now())))
	}
}

func logHandler(fleet *Fleet) http.HandlerFunc {
	return withStore(fleet, func(w http.ResponseWriter, r *http.Request, st *core.Store) {
		var id *string
		if v := r.URL.Query().Get("challenge"); v != "" {
			id = &v
		}
		as, err := st.ListActivities(r.Context(), id)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.LogResponse{Activities: view.FromActivities(as)})
	})
}

func runsHandler(fleet *Fleet) http.HandlerFunc {
	return withStore(fleet, func(w http.ResponseWriter, r *http.Request, st *core.Store) {
		opt := core.RunListOptions{}
		if v := r.URL.Query().Get("challenge"); v != "" {
			opt.ChallengeID = &v
		}
		opt.OpenOnly = r.URL.Query().Get("open") == "true"
		rs, err := st.ListRunsWithoutReap(r.Context(), opt)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.RunsResponse{Runs: view.FromRuns(rs)})
	})
}

func runReportHandler(fleet *Fleet) http.HandlerFunc {
	return withStore(fleet, func(w http.ResponseWriter, r *http.Request, st *core.Store) {
		rep, err := st.ReadRunReport(r.Context(), r.PathValue("id"))
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromRunReport(rep))
	})
}

// needsHumanHandler は state が ok の各ワークスペースの status.needs_human を宣言の順に返す。
// 借りる・読むのに失敗したワークスペースは除き、名前を unreadable_workspaces に添える（残りは返す）。
// needs_human は GitHub を見ない（waiting_external を入れない）。
func needsHumanHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		out, err := collectNeedsHuman(r.Context(), fleet, fleet.Statuses(r.Context()))
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// collectNeedsHuman は statuses（判定した時点の状態）のうち ok のものを借りて読む。借りるのに失敗したものは、
// 要求が取り消されていればエラーにし、そうでなければ除いて名前を添える。
func collectNeedsHuman(ctx context.Context, fleet *Fleet, statuses []WorkspaceStatus) (view.NeedsHumanResponse, error) {
	out := view.NeedsHumanResponse{Workspaces: []view.WorkspaceNeedsHuman{}, UnreadableWorkspaces: []string{}}
	for _, ws := range statuses {
		if ws.State != StateOK {
			continue
		}
		st, release, le := fleet.Acquire(ctx, ws.Name)
		if le != nil { // 判定の後に状態が変わった。除いて名前を添える
			if ctx.Err() != nil {
				return view.NeedsHumanResponse{}, ctx.Err() // 要求の取り消し。残りを返す意味が無い
			}
			out.UnreadableWorkspaces = append(out.UnreadableWorkspaces, ws.Name)
			continue
		}
		ov, err := baseOverview(ctx, st)
		release()
		if err != nil {
			if ctx.Err() != nil {
				return view.NeedsHumanResponse{}, err
			}
			out.UnreadableWorkspaces = append(out.UnreadableWorkspaces, ws.Name)
			continue
		}
		out.Workspaces = append(out.Workspaces, view.WorkspaceNeedsHuman{Name: ws.Name, NeedsHuman: view.FromNeedsHuman(ov)})
	}
	return out, nil
}

// questionsHandler は state が ok の全ワークスペースの未回答の保留を、
// raised_at 昇順（同時刻は workspace、次に challenge_id）で返す。借りる・読むのに失敗した
// ワークスペースは除き、名前を unreadable_workspaces に添える。
func questionsHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		out, err := collectQuestions(r.Context(), fleet, fleet.Statuses(r.Context()))
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// collectQuestions は collectNeedsHuman と同じ規則で、ok のワークスペースの未回答の保留を読む。
func collectQuestions(ctx context.Context, fleet *Fleet, statuses []WorkspaceStatus) (view.QuestionsResponse, error) {
	qs := []view.Question{}
	unreadable := []string{}
	for _, ws := range statuses {
		if ws.State != StateOK {
			continue
		}
		st, release, le := fleet.Acquire(ctx, ws.Name)
		if le != nil {
			if ctx.Err() != nil {
				return view.QuestionsResponse{}, ctx.Err()
			}
			unreadable = append(unreadable, ws.Name)
			continue
		}
		hs, err := st.ListUnansweredHolds(ctx)
		release()
		if err != nil {
			if ctx.Err() != nil {
				return view.QuestionsResponse{}, err
			}
			unreadable = append(unreadable, ws.Name)
			continue
		}
		for _, h := range hs {
			qs = append(qs, view.FromUnansweredHold(ws.Name, h))
		}
	}
	// raised_at は固定幅の UTC 表記なので文字列の比較が時刻の順になる。
	sort.SliceStable(qs, func(i, j int) bool {
		a, b := qs[i], qs[j]
		if a.RaisedAt != b.RaisedAt {
			return a.RaisedAt < b.RaisedAt
		}
		if a.Workspace != b.Workspace {
			return a.Workspace < b.Workspace
		}
		if len(a.ChallengeID) != len(b.ChallengeID) { // C-9 は C-10 より前
			return len(a.ChallengeID) < len(b.ChallengeID)
		}
		return a.ChallengeID < b.ChallengeID
	})
	return view.QuestionsResponse{Questions: qs, UnreadableWorkspaces: unreadable}, nil
}
