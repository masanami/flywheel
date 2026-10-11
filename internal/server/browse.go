package server

import (
	"context"
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

// overviewFor は status と同じ規則で、waiting_external まで入れた Overview を返す。
func overviewFor(ctx context.Context, st *core.Store, checks *checkCache) (*core.Overview, error) {
	ov, err := baseOverview(ctx, st)
	if err != nil {
		return nil, err
	}
	if ov.WaitingExternal, err = st.ListWaitingExternal(ctx, checks.checkSource()); err != nil {
		return nil, err
	}
	return ov, nil
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
	return withStore(fleet, func(w http.ResponseWriter, r *http.Request, st *core.Store) {
		opt := core.ListOptions{}
		if v := r.URL.Query().Get("status"); v != "" {
			opt.Status = &v
		}
		cs, err := st.ListChallenges(r.Context(), opt)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		ov, err := overviewFor(r.Context(), st, checks)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		t := now()
		out := view.ListCardResponse{Challenges: make([]view.ChallengeCard, 0, len(cs))}
		for _, c := range cs {
			card, err := st.DeriveCard(r.Context(), c, ov, t)
			if err != nil {
				writeCoreError(w, err)
				return
			}
			out.Challenges = append(out.Challenges, view.FromChallengeCard(c, card))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func challengeHandler(fleet *Fleet, checks *checkCache, now func() time.Time) http.HandlerFunc {
	return withStore(fleet, func(w http.ResponseWriter, r *http.Request, st *core.Store) {
		d, err := st.GetChallengeWithoutReap(r.Context(), r.PathValue("id"))
		if err != nil {
			writeCoreError(w, err)
			return
		}
		ov, err := overviewFor(r.Context(), st, checks)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		card, err := st.DeriveCard(r.Context(), d.Challenge, ov, now())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view.FromChallengeDetailCard(d, card))
	})
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
		out := view.NeedsHumanResponse{Workspaces: []view.WorkspaceNeedsHuman{}, UnreadableWorkspaces: []string{}}
		for _, ws := range fleet.Statuses(r.Context()) {
			if ws.State != StateOK {
				continue
			}
			st, release, le := fleet.Acquire(r.Context(), ws.Name)
			if le != nil { // 判定の後に状態が変わった。除いて名前を添える
				out.UnreadableWorkspaces = append(out.UnreadableWorkspaces, ws.Name)
				continue
			}
			ov, err := baseOverview(r.Context(), st)
			release()
			if err != nil {
				if r.Context().Err() != nil {
					writeCoreError(w, err) // 要求の取り消し。残りを返す意味が無い
					return
				}
				out.UnreadableWorkspaces = append(out.UnreadableWorkspaces, ws.Name)
				continue
			}
			out.Workspaces = append(out.Workspaces, view.WorkspaceNeedsHuman{Name: ws.Name, NeedsHuman: view.FromNeedsHuman(ov)})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// questionsHandler は state が ok の全ワークスペースの未回答の保留を、
// raised_at 昇順（同時刻は workspace、次に challenge_id）で返す。借りる・読むのに失敗した
// ワークスペースは除き、名前を unreadable_workspaces に添える。
func questionsHandler(fleet *Fleet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		qs := []view.Question{}
		unreadable := []string{}
		for _, ws := range fleet.Statuses(r.Context()) {
			if ws.State != StateOK {
				continue
			}
			st, release, le := fleet.Acquire(r.Context(), ws.Name)
			if le != nil {
				unreadable = append(unreadable, ws.Name)
				continue
			}
			hs, err := st.ListUnansweredHolds(r.Context())
			release()
			if err != nil {
				if r.Context().Err() != nil {
					writeCoreError(w, err)
					return
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
		writeJSON(w, http.StatusOK, view.QuestionsResponse{Questions: qs, UnreadableWorkspaces: unreadable})
	}
}
