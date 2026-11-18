package core

// このファイルは、状況（status）に出す「取り込めるもの」（ingestable）を持つ。判定は ScanIngestable
// （isIngestable）の 1 か所で行い、ここでは親子の並べ替えと、課題になっている親の引き当てだけを行う。
// 親子は開いている Issue の一覧の親の識別（UpstreamIssue.ParentExternalKey）だけで組み立てる。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// IngestableChild は取り込めるもの（根）の子孫 1 件。子孫は深さを問わず根の Children に平らに入る。
type IngestableChild struct {
	ExternalKey       string
	Number            int
	Title             string
	URL               string
	ParentExternalKey string
}

// IngestableItem は取り込めるものの根 1 件。
type IngestableItem struct {
	ExternalKey string
	Number      int
	Repo        string
	Title       string
	URL         string
	// ParentExternalKey は上流の Issue の親の識別（親が無ければ空）。
	ParentExternalKey string
	// ParentChallengeID は親がすでに課題になっているときの、その課題の ID（"C-<n>"）。無ければ空。
	ParentChallengeID string
	Children          []IngestableChild
}

// Ingestable は status の ingestable（取り込めるものの一覧と、読めなかった理由）。
type Ingestable struct {
	Items []IngestableItem
	// Error は上流を読めなかった（一覧の取得の失敗・self_assignees を解決できない）ときの理由。読めたときは nil。
	Error *string
}

// IngestableState は取り込めるものの判定に要る、ストアから読むもの
// （課題になっている Issue・子の記録）。上流を読む前に読み、ストアの借りを返してから上流を読めるようにする。
type IngestableState struct {
	taken    map[string]bool
	bindings map[string]string // 小文字にした external_key → 課題の ID
}

// ReadIngestableState は課題になっている Issue と子の記録を読む（読み取りだけ）。
func (s *Store) ReadIngestableState(ctx context.Context) (*IngestableState, error) {
	st := &IngestableState{bindings: map[string]string{}}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if st.taken, err = loadTakenExternalKeys(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT external_key, challenge_id FROM source_binding`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var key string
			var cid int64
			if err := rows.Scan(&key, &cid); err != nil {
				return err
			}
			st.bindings[strings.ToLower(key)] = formatChallengeID(cid)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return st, nil
}

// IngestableUnavailable は上流の口を組み立てられなかったときの ingestable（items は空、error に理由）。
func IngestableUnavailable(err error) *Ingestable {
	msg := fmt.Sprintf("上流を取得できません: %v", err)
	return &Ingestable{Items: []IngestableItem{}, Error: &msg}
}

// ResolveIngestable は sources の開いている Issue を上流から読み、取り込めるものを親子に並べて返す。
// ストアには触れない（st は ReadIngestableState の結果）。sources が空なら items は空・error は nil。
func ResolveIngestable(ctx context.Context, st *IngestableState, sources []SourceEntry, upstream UpstreamIssueSource) *Ingestable {
	out := &Ingestable{Items: []IngestableItem{}}
	if len(sources) == 0 {
		return out
	}
	scan, err := scanIngestable(ctx, st.taken, sources, upstream)
	if err != nil {
		msg := fmt.Sprintf("上流を取得できません: %v", err)
		out.Error = &msg
		return out
	}
	var reasons []string
	if len(scan.ListFailed) > 0 {
		reasons = append(reasons, "Issue の一覧を読めなかったリポジトリ: "+strings.Join(scan.ListFailed, ", "))
	}
	if len(scan.Unresolved) > 0 {
		reasons = append(reasons, "self_assignees を解決できなかった取り込み元: "+strings.Join(scan.Unresolved, ", "))
	}
	if len(reasons) > 0 {
		msg := strings.Join(reasons, "; ")
		out.Error = &msg
	}
	out.Items = arrangeIngestable(scan.Candidates, st.bindings)
	return out
}

// arrangeIngestable は取り込めるもの（上流の一覧の順）を根と子孫に並べる。根は、直の親が取り込めるものに
// 無い Issue。子孫は取り込みと同じ planSourceChildren で決める（16 段まで・輪は子孫にしない）。どの根の
// 子孫にもならなかったもの（輪の中・16 段たどっても根に着かない）も根に並べ、互いを子孫にしない。
func arrangeIngestable(cands []IngestCandidate, bindings map[string]string) []IngestableItem {
	present := make(map[string]bool, len(cands))
	for _, c := range cands {
		present[strings.ToLower(c.Issue.ExternalKey)] = true
	}
	isRoot := make([]bool, len(cands))
	covered := map[string]bool{}
	kids := make([][]plannedChild, len(cands))
	for i, c := range cands {
		if p := strings.ToLower(c.Issue.ParentExternalKey); p != "" && present[p] {
			continue
		}
		isRoot[i] = true
		kids[i] = planSourceChildren(c, cands)
		for _, k := range kids[i] {
			covered[strings.ToLower(k.cand.Issue.ExternalKey)] = true
		}
	}
	items := make([]IngestableItem, 0, len(cands))
	for i, c := range cands {
		if !isRoot[i] && covered[strings.ToLower(c.Issue.ExternalKey)] {
			continue
		}
		item := IngestableItem{
			ExternalKey:       c.Issue.ExternalKey,
			Number:            c.Issue.Number,
			Repo:              c.Issue.Repo,
			Title:             c.Issue.Title,
			URL:               c.Issue.URL,
			ParentExternalKey: c.Issue.ParentExternalKey,
			ParentChallengeID: bindings[strings.ToLower(c.Issue.ParentExternalKey)],
			Children:          []IngestableChild{},
		}
		for _, k := range kids[i] {
			item.Children = append(item.Children, IngestableChild{
				ExternalKey: k.cand.Issue.ExternalKey, Number: k.cand.Issue.Number, Title: k.cand.Issue.Title,
				URL: k.cand.Issue.URL, ParentExternalKey: k.cand.Issue.ParentExternalKey,
			})
		}
		items = append(items, item)
	}
	return items
}

// ListIngestable は ReadIngestableState と ResolveIngestable を続けて行う（CLI 用。ストアの借りを
// 持ったまま上流を読まないよう、読みは先に終わる）。
func (s *Store) ListIngestable(ctx context.Context, sources []SourceEntry, upstream UpstreamIssueSource) (*Ingestable, error) {
	st, err := s.ReadIngestableState(ctx)
	if err != nil {
		return nil, err
	}
	return ResolveIngestable(ctx, st, sources, upstream), nil
}
