package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// このファイルは、要求の記録（requirement）と区切りを分ける記録（slice_split）の型・閉集合・値の上限と、
// 要求の記録を作る操作、課題の詳細のための読み取りを持つ（docs/features/request-to-plan-s3.md）。

// requirementEvidenceDir は要求の証跡の置き場（ワークスペースから見た相対のパス）。
const requirementEvidenceDir = flywheelDirName + "/requirements"

// 区切りの案と要求の値の上限（文字数は Unicode のコードポイントの数）。
const (
	// SliceMinCount・SliceMaxCount は 1 つの案の区切りの件数の下限と上限。
	SliceMinCount = 2
	SliceMaxCount = 8
	// SliceTitleMaxRunes は要求の題名と区切りの題名の長さの上限。
	SliceTitleMaxRunes = 200
	// SliceTextMaxRunes は区切りの要旨・範囲と、分ける（分けない）理由の長さの上限。
	SliceTextMaxRunes = 2000
)

// RequirementOrigin は要求の記録の出どころ（requirement.origin）。
type RequirementOrigin string

// RequirementOrigin の 3 値。manual は対話の欄の印の無いときの値で、会話を持たない。
const (
	RequirementOriginIssue  RequirementOrigin = "issue"
	RequirementOriginChat   RequirementOrigin = "chat"
	RequirementOriginManual RequirementOrigin = "manual"
)

// RequirementOriginValues は要求の出どころの閉集合を返す。
func RequirementOriginValues() []RequirementOrigin {
	return []RequirementOrigin{RequirementOriginIssue, RequirementOriginChat, RequirementOriginManual}
}

// SliceSplitState は区切りを分ける記録の状態（slice_split.state）。
type SliceSplitState string

// SliceSplitState の 4 値。
const (
	SliceSplitStatePending       SliceSplitState = "pending"
	SliceSplitStateAwaitingOwner SliceSplitState = "awaiting_owner"
	SliceSplitStateSplit         SliceSplitState = "split"
	SliceSplitStateSingle        SliceSplitState = "single"
)

// SliceSplitStateValues は区切りを分ける記録の状態の閉集合を返す。
func SliceSplitStateValues() []SliceSplitState {
	return []SliceSplitState{SliceSplitStatePending, SliceSplitStateAwaitingOwner, SliceSplitStateSplit, SliceSplitStateSingle}
}

// Requirement は要求の記録。
type Requirement struct {
	ID     string // "RQ-<n>"
	Origin RequirementOrigin
	Title  string
	// ExternalKey・URL は元の Issue の外部キーと URL（出どころ issue だけ）。
	ExternalKey *string
	URL         *string
	// ChatSession は会話の識別（出どころ chat だけ）。
	ChatSession *string
	// OriginChallengeID は取り下げた元の課題の ID（出どころ issue だけ）。
	OriginChallengeID *string
	// EvidencePath は証跡の場所（ワークスペースから見た相対のパス）。
	EvidencePath string
	CreatedAt    time.Time
	// ResolvedAt は解決の日時。解決の前は nil。
	ResolvedAt *time.Time
}

// RequirementDetail は課題の詳細が持つ要求の記録。
type RequirementDetail struct {
	Requirement
	// Challenges は要求の記録を指す課題（ID の昇順。取り下げた元の課題を含まない。無ければ空のスライス）。
	Challenges []RequirementChallenge
}

// RequirementChallenge は要求の記録を指す課題 1 件。
type RequirementChallenge struct {
	ID     string
	Title  string
	Status Status
}

// Slice は区切りの案の 1 つの区切り（slice_split.proposal の slices の要素）。
type Slice struct {
	Title    string `json:"title"`
	Headline string `json:"headline"`
	Context  string `json:"context"`
	Summary  string `json:"summary"`
	Scope    string `json:"scope"`
	Repo     string `json:"repo"`
	// DependsOn は前に済ませる区切りの番号（1 始まり）。
	DependsOn []int `json:"depends_on"`
	// ChallengeID は、この区切りから作った課題の ID。区切りを分ける前は空。
	ChallengeID string `json:"challenge_id,omitempty"`
}

// SliceSplit は区切りを分ける記録のうち、課題の詳細が持つ項目。
type SliceSplit struct {
	State SliceSplitState
	// Slices・Reason は案の区切りの一覧と理由。案が無ければ空のスライスと nil。
	Slices []Slice
	Reason *string
	// EvidencePath は証跡の場所、HoldID は開いている保留の ID（"H-<n>"）。無ければ nil。
	EvidencePath *string
	HoldID       *string
}

var requirementIDPattern = regexp.MustCompile(`^RQ-[1-9][0-9]*$`)

func formatRequirementID(id int64) string { return fmt.Sprintf("RQ-%d", id) }

// parseRequirementID は s を要求の記録の内部の整数の ID として解釈する。形式が不正なら ok=false。
func parseRequirementID(s string) (int64, bool) {
	if !requirementIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "RQ-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// requirementEvidencePath は要求の記録 id の証跡の場所。
func requirementEvidencePath(id int64) string {
	return requirementEvidenceDir + "/" + formatRequirementID(id) + ".md"
}

// CreateRequirementInput は CreateRequirement の入力。
type CreateRequirementInput struct {
	Title string
	// Chat は対話の欄の印。nil でなければ、書き込みの中で印を検査し（通らなければ ErrChatMarkInvalid で何も書かない）、
	// 出どころを chat にして記録に会話を持たせる。nil なら出どころは manual。
	Chat *ChatSessionMark
}

// CreateRequirement は要求の記録を作る。題名は、空白だけでなく SliceTitleMaxRunes 文字以内（外れれば ErrValidation。
// 値は正規化せずに保存する）。証跡のファイルは作らない（書くのは呼んだ側）。
func (s *Store) CreateRequirement(ctx context.Context, ch Channel, in CreateRequirementInput) (*Requirement, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: requirement title is blank", ErrValidation)
	}
	if utf8.RuneCountInString(in.Title) > SliceTitleMaxRunes {
		return nil, fmt.Errorf("%w: requirement title is longer than %d characters", ErrValidation, SliceTitleMaxRunes)
	}
	var result *Requirement
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		origin := RequirementOriginManual
		var chatSessionID any
		var chatSession *string
		if in.Chat != nil {
			mark, err := resolveChatMark(ctx, tx, in.Chat.Session)
			if err != nil {
				return err
			}
			origin, chatSessionID, chatSession = RequirementOriginChat, mark.sessionID, &mark.evidence.Session
		}
		at := formatTimestamp(rec.at)
		createdAt, err := parseTimestamp(at)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO requirement (origin, title, chat_session_id, evidence_path, created_at) VALUES (?, ?, ?, '', ?)`,
			string(origin), in.Title, chatSessionID, at)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		evidence := requirementEvidencePath(id)
		if _, err := tx.ExecContext(ctx, `UPDATE requirement SET evidence_path = ? WHERE id = ?`, evidence, id); err != nil {
			return err
		}
		if err := rec.record("requirement", id, "create", nil,
			map[string]any{"title": in.Title, "origin": string(origin), "evidence_path": evidence}); err != nil {
			return err
		}
		result = &Requirement{
			ID: formatRequirementID(id), Origin: origin, Title: in.Title, ChatSession: chatSession,
			EvidencePath: evidence, CreatedAt: createdAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// openRequirementID は、新しい課題が指せる要求の記録（実在し、解決していないもの）の内部の ID を返す。
// 指せなければ ErrValidation。
func openRequirementID(ctx context.Context, tx *sql.Tx, requirement string) (int64, error) {
	notFound := fmt.Errorf("%w: requirement %q does not exist", ErrValidation, requirement)
	id, ok := parseRequirementID(requirement)
	if !ok {
		return 0, notFound
	}
	var resolved sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT resolved_at FROM requirement WHERE id = ?`, id).Scan(&resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, notFound
	}
	if err != nil {
		return 0, err
	}
	if resolved.Valid {
		return 0, fmt.Errorf("%w: requirement %s is already resolved", ErrValidation, requirement)
	}
	return id, nil
}

// loadChallengeRequirement は課題 cid の詳細に出す要求の記録を読む。課題が指す記録、指していなければ、課題を
// 元の課題とする記録。どちらも無ければ nil。
func loadChallengeRequirement(ctx context.Context, tx *sql.Tx, cid int64) (*RequirementDetail, error) {
	var (
		id                                          int64
		origin, title, evidence, createdAt          string
		externalKey, url, chatSession, resolvedText sql.NullString
		originChallenge                             sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT r.id, r.origin, r.title, r.external_key, r.url, s.engine_session, r.origin_challenge_id,
			r.evidence_path, r.created_at, r.resolved_at
		FROM requirement r LEFT JOIN chat_session s ON s.id = r.chat_session_id
		WHERE r.id = COALESCE(
			(SELECT requirement_id FROM challenge WHERE id = ?),
			(SELECT MAX(id) FROM requirement WHERE origin_challenge_id = ?))`, cid, cid).
		Scan(&id, &origin, &title, &externalKey, &url, &chatSession, &originChallenge, &evidence, &createdAt, &resolvedText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := &RequirementDetail{
		Requirement: Requirement{
			ID: formatRequirementID(id), Origin: RequirementOrigin(origin), Title: title,
			ExternalKey: nullStr(externalKey), URL: nullStr(url), ChatSession: nullStr(chatSession), EvidencePath: evidence,
		},
		Challenges: []RequirementChallenge{},
	}
	if !slices.Contains(RequirementOriginValues(), d.Origin) {
		return nil, fmt.Errorf("requirement %s: invalid origin %q", d.ID, origin)
	}
	if originChallenge.Valid {
		v := formatChallengeID(originChallenge.Int64)
		d.OriginChallengeID = &v
	}
	if d.CreatedAt, err = parseTimestamp(createdAt); err != nil {
		return nil, err
	}
	if resolvedText.Valid {
		t, err := parseTimestamp(resolvedText.String)
		if err != nil {
			return nil, err
		}
		d.ResolvedAt = &t
	}
	// 元の課題が無い記録の origin_challenge_id は NULL。採番は 1 始まりなので 0 はどの課題にも当たらない。
	err = forEachRow(ctx, tx, `SELECT id, title, status FROM challenge WHERE requirement_id = ? AND id <> ? ORDER BY id`,
		[]any{id, originChallenge.Int64}, func(rows *sql.Rows) error {
			var (
				challengeID   int64
				title, status string
			)
			if err := rows.Scan(&challengeID, &title, &status); err != nil {
				return err
			}
			d.Challenges = append(d.Challenges, RequirementChallenge{ID: formatChallengeID(challengeID), Title: title, Status: Status(status)})
			return nil
		})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// loadSliceSplit は課題 cid の区切りを分ける記録を読む。記録が無ければ nil。
func loadSliceSplit(ctx context.Context, tx *sql.Tx, cid int64) (*SliceSplit, error) {
	var (
		state              string
		proposal, evidence sql.NullString
		holdID             sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT state, proposal, evidence_path, hold_id FROM slice_split WHERE challenge_id = ?`, cid).
		Scan(&state, &proposal, &evidence, &holdID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &SliceSplit{State: SliceSplitState(state), Slices: []Slice{}, EvidencePath: nullStr(evidence)}
	if !slices.Contains(SliceSplitStateValues(), out.State) {
		return nil, fmt.Errorf("slice_split of %s: invalid state %q", formatChallengeID(cid), state)
	}
	if holdID.Valid {
		v := formatHoldID(holdID.Int64)
		out.HoldID = &v
	}
	if !proposal.Valid {
		return out, nil
	}
	var p struct {
		Reason *string `json:"reason"`
		Slices []Slice `json:"slices"`
	}
	if err := json.Unmarshal([]byte(proposal.String), &p); err != nil {
		return nil, fmt.Errorf("slice_split of %s: proposal: %w", formatChallengeID(cid), err)
	}
	out.Reason = p.Reason
	out.Slices = append(out.Slices, p.Slices...)
	return out, nil
}
