package server

import (
	"context"
	"net/http"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// noticesHandler は GET …/notices（起きたことの知らせ。新しい順に直近の 20 件まで）。成功の本文は view.NoticesResponse。
// 知らせは core が読むたびに導く（種類と値だけ。文は画面が組み立てる）。サーバは題名を添えて写すだけで、何も書かない。
func noticesHandler(fleet *Fleet) http.HandlerFunc {
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
		ns, err := st.ListNotices(r.Context())
		if err != nil {
			writeCoreError(w, err)
			return
		}
		refs := make([]titleRef, 0, len(ns))
		for _, n := range ns {
			refs = append(refs, titleRef{challenge: n.ChallengeID, task: n.TaskID})
		}
		titles, err := loadTitles(r.Context(), st, refs)
		if err != nil {
			writeCoreError(w, err)
			return
		}
		out := make([]view.Notice, 0, len(ns))
		for _, n := range ns {
			out = append(out, toNotice(n, titles))
		}
		writeJSON(w, http.StatusOK, view.NoticesResponse{Notices: out})
	}
}

func toNotice(n core.Notice, titles titleLookup) view.Notice {
	out := view.Notice{At: view.FormatTimestamp(n.At), Kind: string(n.Kind)}
	if n.ChallengeID != "" {
		out.ChallengeID = &n.ChallengeID
		out.ChallengeTitle = titles.challenge(n.ChallengeID)
	}
	if n.TaskID != "" {
		out.TaskID = &n.TaskID
		out.TaskTitle = titles.task(n.TaskID)
	}
	switch n.Kind {
	case core.NoticeTaskAdded:
		c, o := n.AddedCount, string(n.Origin)
		out.AddedCount, out.Origin = &c, &o
	case core.NoticeCycleEnded:
		id, reason := n.CycleID, string(n.EndReason)
		out.CycleID, out.EndReason = &id, &reason
	}
	return out
}

// titleRef は題名を引く対象（課題の ID・タスクの ID。無ければ空）。
type titleRef struct{ challenge, task string }

// titleLookup は課題とタスクの題名の引き（見つからなければ nil）。
type titleLookup struct {
	challenges map[string]string
	tasks      map[string]string
}

func (t titleLookup) challenge(id string) *string { return lookupTitle(t.challenges, id) }
func (t titleLookup) task(id string) *string      { return lookupTitle(t.tasks, id) }

func lookupTitle(m map[string]string, id string) *string {
	if v, ok := m[id]; ok {
		return &v
	}
	return nil
}

// loadTitles は refs が指す課題の題名と、タスクの題名（最新の計画のタスク）を core の読み取りで引く。
func loadTitles(ctx context.Context, st *core.Store, refs []titleRef) (titleLookup, error) {
	out := titleLookup{challenges: map[string]string{}, tasks: map[string]string{}}
	needTasks := map[string]bool{}
	for _, r := range refs {
		if r.task != "" && r.challenge != "" {
			needTasks[r.challenge] = true
		}
	}
	if len(refs) == 0 {
		return out, nil
	}
	cs, err := st.ListChallenges(ctx, core.ListOptions{})
	if err != nil {
		return out, err
	}
	for _, c := range cs {
		out.challenges[c.ID] = c.Title
	}
	for cid := range needTasks {
		node, err := st.ChallengeTreeNode(ctx, cid, agentDeclOrNil(st), time.Now())
		if err != nil {
			continue // タスクの題名が引けなくても、知らせ・記録は返す（題名は null）
		}
		for _, t := range node.Tasks {
			if t.ID != nil {
				out.tasks[*t.ID] = t.Title
			}
		}
	}
	return out, nil
}
