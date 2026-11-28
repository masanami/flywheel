package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- 選んだ Issue だけを課題にする取り込み ---
//
// 新しい課題を作るのは、オーナーが選んだ Issue だけ（`flywheel ingest <指定>...`）。
// 取り込めるものの判定は ScanIngestable の 1 か所に置き、一覧に出るものと
// 取り込めるものが食い違わないようにする。

// ErrIngestUsage は `ingest <指定>...` の指定の誤り（形が正しくない・同じ Issue を
// 2 度並べた）を表す。ストアは変えない。
var ErrIngestUsage = errors.New("core: ingest: invalid selection")

// IngestCandidate は取り込めるもの 1 件（取り込み元と、その Issue）。
type IngestCandidate struct {
	Source SourceEntry
	Issue  UpstreamIssue
}

// IngestScan は ScanIngestable の結果。
type IngestScan struct {
	// Candidates は取り込めるもの（宣言の順・上流の一覧の順）。
	Candidates []IngestCandidate
	// ListFailed は開いている Issue の一覧を取得できなかったリポジトリ（宣言の表記）。
	ListFailed []string
	// Unresolved は self_assignees を解決できなかった取り込み元の ID。
	Unresolved []string
	// taken は走査の前に読んだ、課題か子になっている external_key。listed は読めた開いている Issue の
	// 一覧に在った external_key（取り込めるかに依らない）。どちらもキーは小文字。断る理由を分けるために持つ。
	taken, listed map[string]bool
}

// isIngestable は取り込めるものの判定の本体。issue がその取り込み元の方針
// （assignee_policy）に合い、課題になっておらず、どの課題の子としても記録されて
// いない（taken にその external_key が無い）ときだけ true。self_assignees を
// 解決できていない（resolved=false）取り込み元の Issue は常に false。
// taken のキーは上流の応答の正規の表記（記録どうしの比べ方は取り込みと同じ）。
func isIngestable(src SourceEntry, selfAssignees map[string]bool, resolved bool, issue UpstreamIssue, taken map[string]bool) bool {
	if !resolved || taken[issue.ExternalKey] {
		return false
	}
	return issueMatchesAssigneePolicy(src.Policy(), selfAssignees, issue.Assignees)
}

// loadTakenExternalKeys は課題になっている（source_binding）か、課題の子として
// 記録されている（source_child）external_key の集合を読む。
func loadTakenExternalKeys(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]bool, error) {
	taken := map[string]bool{}
	for _, table := range []string{"source_binding", "source_child"} {
		rows, err := q.QueryContext(ctx, "SELECT external_key FROM "+table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				_ = rows.Close()
				return nil, err
			}
			taken[k] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return taken, nil
}

// ScanIngestable は sources（宣言の取り込み元の全部）の開いている Issue を読み、
// 取り込めるもの（isIngestable）を返す。状況の出力（ListIngestable）もこの判定を使う。
func (s *Store) ScanIngestable(ctx context.Context, sources []SourceEntry, upstream UpstreamIssueSource) (*IngestScan, error) {
	var taken map[string]bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		taken, err = loadTakenExternalKeys(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return scanIngestable(ctx, taken, sources, upstream)
}

// scanIngestable は ScanIngestable の上流を読む部分（ストアには触れない）。
func scanIngestable(ctx context.Context, taken map[string]bool, sources []SourceEntry, upstream UpstreamIssueSource) (*IngestScan, error) {
	scan := &IngestScan{taken: map[string]bool{}, listed: map[string]bool{}}
	for k := range taken {
		scan.taken[strings.ToLower(k)] = true
	}
	seen := map[string]bool{}
	for _, src := range sources {
		selfAssignees, resolved := resolveIngestSelfAssignees(ctx, src, upstream)
		if !resolved {
			scan.Unresolved = append(scan.Unresolved, src.ID)
		}
		for _, repo := range src.Repos {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			issues, err := upstream.ListOpenIssues(ctx, repo)
			if err != nil {
				scan.ListFailed = append(scan.ListFailed, repo)
				continue
			}
			for _, issue := range issues {
				if seen[issue.ExternalKey] {
					continue
				}
				seen[issue.ExternalKey] = true
				scan.listed[strings.ToLower(issue.ExternalKey)] = true
				if isIngestable(src, selfAssignees, resolved, issue, taken) {
					scan.Candidates = append(scan.Candidates, IngestCandidate{Source: src, Issue: issue})
				}
			}
		}
	}
	return scan, nil
}

// IngestSelectedInput は Store.IngestSelected の入力。
type IngestSelectedInput struct {
	// Sources は宣言の取り込み元の全部（判定は --source を付けても全部を読む）。
	Sources []SourceEntry
	// SourceID は番号だけの指定を探す取り込み元を絞る（nil なら絞らない）。
	SourceID *string
	// Specs は `<owner>/<repo>#<番号>` か番号だけ（利用者が打ったまま）。
	Specs    []string
	Upstream UpstreamIssueSource
	// Chat は対話の欄の印。nil でなければ、作成の書き込みの中で印を検査し（通らなければ ErrChatMarkInvalid で何も
	// 作らない）、作った課題を指定の順に印の会話へ紐づける。
	Chat *ChatSessionMark
}

// IngestedChallenge は IngestSelected が作った課題 1 件。
type IngestedChallenge struct {
	ChallengeID string
	ExternalKey string
	// Children は記録した取り込み元の子の数。
	Children int
}

// IngestSelectedResult は IngestSelected の出力（指定の順）。
type IngestSelectedResult struct {
	Ingested []IngestedChallenge
}

var (
	ingestSpecKeyPattern    = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)
	ingestSpecNumberPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// IngestSelected は指定した Issue だけを新しい課題にする。指定のどれか 1 つでも
// 取り込めるものに無ければ、どれも作らず ErrValidation（上流を取得できない・
// self_assignees を解決できないために判定できないときは ErrUpstreamFetchFailed）。
// 断る指定が複数あれば、文には指定ごとの理由を指定の順に並べ、誤りの種類は最初に断る指定のものにする。
// 作成は 1 つのトランザクションで行い、その中で課題になっていないことを読み直す。
func (s *Store) IngestSelected(ctx context.Context, ch Channel, in IngestSelectedInput) (*IngestSelectedResult, error) {
	if len(in.Specs) == 0 {
		return nil, fmt.Errorf("%w: no issue specified", ErrIngestUsage)
	}
	type spec struct {
		raw    string
		key    string // 明示の指定（`<owner>/<repo>#<番号>`）。番号だけなら ""
		number int
	}
	specs := make([]spec, 0, len(in.Specs))
	for _, raw := range in.Specs {
		if m := ingestSpecKeyPattern.FindStringSubmatch(raw); m != nil {
			n, _ := strconv.Atoi(m[2])
			specs = append(specs, spec{raw: raw, key: m[1] + "#" + m[2], number: n})
		} else if ingestSpecNumberPattern.MatchString(raw) {
			n, _ := strconv.Atoi(raw)
			specs = append(specs, spec{raw: raw, number: n})
		} else {
			return nil, fmt.Errorf("%w: %q is not <owner>/<repo>#<number> or a number", ErrIngestUsage, raw)
		}
	}
	for i := range specs {
		for j := i + 1; j < len(specs); j++ {
			a, b := specs[i], specs[j]
			if a.raw == b.raw || (a.key != "" && b.key != "" && strings.EqualFold(a.key, b.key)) {
				return nil, fmt.Errorf("%w: %q is listed twice", ErrIngestUsage, b.raw)
			}
		}
	}
	if _, err := SelectSources(&SourcesDeclaration{Sources: in.Sources}, in.SourceID); err != nil {
		return nil, err
	}

	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	scan, err := s.ScanIngestable(ctx, in.Sources, in.Upstream)
	if err != nil {
		return nil, err
	}

	unresolved := map[string]bool{}
	for _, id := range scan.Unresolved {
		unresolved[id] = true
	}
	failedRepo := map[string]bool{}
	for _, r := range scan.ListFailed {
		failedRepo[strings.ToLower(r)] = true
	}

	chosen := make([]IngestCandidate, 0, len(specs))
	chosenKey := map[string]bool{}
	// 断る指定は途中で返さずに集める（指定の順）。class は最初に断る指定の誤りの種類。
	var class error
	var refusals []string
	refuse := func(kind error, detail string) {
		if class == nil {
			class = kind
		}
		refusals = append(refusals, detail)
	}
	cannotDecide := func(what string) {
		refuse(ErrUpstreamFetchFailed, fmt.Sprintf("cannot decide whether %s is ingestable (upstream list or self_assignees unavailable)", what))
	}
specLoop:
	for _, sp := range specs {
		var matches []IngestCandidate
		for _, c := range scan.Candidates {
			switch {
			case sp.key != "":
				if strings.EqualFold(c.Issue.ExternalKey, sp.key) {
					matches = append(matches, c)
				}
			case in.SourceID == nil || c.Source.ID == *in.SourceID:
				if _, n, ok := splitExternalKey(c.Issue.ExternalKey); ok && n == sp.number {
					matches = append(matches, c)
				}
			}
		}
		if sp.key == "" {
			// 番号だけの指定は、判定できなかった取り込み元・リポジトリが範囲にあると、
			// 見えている 1 件が唯一とは言えない。
			for _, src := range in.Sources {
				if in.SourceID != nil && src.ID != *in.SourceID {
					continue
				}
				bad := unresolved[src.ID]
				for _, repo := range src.Repos {
					bad = bad || failedRepo[strings.ToLower(repo)]
				}
				if bad {
					cannotDecide(sp.raw)
					continue specLoop
				}
			}
		}
		if len(matches) == 1 {
			c := matches[0]
			if chosenKey[c.Issue.ExternalKey] {
				refuse(ErrIngestUsage, fmt.Sprintf("%q selects an issue that is already listed", sp.raw))
				continue
			}
			chosenKey[c.Issue.ExternalKey] = true
			chosen = append(chosen, c)
			continue
		}
		if len(matches) > 1 {
			refuse(ErrValidation, fmt.Sprintf("%q matches %d ingestable issues; specify <owner>/<repo>#<number>", sp.raw, len(matches)))
			continue
		}
		// 取り込めるものに無い。判定できなかったことが原因かを見分ける。
		for _, src := range in.Sources {
			if sp.key == "" && in.SourceID != nil && src.ID != *in.SourceID {
				continue
			}
			for _, repo := range src.Repos {
				if sp.key != "" && !strings.EqualFold(sp.key[:strings.LastIndexByte(sp.key, '#')], repo) {
					continue
				}
				if unresolved[src.ID] || failedRepo[strings.ToLower(repo)] {
					cannotDecide(sp.raw)
					continue specLoop
				}
			}
		}
		reason, err := ingestRejectReason(ctx, scan, in, sp.key, sp.number)
		if err != nil {
			return nil, err
		}
		refuse(ErrValidation, fmt.Sprintf("%q is not ingestable: %s", sp.raw, reason))
	}
	if class != nil {
		return nil, fmt.Errorf("%w: %s", class, strings.Join(refusals, "; "))
	}

	// 子孫は、取り込めるものの中だけで親をたどって決める。同じコマンドの別の指定の子孫にあたる指定は拒む。
	// 一覧を読めなかったリポジトリ・方針を判定できなかった取り込み元にある子孫は、取り込めるものに入らないので、
	// 子として記録されない（<owner>/<repo>#<番号> の指定は、その Issue のリポジトリを読めれば受ける）。
	planned := make([][]plannedChild, len(chosen))
	for i, c := range chosen {
		planned[i] = planSourceChildren(c, scan.Candidates)
	}
	for i, kids := range planned {
		for _, kid := range kids {
			for j, other := range chosen {
				if i != j && strings.EqualFold(kid.cand.Issue.ExternalKey, other.Issue.ExternalKey) {
					return nil, fmt.Errorf("%w: %s is a descendant of %s, which is also selected", ErrValidation, other.Issue.ExternalKey, chosen[i].Issue.ExternalKey)
				}
			}
		}
	}

	out := &IngestSelectedResult{Ingested: make([]IngestedChallenge, 0, len(chosen))}
	err = s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		var chat resolvedChatMark
		if in.Chat != nil {
			var err error
			if chat, err = resolveChatMark(ctx, tx, in.Chat.Session); err != nil {
				return err
			}
		}
		// 上流を読んだ後に課題になっていないことを、このトランザクションの中で読み直す。
		taken, err := loadTakenExternalKeys(ctx, tx)
		if err != nil {
			return err
		}
		out.Ingested = out.Ingested[:0]
		for i, c := range chosen {
			if taken[c.Issue.ExternalKey] {
				return fmt.Errorf("%w: %s became a challenge or a child after the upstream was read", ErrValidation, c.Issue.ExternalKey)
			}
			sb, err := insertIngestedChallenge(ctx, tx, rec, c.Source, c.Issue)
			if err != nil {
				return err
			}
			cid, ok := parseChallengeID(sb.ChallengeID)
			if !ok {
				return fmt.Errorf("core: ingest: malformed challenge id %q", sb.ChallengeID)
			}
			for _, kid := range planned[i] {
				if taken[kid.cand.Issue.ExternalKey] {
					return fmt.Errorf("%w: %s became a challenge or a child after the upstream was read", ErrValidation, kid.cand.Issue.ExternalKey)
				}
				if err := insertSourceChild(ctx, tx, rec.at, cid, kid.cand.Source.ID, kid.cand.Issue, kid.parentKey); err != nil {
					return err
				}
				taken[kid.cand.Issue.ExternalKey] = true
			}
			if in.Chat != nil {
				if err := linkChatChallenge(ctx, tx, chat, cid, chatLinkIngest, formatTimestamp(rec.at)); err != nil {
					return err
				}
			}
			out.Ingested = append(out.Ingested, IngestedChallenge{ChallengeID: sb.ChallengeID, ExternalKey: sb.ExternalKey, Children: len(planned[i])})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
