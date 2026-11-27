package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// このファイルは、下書きづくりの記録（表 spec_draft。課題ごとに高々 1 件）の型と読み書き、下書きづくりへ回せるかの判定、
// 計画を作る判断点の入力の区画を持つ。

// SpecDraftState は下書きづくりの記録の状態の閉集合。
type SpecDraftState string

// SpecDraftState の5値。
const (
	SpecDraftStatePending       SpecDraftState = "pending"
	SpecDraftStateRunning       SpecDraftState = "running"
	SpecDraftStateAwaitingOwner SpecDraftState = "awaiting_owner"
	SpecDraftStateDone          SpecDraftState = "done"
	SpecDraftStateSkipped       SpecDraftState = "skipped"
)

// SpecDraftStateValues は SpecDraftState の全値。
var SpecDraftStateValues = []SpecDraftState{SpecDraftStatePending, SpecDraftStateRunning, SpecDraftStateAwaitingOwner, SpecDraftStateDone, SpecDraftStateSkipped}

var specDraftStateLabels = map[SpecDraftState]string{
	SpecDraftStatePending:       "実行待ち",
	SpecDraftStateRunning:       "実行中",
	SpecDraftStateAwaitingOwner: "オーナーの回答待ち",
	SpecDraftStateDone:          "済み",
	SpecDraftStateSkipped:       "省いた",
}

// Label は状態の表示の語を返す。知らない値は値のまま返す。
func (s SpecDraftState) Label() string {
	if l, ok := specDraftStateLabels[s]; ok {
		return l
	}
	return string(s)
}

// blocksPlanning は、この状態の記録を持つ課題を、計画を作る判断点の対象から外すか。
func (s SpecDraftState) blocksPlanning() bool {
	return s == SpecDraftStatePending || s == SpecDraftStateRunning || s == SpecDraftStateAwaitingOwner
}

// SpecDraftHoldReason は下書きづくりの保留の理由の閉集合。
type SpecDraftHoldReason string

// SpecDraftHoldReason の3値。
const (
	SpecDraftHoldQuestion     SpecDraftHoldReason = "spec_question"
	SpecDraftHoldFailed       SpecDraftHoldReason = "spec_failed"
	SpecDraftHoldAttemptLimit SpecDraftHoldReason = "spec_attempt_limit"
)

// SpecDraftHoldReasonValues は SpecDraftHoldReason の全値。
var SpecDraftHoldReasonValues = []SpecDraftHoldReason{SpecDraftHoldQuestion, SpecDraftHoldFailed, SpecDraftHoldAttemptLimit}

const (
	// SpecDraftMaxAttempts は 1 つの記録の実行の回数の上限。
	SpecDraftMaxAttempts = 3
	// MaxSpecDraftReasonRunes は下書きづくりへ回す理由の長さの上限（コードポイント）。
	MaxSpecDraftReasonRunes = 2000
)

// SpecDraftSectionName は、下書きづくりへ回せるかを計画を作る判断点へ渡すデータの区画の名前。
const SpecDraftSectionName = "仕様の下書きづくり"

// specDraftTargetRepoWord は、区画「受入基準」で下書きを作ったリポジトリを示す行の語。
const specDraftTargetRepoWord = "対象のリポジトリ"

// 下書きづくりへ回せない理由（区画に書く文）。
const (
	specDraftUnavailableDone    = "下書きづくりを済ませた"
	specDraftUnavailableSpec    = "仕様の文書がある"
	specDraftUnavailableSkipped = "オーナーが仕様の文書なしで計画すると決めた"
	specDraftUnavailableStarted = "着手したタスクがある"
	specDraftUnavailableRepos   = "対象にできるリポジトリが無い"
	// specDraftUnavailableOpen は、途中の状態の記録を持つ。判断点の対象から外すので、区画には出ない。
	specDraftUnavailableOpen = "下書きづくりの途中"
)

// SpecDraftQuestion は下書きづくりで残った問い 1 件。
type SpecDraftQuestion struct {
	Question string            `json:"question"`
	Options  []SpecDraftOption `json:"options"`
	// Recommended は推奨の選択肢の、1 から数えた位置。
	Recommended int    `json:"recommended"`
	Reason      string `json:"reason"`
}

// SpecDraftOption は問いの選択肢（推奨の印は label に含めない）。
type SpecDraftOption struct {
	Label string `json:"label"`
}

// SpecDraftAnswerKind は答えの一覧の要素の種類の閉集合。
type SpecDraftAnswerKind string

// SpecDraftAnswerKind の2値。
const (
	SpecDraftAnswerKindAnswer SpecDraftAnswerKind = "answer"
	SpecDraftAnswerKindNote   SpecDraftAnswerKind = "note"
)

// SpecDraftAnswer は答えの一覧の要素 1 件。問いへの答えか、オーナーの添え書き。一覧を読む所は Kind で見分ける。
type SpecDraftAnswer struct {
	// Kind は要素の種類。種類を持たずに保存した要素は、記録を読むときに答えにする。
	Kind SpecDraftAnswerKind `json:"kind"`
	// Attempt は何回目の実行の問いか。添え書きでは、その保留を出した実行の回数。
	Attempt int `json:"attempt"`
	// Question は問いの文（添え書きでは空）。Answer は答えの文か、添え書きの文。
	Question string `json:"question"`
	Answer   string `json:"answer"`
	// By は答えた者。知らない値も誤りにせず、そのまま持つ。
	By    string           `json:"by"`
	Basis []SpecDraftBasis `json:"basis"`
}

// SpecDraftBasis は答えの根拠の場所。
type SpecDraftBasis struct {
	Path    string `json:"path"`
	Locator string `json:"locator"`
}

// SpecDraft は下書きづくりの記録。
type SpecDraft struct {
	// Path は下書きの場所（ワークスペースから見た相対のパス）。BasePath は置き換える元の文書の場所（新しい文書は nil）。
	Path     string
	BasePath *string
	Repo     string
	// Reason は下書きづくりへ回した理由。
	Reason   string
	State    SpecDraftState
	Attempts int
	// Questions はいま残っている問い、Answers は答えと添え書き（追記だけ）。無ければ nil。
	Questions []SpecDraftQuestion
	Answers   []SpecDraftAnswer
	// HoldID は開いている下書きづくりの保留の表示用の ID、HoldReason はその理由。開いている保留が無ければどちらも nil。
	HoldID     *string
	HoldReason *SpecDraftHoldReason
}

// specDraftPathFor は課題 cid の下書きの場所。
func specDraftPathFor(cid int64) string {
	return specDraftDir + "/" + strings.ToLower(formatChallengeID(cid)) + ".md"
}

// loadSpecDraft は課題 cid の下書きづくりの記録を返す（無ければ nil）。参照する保留が回答済みか閉じられていれば、
// HoldID と HoldReason は nil。
func loadSpecDraft(ctx context.Context, tx *sql.Tx, cid int64) (*SpecDraft, error) {
	var (
		d                                     SpecDraft
		state                                 string
		basePath, questions, answers, holdWhy sql.NullString
		openHold                              sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT s.path, s.base_path, s.repo, s.reason, s.state, s.attempts, s.questions, s.answers, h.id, s.hold_reason
		FROM spec_draft s LEFT JOIN hold h ON h.id = s.hold_id AND h.answered_at IS NULL AND h.closed_at IS NULL
		WHERE s.challenge_id = ?`, cid).Scan(&d.Path, &basePath, &d.Repo, &d.Reason, &state, &d.Attempts, &questions, &answers, &openHold, &holdWhy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.State = SpecDraftState(state)
	if basePath.Valid {
		d.BasePath = &basePath.String
	}
	if questions.Valid {
		if err := json.Unmarshal([]byte(questions.String), &d.Questions); err != nil {
			return nil, fmt.Errorf("spec_draft questions: %w", err)
		}
	}
	if answers.Valid {
		if err := json.Unmarshal([]byte(answers.String), &d.Answers); err != nil {
			return nil, fmt.Errorf("spec_draft answers: %w", err)
		}
		for i := range d.Answers {
			if d.Answers[i].Kind == "" {
				d.Answers[i].Kind = SpecDraftAnswerKindAnswer
			}
		}
	}
	if openHold.Valid {
		id := formatHoldID(openHold.Int64)
		d.HoldID = &id
		if holdWhy.Valid {
			r := SpecDraftHoldReason(holdWhy.String)
			d.HoldReason = &r
		}
	}
	return &d, nil
}

// insertSpecDraft は課題 cid の下書きづくりの記録を、状態 pending・実行 0 回で作る。d の Path・BasePath・Repo・Reason を使う。
func insertSpecDraft(ctx context.Context, tx *sql.Tx, cid int64, d SpecDraft, at time.Time) error {
	ts := formatTimestamp(at)
	_, err := tx.ExecContext(ctx, `INSERT INTO spec_draft (challenge_id, path, base_path, repo, reason, state, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`, cid, d.Path, d.BasePath, d.Repo, d.Reason, string(SpecDraftStatePending), ts, ts)
	return err
}

// specDraftUpdate は記録の、作った後に変わる値の全部。HoldRowID は開いている下書きづくりの保留の行の番号。
type specDraftUpdate struct {
	State      SpecDraftState
	Attempts   int
	Questions  []SpecDraftQuestion
	Answers    []SpecDraftAnswer
	HoldRowID  *int64
	HoldReason *SpecDraftHoldReason
}

// updateSpecDraft は課題 cid の記録の、状態・回数・問い・答え・保留を書き換える。場所・リポジトリ・回した理由は変えない。
// 閉集合の外の値は ErrValidation、記録が無ければ ErrNotFound。状態の移り方は呼び出し元が決める。
func updateSpecDraft(ctx context.Context, tx *sql.Tx, cid int64, u specDraftUpdate, at time.Time) error {
	if !slices.Contains(SpecDraftStateValues, u.State) {
		return fmt.Errorf("%w: spec draft state %q", ErrValidation, u.State)
	}
	if u.HoldReason != nil && !slices.Contains(SpecDraftHoldReasonValues, *u.HoldReason) {
		return fmt.Errorf("%w: spec draft hold reason %q", ErrValidation, *u.HoldReason)
	}
	// 空の一覧は NULL にする。
	list := func(n int, v any) (any, error) {
		if n == 0 {
			return nil, nil
		}
		b, err := json.Marshal(v)
		return string(b), err
	}
	questions, err := list(len(u.Questions), u.Questions)
	if err != nil {
		return err
	}
	answers, err := list(len(u.Answers), u.Answers)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE spec_draft SET state = ?, attempts = ?, questions = ?, answers = ?, hold_id = ?, hold_reason = ?, updated_at = ?
		WHERE challenge_id = ?`, string(u.State), u.Attempts, questions, answers, u.HoldRowID, u.HoldReason, formatTimestamp(at), cid)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// specDraftTargetRepos は下書きづくりの対象にできるリポジトリの名前（宣言の順）。形が flow の委譲先を使い、
// スロットの形が作業ツリーのものだけ（クローンの形は作業用のクローンしか持たず、タスクのブランチの中身を読みうる）。
func specDraftTargetRepos(conn *ConnectorsDeclaration) []string {
	var out []string
	for _, r := range conn.Repos {
		_, c := conn.findConnectorRepo(r.Name)
		if c != nil && c.Form == ConnectorFormFlow && slotProvider(r.Slots.Provider) == slotProviderWorktree {
			out = append(out, r.Name)
		}
	}
	return out
}

// specDraftUnavailable は、課題を下書きづくりへ回せない理由を返す（回せるなら空）。複数に当たるときは最初のもの。
// d は課題の記録（無ければ nil）、tasks は課題の全部のタスク、repos は対象にできるリポジトリ。
func specDraftUnavailable(d *SpecDraft, specPath *string, tasks []Task, repos []string) string {
	switch {
	case d != nil && d.State == SpecDraftStateDone:
		return specDraftUnavailableDone
	case specPath != nil:
		return specDraftUnavailableSpec
	case d != nil && d.State == SpecDraftStateSkipped:
		return specDraftUnavailableSkipped
	case d != nil:
		return specDraftUnavailableOpen
	case slices.ContainsFunc(tasks, taskStarted):
		return specDraftUnavailableStarted
	case len(repos) == 0:
		return specDraftUnavailableRepos
	}
	return ""
}

// taskStarted はタスクが着手済み（着手中・検証中・済み）か。
func taskStarted(t Task) bool {
	return t.Status == TaskStatusInProgress || t.Status == TaskStatusVerifying || t.Status == TaskStatusDone
}

// formatSpecDraftSection は区画「仕様の下書きづくり」の本文。unavailable が空なら回せる。
func formatSpecDraftSection(unavailable string, repos []string) string {
	if unavailable != "" {
		return "回せない\n理由: " + unavailable
	}
	var b strings.Builder
	b.WriteString("回せる\n対象にできるリポジトリ:")
	for _, r := range repos {
		b.WriteString("\n- " + oneLine(r))
	}
	return b.String()
}

// withSpecDraftTargetRepo は区画「受入基準」の本文 section の見出し（最初の空の行の前）に、下書きを作ったリポジトリの行を足す。
func withSpecDraftTargetRepo(section, repo string) string {
	head, items, _ := strings.Cut(section, "\n\n")
	return head + "\n" + specDraftTargetRepoWord + ": " + oneLine(repo) + "\n\n" + items
}

// specDraftBlocksPlanning は課題 cid が途中の状態の下書きづくりの記録を持つか。
func specDraftBlocksPlanning(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil || d == nil {
		return false, err
	}
	return d.State.blocksPlanning(), nil
}

// errJ2SpecDraftNotMapped は、写す直前に読み直した課題が下書きづくりへ回せなくなっていたことを表す内部の sentinel。
var errJ2SpecDraftNotMapped = errors.New("core: challenge is no longer eligible for spec drafting")

// applyJ2NeedsSpec は判定 needs_spec の出力を課題へ写す。計画を登録せず、課題の状態と版を変えずに、下書きづくりの記録を作って
// 作業ログに残す。写す直前に読み直した課題が分類済でないか、回せる条件を外れていれば写さない。
func (s *Store) applyJ2NeedsSpec(ctx context.Context, runIDDisplay, challengeIDDisplay string, out j2SpecDraft) (mapped bool, err error) {
	runID, ok := parseRunID(runIDDisplay)
	if !ok {
		return false, ErrValidation
	}
	cid, ok := parseChallengeID(challengeIDDisplay)
	if !ok {
		return false, ErrNotFound
	}
	actor, err := s.resolveActor()
	if err != nil {
		return false, err
	}
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runID, func(tx *sql.Tx, rec *activityRecorder) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		d, err := loadSpecDraft(ctx, tx, cid)
		if err != nil {
			return err
		}
		tasks, err := listTasks(ctx, tx, cid)
		if err != nil {
			return err
		}
		// 対象のリポジトリは出力の検査で確かめた（repos はここでは見ない）。
		if ch.Status != StatusClassified || specDraftUnavailable(d, ch.SpecPath, tasks, []string{out.Repo}) != "" {
			return errJ2SpecDraftNotMapped
		}
		record := SpecDraft{Path: specDraftPathFor(cid), BasePath: out.BasePath, Repo: out.Repo, Reason: out.Reason}
		if err := insertSpecDraft(ctx, tx, cid, record, rec.at); err != nil {
			return err
		}
		return rec.record("challenge", cid, ActivityActionSpecDraftRequested, nil,
			map[string]any{"path": record.Path, "base_path": record.BasePath, "repo": record.Repo, "reason": record.Reason})
	})
	if errors.Is(err, errJ2SpecDraftNotMapped) {
		return false, nil
	}
	return err == nil, err
}
