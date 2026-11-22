package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// このファイルはフォローアップ（課題にぶら下がる 1 行の項目）の core の口を持つ
// （docs/features/core-redesign.md S2b）。フォローアップは課題の状態機械に乗せず、
// 状態は open・done・dropped の 3 つだけ。口はどれも呼び出し側のトランザクションの中で使える形
// （create・complete・drop・promote の小文字の関数が tx と rec を受ける）を正本にし、
// Store のメソッドはそれを 1 つのトランザクションで包むだけにする。

// FollowupStatus はフォローアップの状態。
type FollowupStatus string

// FollowupStatus の 3 値。
const (
	FollowupStatusOpen    FollowupStatus = "open"
	FollowupStatusDone    FollowupStatus = "done"
	FollowupStatusDropped FollowupStatus = "dropped"
)

// FollowupSource はフォローアップの出どころ。
type FollowupSource string

// FollowupSource の値。
const (
	// FollowupSourceLearn は学びの判断点（J6）が書いたフォローアップ。
	FollowupSourceLearn FollowupSource = "learn"
	// FollowupSourceSupervisor は、スーパーバイザーの手 split_followup が、残った条件から作ったフォローアップ（区切り S4b-2 で値だけを足す。
	// 書く手は後のタスク）。
	FollowupSourceSupervisor FollowupSource = "supervisor"
)

// FollowupSourceValues はフォローアップの出どころの閉集合を定義の順に返す（写し）。
func FollowupSourceValues() []FollowupSource {
	return []FollowupSource{FollowupSourceLearn, FollowupSourceSupervisor}
}

// ParseFollowupStatus は s をフォローアップの状態として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseFollowupStatus(s string) (FollowupStatus, bool) {
	switch v := FollowupStatus(strings.TrimSpace(s)); v {
	case FollowupStatusOpen, FollowupStatusDone, FollowupStatusDropped:
		return v, true
	}
	return "", false
}

var followupIDPattern = regexp.MustCompile(`^F-[1-9][0-9]*$`)

func formatFollowupID(id int64) string { return fmt.Sprintf("F-%d", id) }

// parseFollowupID は s をフォローアップの内部整数 ID として解釈する。形式不正は ok=false。
func parseFollowupID(s string) (int64, bool) {
	if !followupIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "F-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Followup はフォローアップの 1 行。
type Followup struct {
	ID string // "F-<n>"
	// ChallengeID はぶら下がる課題（"C-<n>"）。課題に属さないものは nil。
	ChallengeID *string
	Title       string
	Detail      string // 無ければ空の文字列
	Source      FollowupSource
	Status      FollowupStatus
	// Note は済みにしたときの、確かめた結果。
	Note *string
	// Reason は捨てた理由。
	Reason *string
	// PromotedChallengeID は上げた先の課題（"C-<n>"）。上げていなければ nil。
	PromotedChallengeID *string
	// PromotedTaskID は上げた先のタスク（"T-<n>"。出どころが followup の足す依頼で足したタスク）。上げていなければ nil。
	// 上げた先は、これがあればこちら、無ければ PromotedChallengeID と読む。
	PromotedTaskID *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// FollowupInput は createFollowup の入力。
type FollowupInput struct {
	// ChallengeID はぶら下がる課題の ID。空は課題に属さない。存在しなければ ErrNotFound。
	ChallengeID string
	// Title は題名（1 行。必須。空・改行を含むものは ErrValidation）。
	Title string
	// Detail は説明。空なら NULL で書く。
	Detail string
	// Source は出どころ（learn か supervisor。ほかは ErrValidation）。
	Source FollowupSource
	// TaskID はぶら下がるタスクの ID（"T-<n>"。followup.task_id）。空は課題にだけぶら下がる（NULL）。形式の不正は ErrValidation。
	TaskID string
}

// followupTitleValid は題名が空でなく、1 行であることを返す。
func followupTitleValid(title string) bool {
	return strings.TrimSpace(title) != "" && !strings.ContainsAny(title, "\r\n")
}

// createFollowup は状態 open のフォローアップを 1 行書き、作業ログに create を残す。返すのは内部整数 ID。
func createFollowup(ctx context.Context, tx *sql.Tx, rec *activityRecorder, in FollowupInput) (int64, error) {
	if !followupTitleValid(in.Title) || (in.Source != FollowupSourceLearn && in.Source != FollowupSourceSupervisor) {
		return 0, ErrValidation
	}
	var taskID any
	if in.TaskID != "" {
		id, ok := parseTaskID(in.TaskID)
		if !ok {
			return 0, ErrValidation
		}
		taskID = id
	}
	var challenge any
	if in.ChallengeID != "" {
		cid, ok := parseChallengeID(in.ChallengeID)
		if !ok {
			return 0, ErrNotFound
		}
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return 0, err
		}
		challenge = cid
	}
	var detail any
	if in.Detail != "" {
		detail = in.Detail
	}
	nowStr := formatTimestamp(rec.at)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO followup (challenge_id, title, detail, source, status, created_at, updated_at, task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		challenge, in.Title, detail, string(in.Source), string(FollowupStatusOpen), nowStr, nowStr, taskID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	after := map[string]any{"title": in.Title, "source": string(in.Source), "status": string(FollowupStatusOpen)}
	if in.ChallengeID != "" {
		after["challenge_id"] = in.ChallengeID
	}
	if in.Detail != "" {
		after["detail"] = in.Detail
	}
	if in.TaskID != "" {
		after["task_id"] = in.TaskID
	}
	if err := rec.record("followup", id, "create", nil, after); err != nil {
		return 0, err
	}
	return id, nil
}

const followupColumns = `id, challenge_id, title, detail, source, status, note, reason, promoted_challenge_id, promoted_task_id, created_at, updated_at`

type followupScanner interface{ Scan(dest ...any) error }

func scanFollowup(r followupScanner) (*Followup, error) {
	var (
		id                         int64
		challenge, promoted        sql.NullInt64
		promotedTask               sql.NullInt64
		title, source, status      string
		detail, note, reason       sql.NullString
		createdAtStr, updatedAtStr string
	)
	if err := r.Scan(&id, &challenge, &title, &detail, &source, &status, &note, &reason, &promoted, &promotedTask, &createdAtStr, &updatedAtStr); err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp(createdAtStr)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp(updatedAtStr)
	if err != nil {
		return nil, err
	}
	f := &Followup{
		ID: formatFollowupID(id), Title: title, Detail: detail.String,
		Source: FollowupSource(source), Status: FollowupStatus(status),
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if challenge.Valid {
		s := formatChallengeID(challenge.Int64)
		f.ChallengeID = &s
	}
	if promoted.Valid {
		s := formatChallengeID(promoted.Int64)
		f.PromotedChallengeID = &s
	}
	if promotedTask.Valid {
		s := formatTaskID(promotedTask.Int64)
		f.PromotedTaskID = &s
	}
	if note.Valid {
		f.Note = &note.String
	}
	if reason.Valid {
		f.Reason = &reason.String
	}
	return f, nil
}

func loadFollowup(ctx context.Context, tx *sql.Tx, id int64) (*Followup, error) {
	f, err := scanFollowup(tx.QueryRowContext(ctx, `SELECT `+followupColumns+` FROM followup WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// FollowupFilter は ListFollowups の絞り込み。
type FollowupFilter struct {
	// Status は状態。空と all は全部。閉集合外は ErrValidation。
	Status string
	// ChallengeID は課題。空は絞らない。存在しなければ ErrNotFound。
	ChallengeID string
}

func listFollowups(ctx context.Context, tx *sql.Tx, f FollowupFilter) ([]Followup, error) {
	query := `SELECT ` + followupColumns + ` FROM followup WHERE 1 = 1`
	var args []any
	if st := strings.TrimSpace(f.Status); st != "" && st != "all" {
		st, ok := ParseFollowupStatus(f.Status)
		if !ok {
			return nil, ErrValidation
		}
		query += ` AND status = ?`
		args = append(args, string(st))
	}
	if f.ChallengeID != "" {
		cid, ok := parseChallengeID(f.ChallengeID)
		if !ok {
			return nil, ErrNotFound
		}
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return nil, err
		}
		query += ` AND challenge_id = ?`
		args = append(args, cid)
	}
	query += ` ORDER BY id ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Followup{}
	for rows.Next() {
		fu, err := scanFollowup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *fu)
	}
	return out, rows.Err()
}

// ListFollowups はフォローアップを id の昇順で返す。Status が空なら全部（CLI の既定の open は呼び出し側が渡す）。
// 状態だけで絞り、上げてあるものも未のうちに数える。
func (s *Store) ListFollowups(ctx context.Context, f FollowupFilter) ([]Followup, error) {
	var out []Followup
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = listFollowups(ctx, tx, f)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// openFollowupToTriageCond は「未で、オーナーがまだ扱いを決めていない」フォローアップの条件（表の別名は f）。
// 未のうち、上げてあって上げた先の課題が取り下げでないもの（もう扱いを決めて進んでいるもの）を除く。
// 状況（status）の未の件数と、課題のツリーの未のフォローアップの件数が、同じ条件を使う。
const openFollowupToTriageCond = `f.status = '` + string(FollowupStatusOpen) + `' AND (f.promoted_challenge_id IS NULL
	OR EXISTS (SELECT 1 FROM challenge c WHERE c.id = f.promoted_challenge_id AND c.status = '` + string(StatusWithdrawn) + `'))`

// countOpenFollowupsToTriage は、openFollowupToTriageCond に当たるフォローアップの件数。状況（status）の未の件数が使う。
func countOpenFollowupsToTriage(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM followup f WHERE `+openFollowupToTriageCond).Scan(&n)
	return n, err
}

// CountOpenFollowupsToTriage は countOpenFollowupsToTriage を読み取りで返す。
func (s *Store) CountOpenFollowupsToTriage(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = countOpenFollowupsToTriage(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	return n, nil
}

// parseFollowupIDs は ids を内部整数 ID にする。空の指定・形式不正は ErrNotFound（形式不正は存在しないものと同じ扱い）、
// 同じ ID の重複は ErrValidation。返す列は指定の順。
func parseFollowupIDs(ids []string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, s := range ids {
		n, ok := parseFollowupID(s)
		if !ok {
			return nil, ErrNotFound
		}
		if seen[n] {
			return nil, ErrValidation
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// FollowupAttestationTarget は複数のフォローアップの本人確認の対象の文字列（ID を番号の小さい順に並べて
// カンマでつないだもの。1 件ならその ID）を返す。形式不正の ID があれば ok=false。
func FollowupAttestationTarget(ids []string) (string, bool) {
	nums := make([]int64, 0, len(ids))
	for _, s := range ids {
		n, ok := parseFollowupID(s)
		if !ok {
			return "", false
		}
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = formatFollowupID(n)
	}
	return strings.Join(parts, ","), len(parts) > 0
}

// loadOpenFollowups は ids を全部読み、1 つでも存在しなければ ErrNotFound、未でなければ ErrValidation を返す
// （どれも変える前に呼ぶ）。
func loadOpenFollowups(ctx context.Context, tx *sql.Tx, nums []int64) ([]*Followup, error) {
	out := make([]*Followup, 0, len(nums))
	for _, n := range nums {
		f, err := loadFollowup(ctx, tx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	for _, f := range out {
		if f.Status != FollowupStatusOpen {
			return nil, ErrValidation
		}
	}
	return out, nil
}

// closeFollowup は未のフォローアップを status（done・dropped）にして、作業ログに action を残す。
func closeFollowup(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att *Attestation, f *Followup, status FollowupStatus, action, noteCol, text string) error {
	id, _ := parseFollowupID(f.ID)
	res, err := tx.ExecContext(ctx,
		`UPDATE followup SET status = ?, `+noteCol+` = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(status), text, formatTimestamp(rec.at), id, string(FollowupStatusOpen))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrValidation
	}
	after := map[string]any{"status": string(status), noteCol: text}
	if att != nil {
		att.addRelayTo(after)
	}
	return rec.record("followup", id, action, map[string]any{"status": string(FollowupStatusOpen)}, after)
}

// completeFollowups は ids を済みにする（確かめた結果が必須）。1 つでも存在しない（ErrNotFound）・
// 未でない（ErrValidation）なら、どれも変えない。
func completeFollowups(ctx context.Context, tx *sql.Tx, rec *activityRecorder, ids []string, note string) ([]Followup, error) {
	if strings.TrimSpace(note) == "" {
		return nil, ErrValidation
	}
	nums, err := parseFollowupIDs(ids)
	if err != nil {
		return nil, err
	}
	fs, err := loadOpenFollowups(ctx, tx, nums)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if err := closeFollowup(ctx, tx, rec, nil, f, FollowupStatusDone, ActivityActionFollowupDone, "note", note); err != nil {
			return nil, err
		}
	}
	return reloadFollowups(ctx, tx, nums)
}

// dropFollowups は ids を捨てた（dropped）にする（理由が必須）。検査は completeFollowups と同じ。
func dropFollowups(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, ids []string, reason string) ([]Followup, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrValidation
	}
	nums, err := parseFollowupIDs(ids)
	if err != nil {
		return nil, err
	}
	fs, err := loadOpenFollowups(ctx, tx, nums)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if err := closeFollowup(ctx, tx, rec, &att, f, FollowupStatusDropped, ActivityActionFollowupDrop, "reason", reason); err != nil {
			return nil, err
		}
	}
	return reloadFollowups(ctx, tx, nums)
}

func reloadFollowups(ctx context.Context, tx *sql.Tx, nums []int64) ([]Followup, error) {
	out := make([]Followup, 0, len(nums))
	for _, n := range nums {
		f, err := loadFollowup(ctx, tx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, nil
}

// promoteFollowup は未のフォローアップを新しい課題（未分類）に上げる。フォローアップの状態は未のまま、
// 上げた先を記録する。すでに上げてあるものは、上げた先の課題が取り下げのときだけ上げ直せる
// （それ以外は ErrValidation）。作る課題は課題に属すれば出どころ followup（派生元はその課題）、
// 属さなければ manual。
func promoteFollowup(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, id string) (*Challenge, *Followup, error) {
	n, ok := parseFollowupID(id)
	if !ok {
		return nil, nil, ErrNotFound
	}
	f, err := loadFollowup(ctx, tx, n)
	if err != nil {
		return nil, nil, err
	}
	if err := checkPromotable(ctx, tx, f); err != nil {
		return nil, nil, err
	}
	var prevPromoted any
	if f.PromotedChallengeID != nil {
		prevPromoted = *f.PromotedChallengeID
	}
	origin, followupOf := ChallengeOriginManual, (*string)(nil)
	if f.ChallengeID != nil {
		origin, followupOf = ChallengeOriginFollowup, f.ChallengeID
	}
	cid, now, err := insertChallengeRow(ctx, tx, rec.at, f.Title, f.Detail, "", nil, rec.actor, origin, followupOf, nil)
	if err != nil {
		return nil, nil, err
	}
	cafter := createdChallengeAfter(f.Title, f.Detail, "", nil, rec.actor)
	if followupOf != nil {
		cafter["origin"] = string(origin)
		cafter["followup_of"] = *followupOf
	}
	if err := rec.record("challenge", cid, "create", nil, cafter); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE followup SET promoted_challenge_id = ?, updated_at = ? WHERE id = ?`,
		cid, formatTimestamp(rec.at), n); err != nil {
		return nil, nil, err
	}
	fafter := map[string]any{"promoted_challenge_id": formatChallengeID(cid)}
	att.addRelayTo(fafter)
	if err := rec.record("followup", n, ActivityActionFollowupPromote, map[string]any{"promoted_challenge_id": prevPromoted}, fafter); err != nil {
		return nil, nil, err
	}
	updated, err := loadFollowup(ctx, tx, n)
	if err != nil {
		return nil, nil, err
	}
	ch := &Challenge{
		ID: formatChallengeID(cid), Title: f.Title, Description: f.Detail, Status: StatusUnclassified,
		Version: 1, Reporter: rec.actor, CreatedAt: now, UpdatedAt: now,
	}
	return ch, updated, nil
}

// completeFollowupsPromotedTo は、課題 cid が完了（done）になったときに、cid へ上げてある未のフォローアップを
// 済みにする（確かめた結果に上げた先の課題の ID を書き、followup_done を残す）。すでに済み・捨てたものは変えない。
// 課題の遷移の実行の共通の所（runTransition）が、遷移先が done のときに同じトランザクションの中で呼ぶ。
func completeFollowupsPromotedTo(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM followup WHERE promoted_challenge_id = ? AND status = ? ORDER BY id`, cid, string(FollowupStatusOpen))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	note := fmt.Sprintf("上げた先の課題 %s が完了した", formatChallengeID(cid))
	for _, id := range ids {
		f, err := loadFollowup(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := closeFollowup(ctx, tx, rec, nil, f, FollowupStatusDone, ActivityActionFollowupDone, "note", note); err != nil {
			return err
		}
	}
	return nil
}

// CreateFollowup はフォローアップを 1 件書く（状態は open）。
func (s *Store) CreateFollowup(ctx context.Context, ch Channel, in FollowupInput) (*Followup, error) {
	var out *Followup
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		id, err := createFollowup(ctx, tx, rec, in)
		if err != nil {
			return err
		}
		out, err = loadFollowup(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CompleteFollowups は未のフォローアップを済みにする。本人確認は要らない（確かめた結果が必須）。
// 操作した人と経路は作業ログに残す。
func (s *Store) CompleteFollowups(ctx context.Context, ch Channel, ids []string, note string) ([]Followup, error) {
	var out []Followup
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		out, err = completeFollowups(ctx, tx, rec, ids, note)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DropFollowups は未のフォローアップを捨てた（dropped）にする。本人確認の対象は
// FollowupAttestationTarget(ids)。
func (s *Store) DropFollowups(ctx context.Context, ids []string, reason string, att Attestation) ([]Followup, error) {
	target, ok := FollowupAttestationTarget(ids)
	if !ok {
		return nil, ErrNotFound
	}
	if !attestationPermits(att, target, actionFollowupDrop) {
		return nil, ErrVerificationRejected
	}
	var out []Followup
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionFollowupDrop, ""); err != nil {
			return err
		}
		var err error
		out, err = dropFollowups(ctx, tx, rec, att, ids, reason)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PromoteFollowup は未のフォローアップを新しい課題に上げる（1 回に 1 件。本人確認の対象は id）。
func (s *Store) PromoteFollowup(ctx context.Context, id string, att Attestation) (*Challenge, *Followup, error) {
	if !attestationPermits(att, id, actionFollowupPromote) {
		return nil, nil, ErrVerificationRejected
	}
	var (
		ch *Challenge
		fu *Followup
	)
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := s.chatGate(ctx, tx, att, actionFollowupPromote, ""); err != nil {
			return err
		}
		var err error
		ch, fu, err = promoteFollowup(ctx, tx, rec, att, id)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return ch, fu, nil
}

// checkPromotable は f を課題に上げてよいかを検査する。未であること、上げてあるなら上げた先の課題が
// 取り下げであること（それ以外は ErrValidation）。
func checkPromotable(ctx context.Context, tx *sql.Tx, f *Followup) error {
	if f.Status != FollowupStatusOpen {
		return ErrValidation
	}
	if f.PromotedChallengeID == nil {
		return nil
	}
	pid, _ := parseChallengeID(*f.PromotedChallengeID)
	prev, err := loadChallenge(ctx, tx, pid)
	if err != nil {
		return err
	}
	if prev.Status != StatusWithdrawn {
		return ErrValidation
	}
	return nil
}

// PrepareFollowupDrop は捨てる前の本人確認の要約のために、ids のフォローアップを番号の小さい順で返す（読み取りだけ）。
// 理由が空なら ErrValidation、存在しないものがあれば ErrNotFound、未でないものがあれば ErrValidation
// （DropFollowups と同じ規則・同じ順。本人確認を求める前に終えるために使う）。
func (s *Store) PrepareFollowupDrop(ctx context.Context, ids []string, reason string) ([]Followup, error) {
	// 検査の順は dropFollowups と同じ（理由 → ID の形 → 存在 → 未であること）。
	if strings.TrimSpace(reason) == "" {
		return nil, ErrValidation
	}
	if _, ok := FollowupAttestationTarget(ids); !ok {
		return nil, ErrNotFound
	}
	var out []Followup
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		nums, err := parseFollowupIDs(ids)
		if err != nil {
			return err
		}
		sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
		fs, err := loadOpenFollowups(ctx, tx, nums)
		if err != nil {
			return err
		}
		for _, f := range fs {
			out = append(out, *f)
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// PrepareFollowupPromote は課題に上げる前の本人確認の要約のために、id のフォローアップを返す（読み取りだけ）。
// PromoteFollowup と同じ規則（存在しなければ ErrNotFound、上げてよくなければ ErrValidation）で検査する。
func (s *Store) PrepareFollowupPromote(ctx context.Context, id string) (*Followup, error) {
	n, ok := parseFollowupID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *Followup
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		f, err := loadFollowup(ctx, tx, n)
		if err != nil {
			return err
		}
		if err := checkPromotable(ctx, tx, f); err != nil {
			return err
		}
		out = f
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}
