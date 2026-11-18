package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// --- 取り込み元の子（source_child） ---
//
// 選んだ Issue の子孫の Issue は、課題にせず、その課題の「取り込み元の子」として平らに記録する
// （直の親は parent_external_key）。記録した子の題名・本文・上流の状態は、引数の無い取り込みが追従する。

// sourceChildMaxDepth は子孫をたどる段数の上限。
const sourceChildMaxDepth = 16

// SourceChild は課題の取り込み元の子 1 件（公開の形）。
type SourceChild struct {
	ExternalKey       string
	SourceID          string
	ParentExternalKey string
	URL               string
	Title             string
	Body              string
	UpstreamState     string
}

// plannedChild は取り込みで記録する子 1 件（子と、直の親の識別）。
type plannedChild struct {
	cand      IngestCandidate
	parentKey string
}

// planSourceChildren は root の子孫を、取り込めるもの（cands）の中だけで親をたどって決める。
// 直の親が root かその子孫である Issue だけが入り、たどるのは sourceChildMaxDepth 段まで。
// 親の記録が輪になっている Issue は子孫にしない（root 自身も子にならない）。
// 親の照合は大文字小文字を区別しない。戻りは上流の一覧の順（cands の順）に並べる。
func planSourceChildren(root IngestCandidate, cands []IngestCandidate) []plannedChild {
	byKey := make(map[string]IngestCandidate, len(cands))
	byParent := map[string][]IngestCandidate{}
	for _, c := range cands {
		byKey[strings.ToLower(c.Issue.ExternalKey)] = c
		if p := strings.ToLower(c.Issue.ParentExternalKey); p != "" {
			byParent[p] = append(byParent[p], c)
		}
	}
	// inCycle は c が親の記録の輪の上にあるか（親をたどって c 自身に戻るか）。c の祖先だけが
	// 別の輪を作っていても、c は輪の上にない。
	inCycle := func(c IngestCandidate) bool {
		self := strings.ToLower(c.Issue.ExternalKey)
		seen := map[string]bool{self: true}
		cur := c
		for {
			pk := strings.ToLower(cur.Issue.ParentExternalKey)
			p, ok := byKey[pk]
			if !ok {
				return false
			}
			if pk == self {
				return true
			}
			if seen[pk] {
				return false // 自分以外の輪に入った。自分は輪の上にない
			}
			seen[pk] = true
			cur = p
		}
	}

	visited := map[string]bool{strings.ToLower(root.Issue.ExternalKey): true}
	level := []IngestCandidate{root}
	order := map[string]int{}
	for i, c := range cands {
		order[strings.ToLower(c.Issue.ExternalKey)] = i
	}
	var out []plannedChild
	for depth := 0; depth < sourceChildMaxDepth && len(level) > 0; depth++ {
		var next []IngestCandidate
		for _, parent := range level {
			for _, kid := range byParent[strings.ToLower(parent.Issue.ExternalKey)] {
				k := strings.ToLower(kid.Issue.ExternalKey)
				if visited[k] || inCycle(kid) {
					continue
				}
				visited[k] = true
				out = append(out, plannedChild{cand: kid, parentKey: parent.Issue.ExternalKey})
				next = append(next, kid)
			}
		}
		level = next
	}
	sort.SliceStable(out, func(i, j int) bool {
		return order[strings.ToLower(out[i].cand.Issue.ExternalKey)] < order[strings.ToLower(out[j].cand.Issue.ExternalKey)]
	})
	return out
}

// insertSourceChild は cid の課題の子を 1 件記録する（作業ログは書かない）。
func insertSourceChild(ctx context.Context, tx *sql.Tx, at time.Time, cid int64, sourceID string, issue UpstreamIssue, parentKey string) error {
	now := formatTimestamp(at)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO source_child (challenge_id, source_id, external_key, parent_external_key, url, title, body, upstream_state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cid, sourceID, issue.ExternalKey, parentKey, issue.URL, issue.Title, issue.Body, string(upstreamStateOpen), now, now)
	return err
}

// loadSourceChildren は cid の課題の子を、記録した順に返す。無ければ空のスライス。
func loadSourceChildren(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, cid int64) ([]SourceChild, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT external_key, source_id, COALESCE(parent_external_key, ''), COALESCE(url, ''), COALESCE(title, ''), COALESCE(body, ''), upstream_state
		 FROM source_child WHERE challenge_id = ? ORDER BY id`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SourceChild{}
	for rows.Next() {
		var c SourceChild
		if err := rows.Scan(&c.ExternalKey, &c.SourceID, &c.ParentExternalKey, &c.URL, &c.Title, &c.Body, &c.UpstreamState); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sourceChildExists は external_key が子として記録されているかを返す。
func sourceChildExists(ctx context.Context, tx *sql.Tx, externalKey string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_child WHERE external_key = ?`, externalKey).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// --- 追従 ---

// sourceChildSnapshot は追従の①で読む子 1 件の目安。
type sourceChildSnapshot struct {
	UpstreamState string
}

func (s *Store) loadSourceChildSnapshots(ctx context.Context) (map[string]sourceChildSnapshot, error) {
	snaps := map[string]sourceChildSnapshot{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT external_key, upstream_state FROM source_child`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k, st string
			if err := rows.Scan(&k, &st); err != nil {
				return err
			}
			snaps[k] = sourceChildSnapshot{UpstreamState: st}
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return snaps, nil
}

// refreshSourceChild は記録した子の題名・本文・URL・上流の状態を issue の値へ更新する。
// 変わらなければ何も書かない。
func (s *Store) refreshSourceChild(ctx context.Context, actor string, ch Channel, issue UpstreamIssue, state upstreamState) error {
	return s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		var title, body, url, st string
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(title, ''), COALESCE(body, ''), COALESCE(url, ''), upstream_state FROM source_child WHERE external_key = ?`,
			issue.ExternalKey).Scan(&title, &body, &url, &st)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		newState := string(state)
		if title == issue.Title && body == issue.Body && url == issue.URL && st == newState {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE source_child SET title = ?, body = ?, url = ?, upstream_state = ?, updated_at = ? WHERE external_key = ?`,
			issue.Title, issue.Body, issue.URL, newState, formatTimestamp(rec.at), issue.ExternalKey)
		return err
	})
}

// setSourceChildState は記録した子の上流の状態だけを変える（変わらなければ書かない）。
func (s *Store) setSourceChildState(ctx context.Context, actor string, ch Channel, externalKey string, state upstreamState) error {
	return s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE source_child SET upstream_state = ?, updated_at = ? WHERE external_key = ? AND upstream_state <> ?`,
			string(state), formatTimestamp(rec.at), externalKey, string(state))
		return err
	})
}

// checkUnseenSourceChildren は repo の開いている一覧（seenIssues）に現れなかった、開いていると記録した子を
// 1 件ずつ上流に確かめ、closed・missing を決める（対応のある課題の checkUnseenOpenBindings と同じ決まり）。
// 確かめられなかったときは変えない。
func (s *Store) checkUnseenSourceChildren(ctx context.Context, actor string, ch Channel, repo string, snaps map[string]sourceChildSnapshot, seenIssues []UpstreamIssue, upstream UpstreamIssueSource) error {
	seen := make(map[string]bool, len(seenIssues))
	for _, issue := range seenIssues {
		seen[issue.ExternalKey] = true
	}
	type candidate struct {
		key    string
		repo   string
		number int
	}
	var cands []candidate
	for key, snap := range snaps {
		if snap.UpstreamState != string(upstreamStateOpen) || seen[key] {
			continue
		}
		keyRepo, number, ok := splitExternalKey(key)
		if !ok || !strings.EqualFold(keyRepo, repo) {
			continue
		}
		cands = append(cands, candidate{key: key, repo: keyRepo, number: number})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].number < cands[j].number })
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return err
		}
		issue, getErr := upstream.GetIssue(ctx, c.repo, c.number)
		switch {
		case isUpstreamGone(getErr):
			if err := s.setSourceChildState(ctx, actor, ch, c.key, upstreamStateMissing); err != nil {
				if ctx.Err() != nil {
					return err
				}
			}
		case getErr != nil:
			// 確かめられなかったときは変えない。
		case issue.State == "closed":
			if err := s.refreshSourceChild(ctx, actor, ch, withKey(issue, c.key), upstreamStateClosed); err != nil && ctx.Err() != nil {
				return err
			}
		default:
			if err := s.refreshSourceChild(ctx, actor, ch, withKey(issue, c.key), upstreamStateOpen); err != nil && ctx.Err() != nil {
				return err
			}
		}
	}
	return nil
}

// isUpstreamGone は GetIssue の失敗が「上流に無い」（404・410・移管）かを返す。
func isUpstreamGone(err error) bool {
	return errors.Is(err, ErrUpstreamIssueNotFound) || errors.Is(err, ErrUpstreamIssueTransferred)
}

func withKey(issue UpstreamIssue, key string) UpstreamIssue {
	issue.ExternalKey = key
	return issue
}

// deleteSourceChildren は cid の課題の子の記録を消し、消した件数を返す。
func deleteSourceChildren(ctx context.Context, tx *sql.Tx, cid int64) (int, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM source_child WHERE challenge_id = ?`, cid)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// checkUnbindSafeForChildren は、子の記録を持つ課題に終わっていない run か契約の実行があれば拒む。
func checkUnbindSafeForChildren(ctx context.Context, tx *sql.Tx, cid int64, id string) error {
	var unfinished int
	if err := tx.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM run WHERE challenge_id = ? AND ended_at IS NULL)
		      + (SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL)`, cid, cid).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished > 0 {
		return fmt.Errorf("%w: %s has unfinished run or contract run; its source children cannot be removed now", ErrValidation, id)
	}
	return nil
}
