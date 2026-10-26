package core

// このファイルは J6（学習）の本体を持つ。
//
// 1 件の流れ:
//
//	① 課題・承認済みの計画・作業ログ・run の一覧と委譲の報告・承認と保留・未分類の発生を読む
//	② J6 を起動する（課題 1 件につき 1 回。その課題の未分類の発生をまとめて分類する）
//	③ 出力を検査する。発生の ID・分類の抜けと重複・evidence・記憶の規則・フォローアップの件数の
//	   どれかに反すれば、出力全体を invalid_output にして何も書かない
//	④ 1 つのトランザクションの中で、記憶のファイルを書き（元に戻す関数を持つ）、分類の列を埋め、
//	   足す発生を作り、作業ログ（entity=challenge・action=learn）を 1 行残す。どこかが失敗すれば
//	   書いたファイルを戻し、ストアの変更もすべて巻き戻る
//
// 課題の状態と版は変えず、保留も作らない。J6 の失敗は発生を作らない（insertRunFailureOccurrence の
// 許可リストに J6 を入れない）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// NotStartedNotLearnable は、ID を指定した `learn` の課題が対象の条件（状態が完了か人間対応待ちで、
// 未分類の発生を持つ）を満たさず、J6 を起動しなかったことを表す。`cycle` の not_started の閉集合
// （NotStartedReasonValues）に含まれる。
const NotStartedNotLearnable NotStartedReason = "not_learnable"

// J6 が根拠（evidence）に書ける ID の接頭辞。run は R-<n>、承認は AP-<n>、保留は H-<n>、
// 作業ログは L-<n>。発生の ID は O-<n>。いずれも J6 に渡す入力の区画にこの形で載せる。
const (
	j6EvidenceRun      = "R"
	j6EvidenceApproval = "AP"
	j6EvidenceHold     = "H"
	j6EvidenceActivity = "L"
)

var j6EvidenceRe = regexp.MustCompile(`^(R|AP|H|L)-([1-9][0-9]*)$`)

// maxJ6ActivityRows は J6 の入力に載せる作業ログの行数の上限（新しい方から）。
const maxJ6ActivityRows = 200

// maxJ6ReportBytes は J6 の入力に載せる委譲の報告 1 件の大きさの上限。
const maxJ6ReportBytes = 16 * 1024

// maxJ6Followups は J6 が出力してよいフォローアップの件数の上限。これを超える出力は invalid_output。
const maxJ6Followups = 3

// j6Occurrence は J6 に分類させる未分類の発生 1 件。
type j6Occurrence struct {
	ID       int64
	Kind     string
	Outcome  string
	Ref      string // 出来事の記録への参照の説明
	CycleID  *int64
	CauseRun *int64
	At       string
}

type j6Approval struct {
	ID       int64
	Kind     string
	Decision string
	Reason   string
	Fields   string
	At       string
}

type j6Hold struct {
	ID       int64
	Question string
	Answer   *string
	Closed   bool // 回答なしで閉じた（課題の取り下げ）
	From     string
	At       string
}

type j6ActivityRow struct {
	ID     int64
	At     string
	Entity string
	Action string
	Before string
	After  string
}

// j6Context は J6 の起動の前に読む事実。
type j6Context struct {
	Challenge   Challenge
	Plan        *approvedPlan
	Occurrences []j6Occurrence
	Approvals   []j6Approval
	Holds       []j6Hold
	Activities  []j6ActivityRow
	ActivityCut int
	Runs        []runRow
	// Followups はこの課題を followup_of に持つ既存の課題（J6 が前に起票したもの）。ID の昇順。
	Followups []j6Followup
}

// j6Followup はこの課題から起票済みのフォローアップ 1 件（題名と状態）。
type j6Followup struct {
	ID     int64
	Title  string
	Status Status
}

func loadJ6Followups(ctx context.Context, tx *sql.Tx, cid int64) ([]j6Followup, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, title, status FROM challenge WHERE followup_of = ? ORDER BY id ASC`, formatChallengeID(cid))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j6Followup
	for rows.Next() {
		var f j6Followup
		var st string
		if err := rows.Scan(&f.ID, &f.Title, &st); err != nil {
			return nil, err
		}
		f.Status = Status(st)
		out = append(out, f)
	}
	return out, rows.Err()
}

func nullStrValue(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

// loadJ6UnclassifiedOccurrences は課題の未分類の発生（原因が NULL）を ID の昇順で返す。
func loadJ6UnclassifiedOccurrences(ctx context.Context, tx *sql.Tx, cid int64) ([]j6Occurrence, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, kind, outcome, approval_id, hold_id, run_id, execution_id, activity_id, cycle_id, cause_run_id, created_at
		 FROM occurrence WHERE challenge_id = ? AND cause IS NULL ORDER BY id ASC`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j6Occurrence
	for rows.Next() {
		var o j6Occurrence
		var ap, hd, rn, ex, ac, cy, cr sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Kind, &o.Outcome, &ap, &hd, &rn, &ex, &ac, &cy, &cr, &o.At); err != nil {
			return nil, err
		}
		switch {
		case ap.Valid:
			o.Ref = "承認 " + j6EvidenceApproval + "-" + strconv.FormatInt(ap.Int64, 10)
		case hd.Valid:
			o.Ref = "保留 " + j6EvidenceHold + "-" + strconv.FormatInt(hd.Int64, 10)
		case rn.Valid:
			o.Ref = "run " + formatRunID(rn.Int64)
		case ex.Valid:
			o.Ref = "実行の試行 " + strconv.FormatInt(ex.Int64, 10)
		case ac.Valid:
			o.Ref = "作業ログ " + j6EvidenceActivity + "-" + strconv.FormatInt(ac.Int64, 10)
		}
		if cy.Valid {
			v := cy.Int64
			o.CycleID = &v
		}
		if cr.Valid {
			v := cr.Int64
			o.CauseRun = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func loadJ6Approvals(ctx context.Context, tx *sql.Tx, cid int64) ([]j6Approval, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, kind, decision, reason, rejected_fields, decided_at FROM approval WHERE challenge_id = ? ORDER BY id ASC`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j6Approval
	for rows.Next() {
		var a j6Approval
		var reason, fields sql.NullString
		if err := rows.Scan(&a.ID, &a.Kind, &a.Decision, &reason, &fields, &a.At); err != nil {
			return nil, err
		}
		a.Reason, a.Fields = nullStrValue(reason), nullStrValue(fields)
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadJ6Holds(ctx context.Context, tx *sql.Tx, cid int64) ([]j6Hold, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, question, answer, from_status, raised_at, closed_at IS NOT NULL FROM hold WHERE challenge_id = ? ORDER BY id ASC`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j6Hold
	for rows.Next() {
		var h j6Hold
		var ans sql.NullString
		if err := rows.Scan(&h.ID, &h.Question, &ans, &h.From, &h.At, &h.Closed); err != nil {
			return nil, err
		}
		if ans.Valid {
			v := ans.String
			h.Answer = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// j6ActivityScope は課題自身と、課題に従属する不可逆操作の作業ログを選ぶ条件（ListActivities と同じ範囲）。
const j6ActivityScope = `((entity = 'challenge' AND entity_id = ?) OR (entity = 'operation' AND entity_id IN (SELECT id FROM operation WHERE challenge_id = ?)))`

// loadJ6Activities は課題の作業ログを古い順に、新しい方から maxJ6ActivityRows 行だけ返す。
// 切り捨てた行数も返す。
func loadJ6Activities(ctx context.Context, tx *sql.Tx, cid int64) (rowsOut []j6ActivityRow, cut int, err error) {
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity WHERE `+j6ActivityScope, cid, cid).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, at, entity, action, before, after FROM activity WHERE `+j6ActivityScope+` ORDER BY id DESC LIMIT ?`,
		cid, cid, maxJ6ActivityRows)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a j6ActivityRow
		var before, after sql.NullString
		if err := rows.Scan(&a.ID, &a.At, &a.Entity, &a.Action, &before, &after); err != nil {
			return nil, 0, err
		}
		a.Before, a.After = nullStrValue(before), nullStrValue(after)
		rowsOut = append(rowsOut, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(rowsOut)-1; i < j; i, j = i+1, j-1 {
		rowsOut[i], rowsOut[j] = rowsOut[j], rowsOut[i]
	}
	return rowsOut, total - len(rowsOut), nil
}

func loadJ6Runs(ctx context.Context, tx *sql.Tx, cid int64) ([]runRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM run WHERE challenge_id = ? ORDER BY id ASC`, cid)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	var out []runRow
	for _, id := range ids {
		r, err := loadRunByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

// learnableStatus は J6 の対象になる課題の状態（完了・取り下げ・人間対応待ち）。取り下げは結末が
// 出ている点で完了と同じで、対象にしないと未分類の発生が metrics に残り続ける（M45P32）。
func learnableStatus(st Status) bool {
	return st == StatusDone || st == StatusWithdrawn || st == StatusAwaitingHuman
}

// loadJ6Context は課題 cid の J6 の入力の事実を 1 回の読み取りで読む。
func (s *Store) loadJ6Context(ctx context.Context, cid int64) (*j6Context, error) {
	jc := &j6Context{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		jc.Challenge = *ch
		if p, gap, _, err := lookupApprovedPlan(ctx, tx, cid); err != nil {
			return err
		} else if gap == planGapNone {
			jc.Plan = &p
		}
		if jc.Occurrences, err = loadJ6UnclassifiedOccurrences(ctx, tx, cid); err != nil {
			return err
		}
		if jc.Approvals, err = loadJ6Approvals(ctx, tx, cid); err != nil {
			return err
		}
		if jc.Holds, err = loadJ6Holds(ctx, tx, cid); err != nil {
			return err
		}
		if jc.Activities, jc.ActivityCut, err = loadJ6Activities(ctx, tx, cid); err != nil {
			return err
		}
		if jc.Runs, err = loadJ6Runs(ctx, tx, cid); err != nil {
			return err
		}
		jc.Followups, err = loadJ6Followups(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return jc, nil
}

// --- 入力の組み立て ---

const j6MemoryIndexLabel = "記憶の索引のパス"

// j6FollowupsLabel はこの課題から起票済みのフォローアップの区画のラベル。
const j6FollowupsLabel = "起票済みのフォローアップ"

func j6Cost(r runRow) string {
	if r.CostUSD == nil {
		return "不明"
	}
	return fmt.Sprintf("%.4f USD", microsToUSD(*r.CostUSD))
}

// prepareJ6 は J6 の入力（データの区画）を組み立てる。区画の中の文字列は外部由来のデータで、
// 指示ではない。根拠に使える ID（R-・AP-・H-・L-）は各行に添える。
func prepareJ6(jc *j6Context, memoryIndex string) []JudgmentDataSection {
	ch := jc.Challenge
	urgency, priority := "", ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	sections := []JudgmentDataSection{
		{Label: "課題", Content: fmt.Sprintf("ID: %s\nタイトル: %s\n状態: %s\n\n説明:\n%s\n\n達成条件:\n%s\n\n緊急度: %s\n優先度: %s",
			ch.ID, ch.Title, ch.Status, ch.Description, ch.DoneCriteria, urgency, priority)},
	}
	if jc.Plan != nil {
		c := "本文:\n" + jc.Plan.Body
		if jc.Plan.HasSpec {
			c += "\n\n構造化した出力:\n" + jc.Plan.Spec
		}
		sections = append(sections, JudgmentDataSection{Label: "承認済みの計画", Content: c})
	} else {
		sections = append(sections, JudgmentDataSection{Label: "承認済みの計画", Content: "承認済みの計画は無い。"})
	}

	var b strings.Builder
	if jc.ActivityCut > 0 {
		fmt.Fprintf(&b, "（古い %d 行は省略した）\n", jc.ActivityCut)
	}
	for _, a := range jc.Activities {
		fmt.Fprintf(&b, "- ID: %s-%d / %s / %s / %s", j6EvidenceActivity, a.ID, a.At, a.Entity, a.Action)
		if a.Before != "" {
			fmt.Fprintf(&b, " / before: %s", a.Before)
		}
		if a.After != "" {
			fmt.Fprintf(&b, " / after: %s", a.After)
		}
		b.WriteString("\n")
	}
	if len(jc.Activities) == 0 {
		b.WriteString("作業ログは無い。")
	}
	sections = append(sections, JudgmentDataSection{Label: "作業ログ", Content: strings.TrimRight(b.String(), "\n")})

	b.Reset()
	for _, r := range jc.Runs {
		fmt.Fprintf(&b, "- ID: %s / 種別: %s", r.ID, r.Kind)
		if r.Judgment != "" {
			fmt.Fprintf(&b, " / 判断点: %s", r.Judgment)
		}
		fmt.Fprintf(&b, " / 結果: %s / 費用: %s", r.Result, j6Cost(r))
		if r.Kind == runKindDelegate {
			if strings.TrimSpace(r.Output) != "" {
				fmt.Fprintf(&b, "\n  報告: %s", truncateUTF8(r.Output, maxJ6ReportBytes))
			} else {
				b.WriteString("\n  報告: なし")
			}
		}
		b.WriteString("\n")
	}
	if len(jc.Runs) == 0 {
		b.WriteString("run は無い。")
	}
	sections = append(sections, JudgmentDataSection{Label: "実行の一覧と委譲の報告", Content: strings.TrimRight(b.String(), "\n")})

	b.Reset()
	for _, a := range jc.Approvals {
		fmt.Fprintf(&b, "- 承認 ID: %s-%d / 種類: %s / 判断: %s / %s", j6EvidenceApproval, a.ID, a.Kind, a.Decision, a.At)
		if a.Reason != "" {
			fmt.Fprintf(&b, " / 理由: %s", a.Reason)
		}
		if a.Fields != "" {
			fmt.Fprintf(&b, " / ずれた欄: %s", a.Fields)
		}
		b.WriteString("\n")
	}
	for _, h := range jc.Holds {
		fmt.Fprintf(&b, "- 保留 ID: %s-%d / %s / 問い: %s / 回答: ", j6EvidenceHold, h.ID, h.At, h.Question)
		switch {
		case h.Answer != nil:
			b.WriteString(*h.Answer)
		case h.Closed:
			b.WriteString("(回答なしで閉じた)")
		default:
			b.WriteString("(未回答)")
		}
		b.WriteString("\n")
	}
	if len(jc.Approvals)+len(jc.Holds) == 0 {
		b.WriteString("承認も保留も無い。")
	}
	sections = append(sections, JudgmentDataSection{Label: "承認と保留の記録", Content: strings.TrimRight(b.String(), "\n")})

	b.Reset()
	for _, f := range jc.Followups {
		fmt.Fprintf(&b, "- ID: %s / 状態: %s / 題名: %s\n", formatChallengeID(f.ID), f.Status, f.Title)
	}
	if len(jc.Followups) == 0 {
		b.WriteString("既存のフォローアップは無い。")
	}
	sections = append(sections, JudgmentDataSection{Label: j6FollowupsLabel, Content: strings.TrimRight(b.String(), "\n")})

	b.Reset()
	for _, o := range jc.Occurrences {
		fmt.Fprintf(&b, "- ID: %s / 種類: %s / 良し悪し: %s / 参照: %s / %s\n", formatOccurrenceID(o.ID), o.Kind, o.Outcome, o.Ref, o.At)
	}
	sections = append(sections, JudgmentDataSection{Label: "分類待ちの発生", Content: strings.TrimRight(b.String(), "\n")})

	if memoryIndex != "" {
		sections = append(sections, JudgmentDataSection{Label: j6MemoryIndexLabel, Content: "索引のパス: " + memoryIndex})
	}
	return sections
}

// --- 出力の検査と写像 ---

var (
	// errJ6Invalid は出力がストアの事実（発生の ID・evidence）に合わないことを表す。
	errJ6Invalid = errors.New("core: J6 output does not match the store")
	// errJ6NotEligible は写す直前に読み直した課題が対象でなくなっていたことを表す。
	errJ6NotEligible = errors.New("core: challenge is no longer eligible for J6 mapping")
)

// j6ClassifyFault はテストが分類の UPDATE の直前に失敗を注入するフック（tx は写像のトランザクション）。
// 本番では nil。
var j6ClassifyFault func(tx *sql.Tx, occurrenceID int64) error

// j6AfterMappingTx はテストが写像のトランザクションの終了直後（書き込みのロックが外れた後・
// トランザクションの外での戻しの前）に呼ぶフック。本番では nil。
var j6AfterMappingTx func()

type j6Evidence struct {
	ref   OccurrenceRef
	runID *int64
}

// resolveJ6Evidence は evidence が課題 cid の run・作業ログ・承認・保留のいずれかの ID なら、
// 発生の参照にして返す。そうでなければ ok=false。
func resolveJ6Evidence(ctx context.Context, tx *sql.Tx, cid int64, evidence string) (j6Evidence, bool, error) {
	m := j6EvidenceRe.FindStringSubmatch(evidence)
	if m == nil {
		return j6Evidence{}, false, nil
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return j6Evidence{}, false, nil
	}
	var q string
	var args []any
	var ev j6Evidence
	switch m[1] {
	case j6EvidenceRun:
		q, args = `SELECT 1 FROM run WHERE id = ? AND challenge_id = ?`, []any{n, cid}
		ev.ref.RunID, ev.runID = &n, &n
	case j6EvidenceApproval:
		q, args = `SELECT 1 FROM approval WHERE id = ? AND challenge_id = ?`, []any{n, cid}
		ev.ref.ApprovalID = &n
	case j6EvidenceHold:
		q, args = `SELECT 1 FROM hold WHERE id = ? AND challenge_id = ?`, []any{n, cid}
		ev.ref.HoldID = &n
	default:
		q, args = `SELECT 1 FROM activity WHERE id = ? AND `+j6ActivityScope, []any{n, cid, cid}
		ev.ref.ActivityID = &n
	}
	var one int
	switch err := tx.QueryRowContext(ctx, q, args...).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return j6Evidence{}, false, nil
	case err != nil:
		return j6Evidence{}, false, err
	}
	return ev, true, nil
}

func j6MemoryLog(written []memoryWritten) []map[string]any {
	out := make([]map[string]any, 0, len(written))
	for _, w := range written {
		out = append(out, map[string]any{"path": w.Path, "mode": w.Mode, "sha256": w.SHA256})
	}
	return out
}

// applyJ6 は検査を通った出力を 1 つのトランザクションで写す。mapped=false は、課題が対象でなく
// なっていて写さなかったこと。出力がストアの事実に合わなければ invalid=true（何も書かない）。
//
// トランザクションの中の順は、(1) ストアの事実との照合 (2) 記憶のファイルを書く (3) 分類の列・
// 足す発生・作業ログ。(3) のどれかが失敗したら、書いたファイルをトランザクションの中（書き込みの
// ロックの下）で戻す。ロックが外れた後に戻すと、並走する別の写像がその間に書いた索引の行や追記を
// 消しうるため。トランザクションの外で戻すのはコミットが失敗したときだけ。
func (s *Store) applyJ6(ctx context.Context, runIDDisplay string, cid int64, v *j6ValidatedOutput, snapshot []j6Occurrence, decl *AgentDeclaration) (mapped, invalid bool, note string, err error) {
	runInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return false, false, "", ErrValidation
	}
	actor, err := resolveActor()
	if err != nil {
		return false, false, "", err
	}
	var undo func()
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) (ferr error) {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if !learnableStatus(ch.Status) {
			return errJ6NotEligible
		}
		run, err := loadRunByID(ctx, tx, runInt)
		if err != nil {
			return err
		}
		if run == nil {
			return fmt.Errorf("core: J6: run %d not found", runInt)
		}

		// (1) 発生の ID は、J6 に渡した未分類の発生（入力のスナップショット）と過不足なく一致する。
		// 渡した後に足された発生は次の learn に回す。渡した発生がもう未分類でなければ写さない。
		current, err := loadJ6UnclassifiedOccurrences(ctx, tx, cid)
		if err != nil {
			return err
		}
		still := make(map[int64]bool, len(current))
		for _, o := range current {
			still[o.ID] = true
		}
		want := make(map[int64]bool, len(snapshot))
		for _, o := range snapshot {
			if !still[o.ID] {
				return errJ6NotEligible
			}
			want[o.ID] = true
		}
		if len(v.Classifications) != len(snapshot) {
			return errJ6Invalid
		}
		for _, c := range v.Classifications {
			id, ok := parseOccurrenceID(c.OccurrenceID)
			if !ok || !want[id] {
				return errJ6Invalid
			}
			delete(want, id) // 重複は validateJ6Output が落としているが、念のため 2 回目を許さない
		}
		if len(want) != 0 {
			return errJ6Invalid
		}
		evidences := make([]j6Evidence, 0, len(v.Added))
		for _, a := range v.Added {
			ev, ok, err := resolveJ6Evidence(ctx, tx, cid, a.Evidence)
			if err != nil {
				return err
			}
			if !ok {
				return errJ6Invalid
			}
			evidences = append(evidences, ev)
		}

		// (2) 記憶のファイル。検査から書き込みまでを書き込みのロックの下で行う（索引の読み取りと
		// 取り消しの基準が、別の写像の書き込みと食い違わないように）。
		plan, perr := prepareMemoryWrite(s.workspace, decl, v.Memory, rec.at)
		if perr != nil {
			if errors.Is(perr, ErrMemoryWriteRejected) {
				return errJ6Invalid
			}
			return perr
		}
		var written []memoryWritten
		if plan != nil {
			if undo, err = plan.apply(); err != nil {
				return err
			}
			defer func() {
				if ferr != nil {
					undo()
					undo = nil
				}
			}()
			written = plan.written()
		}

		// (3) ストアへの写像。
		var cycleID *int64
		if run.CycleID != nil {
			if n, ok := parseCycleID(*run.CycleID); ok {
				cycleID = &n
			}
		}
		for _, c := range v.Classifications {
			id, _ := parseOccurrenceID(c.OccurrenceID)
			if j6ClassifyFault != nil {
				if err := j6ClassifyFault(tx, id); err != nil {
					return err
				}
			}
			var handled any
			if c.Cause == OccurrenceCauseNone {
				handled = occurrenceHandledNotApplicable
			}
			res, err := tx.ExecContext(ctx,
				`UPDATE occurrence SET cause = ?, layer = ?, target = ?, signal = ?, classified_run_id = ?, handled = ?
				 WHERE id = ? AND challenge_id = ? AND cause IS NULL`,
				string(c.Cause), string(c.Layer), c.Target, c.Signal, runInt, handled, id, cid)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Errorf("core: J6: occurrence %d was not classified", id)
			}
		}
		for i, a := range v.Added {
			if err := s.insertJ6OccurrenceRow(tx, j6OccurrenceInput{
				ChallengeID: cid, CycleID: cycleID, CauseRunID: evidences[i].runID,
				J6Kind: a.Kind, Ref: evidences[i].ref, Outcome: a.Outcome, Cause: a.Cause, Layer: a.Layer,
				Target: a.Target, Signal: a.Signal, ClassifiedRunID: runInt, At: rec.at,
			}); err != nil {
				return err
			}
		}
		// フォローアップは未分類の課題として同じトランザクションで起票する（保留は作らない）。
		filed := []string{}
		source := formatChallengeID(cid)
		for _, f := range v.Followups {
			fid, _, err := insertChallengeRow(ctx, tx, rec.at, f.Title, f.Description, f.DoneCriteria, nil, rec.actor, ChallengeOriginFollowup, &source, nil)
			if err != nil {
				return err
			}
			after := createdChallengeAfter(f.Title, f.Description, f.DoneCriteria, nil, rec.actor)
			after["origin"] = string(ChallengeOriginFollowup)
			after["followup_of"] = source
			if err := rec.record("challenge", fid, ActivityActionFollowup, nil, after); err != nil {
				return err
			}
			filed = append(filed, formatChallengeID(fid))
		}
		return rec.record("challenge", cid, ActivityActionLearn, nil, map[string]any{
			"classified": len(v.Classifications),
			"added":      len(v.Added),
			"memory":     j6MemoryLog(written),
			"followups":  filed,
		})
	})
	if j6AfterMappingTx != nil {
		j6AfterMappingTx()
	}
	if err != nil {
		if undo != nil { // fn は成功したがコミットが失敗した
			undo()
		}
		switch {
		case errors.Is(err, errJ6Invalid):
			return false, true, "", nil
		case errors.Is(err, errJ6NotEligible):
			return false, false, "", nil
		}
		return false, false, "", err
	}
	note = fmt.Sprintf("classified %d, added %d, memory %d", len(v.Classifications), len(v.Added), len(v.Memory))
	return true, false, note, nil
}

// finalizeJ6Output は run が succeeded のときに、出力を検査して写す。検査に落ちたら run の結果を
// invalid_output にして何も書かない。
func (s *Store) finalizeJ6Output(ctx context.Context, in J6AutoInput, runIDDisplay string, cid int64, snapshot []j6Occurrence, structured []byte) (outcome, note string, result RunResult, err error) {
	invalid := func() (string, string, RunResult, error) {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", "", "", err
		}
		return "", "", RunResultInvalidOutput, nil
	}
	v, ok := validateJ6Output(structured)
	if !ok || len(v.Followups) > maxJ6Followups {
		return invalid()
	}
	mapped, bad, note, err := s.applyJ6(ctx, runIDDisplay, cid, v, snapshot, in.AgentDecl)
	if err != nil {
		return "", "", "", err
	}
	if bad {
		return invalid()
	}
	if !mapped {
		return "", "", RunResultSucceeded, nil
	}
	return "learned", note, RunResultSucceeded, nil
}

// --- 対象の選び方と本体 ---

// selectJ6AutoTargets は ID を省略した `learn` が対象にする課題の内部整数 ID を ID の昇順で返す。
// 対象は、未分類の発生を持ち、状態が完了・取り下げ・人間対応待ちの課題。上流の状態による除外はしない
// （完了した課題は上流が closed のことが多い）。
func selectJ6AutoTargets(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT c.id FROM challenge c
		 WHERE c.status IN (?, ?, ?) AND EXISTS (SELECT 1 FROM occurrence o WHERE o.challenge_id = c.id AND o.cause IS NULL)
		 ORDER BY c.id ASC`, string(StatusDone), string(StatusWithdrawn), string(StatusAwaitingHuman))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// J6AutoInput は Store.LearnJ6 の入力。
type J6AutoInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（対象の条件を満たさなければ
	// 理由 not_learnable の NotStarted）。nil なら selectJ6AutoTargets が選ぶ対象すべて。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	// Invoker は J6 の起動の実行者。
	Invoker JudgmentInvoker
	// CycleID はこの操作が属する周。空は ErrValidation。
	CycleID string
	// Cycle が非 nil なら枠超過の状態をこの JudgmentCycle と共有する。
	Cycle *JudgmentCycle
}

// LearnJ6 は `learn` の本体（J6）。対象の選び方・入力・出力の検査と写像は core が持つ。呼び出し元は
// 宣言の読み込み・invoker の組み立て・周の開始と終了だけを行う。
//
//   - in.ChallengeID が非 nil: 対象の条件を満たさなければ理由 not_learnable の NotStarted を返す
//     （エラーにしない）。終了していない run があれば ErrRunInProgress、周の上限で起動できなければ
//     ErrBudgetExceeded を返す。
//   - in.ChallengeID が nil: 対象を ID の昇順に処理し、起動しなかった課題を NotStarted へ入れる。
//
// J6 の結果の失敗が同じ課題で failure_limit 回続いたら、ID を省略した選択ではその課題を対象から外して
// 理由 failure_limit の NotStarted に入れる（ID を指定した個別の操作には当てない）。課題の状態は変えず、保留を作らない。
func (s *Store) LearnJ6(ctx context.Context, in J6AutoInput) (*JudgmentAutoResult, error) {
	if in.AgentDecl == nil || in.Invoker == nil || in.CycleID == "" {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		return nil, ErrValidation
	}
	result := &JudgmentAutoResult{}

	if in.ChallengeID != nil {
		cid, ok := parseChallengeID(*in.ChallengeID)
		if !ok {
			return nil, ErrNotFound
		}
		item, ns, err := s.learnOne(ctx, in, nil, cid, true)
		if err != nil {
			return nil, err
		}
		appendDelegateOutcome(result, item, ns)
		return result, nil
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, err := selectJ6AutoTargets(ctx, tx)
		targetIDs = ids
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	jc := in.Cycle
	if jc == nil {
		jc = NewJudgmentCycle(in.CycleID)
	}
	for _, cid := range targetIDs {
		item, ns, err := s.learnOne(ctx, in, jc, cid, false)
		if err != nil {
			// 1 件の課題の問題で、他の課題の学習を止めない。
			if errors.Is(err, ErrRunInProgress) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		appendDelegateOutcome(result, item, ns)
	}
	return result, nil
}

// j6FailureLimitHit は課題の J6 の連続失敗が上限に達していれば NotStarted（failure_limit）を返す。
// 数え方は loadJudgmentLimitHit（課題の現在の版と、最後の回答済みの保留で区切る）を使う。J6 は
// 課題の状態と版を変えないので、完了の課題では失敗が溜まれば外れたままになり、人間対応待ちの課題では
// 保留への回答（版が変わる）で数え直しになる。
func (s *Store) j6FailureLimitHit(ctx context.Context, ch Challenge, cid int64, decl *AgentDeclaration) (*NotStarted, error) {
	limit := judgmentFailureLimit(decl)
	var hit *judgmentLimitHit
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		h, err := loadJudgmentLimitHit(ctx, tx, cid, ch.Version, JudgmentJ6, limit)
		hit = h
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if hit == nil {
		return nil, nil
	}
	return &NotStarted{ChallengeID: ch.ID, Reason: NotStartedFailureLimit,
		Detail: fmt.Sprintf("%s: %d consecutive failures (limit %d)", hit.Judgment, hit.Count, hit.Limit)}, nil
}

// learnOne は課題 1 件の J6 を実行する。individual（ID を指定した個別の操作）では、枠超過・周の上限を
// エラーで返し、対象外を not_learnable で返す。自動の選択（individual=false）では対象外の課題は
// 黙って除く。item と notStarted はどちらか一方だけが非 nil（対象外で除いたときは両方 nil）。
func (s *Store) learnOne(ctx context.Context, in J6AutoInput, jc *JudgmentCycle, cid int64, individual bool) (*JudgmentAutoItem, *NotStarted, error) {
	if jc != nil && jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: formatChallengeID(cid), Reason: NotStartedRateLimited}, nil
	}
	lc, err := s.loadJ6Context(ctx, cid)
	if err != nil {
		return nil, nil, err
	}
	ch := lc.Challenge
	if !learnableStatus(ch.Status) || len(lc.Occurrences) == 0 {
		if individual {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedNotLearnable}, nil
		}
		return nil, nil, nil
	}
	var active bool
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		a, err := challengeHasActiveRun(ctx, tx, cid)
		active = a
		return err
	})); err != nil {
		return nil, nil, err
	}
	if active {
		return nil, nil, ErrRunInProgress
	}
	// 連続失敗の上限は自動の選択（cycle の learn の段・ID 省略の learn）だけに当てる。ID を指定した個別の
	// 操作には当てない（M3 の「個別の操作には除外を適用しない」に揃える。完了の課題は版が変わらず、
	// 上限に達すると二度と学習できなくなるため）。
	if !individual {
		if ns, err := s.j6FailureLimitHit(ctx, ch, cid, in.AgentDecl); err != nil || ns != nil {
			return nil, ns, err
		}
	}

	budget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ6)
	if err := s.checkCycleBudget(ctx, in.CycleID, budget); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}

	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ6,
		MaxBudgetUSD: budget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     prepareJ6(lc, memoryIndexPath(s.workspace, in.AgentDecl)),
		OutputSchema: j6OutputSchema(),
		Invoker:      in.Invoker,
	}
	var res *RunJudgmentResult
	if jc != nil {
		var ns *NotStarted
		res, ns, err = jc.RunJudgment(ctx, s, runIn)
		if err != nil || ns != nil {
			return nil, ns, err
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		if res, err = s.RunJudgment(ctx, runIn); err != nil {
			return nil, nil, err
		}
	}
	item := &JudgmentAutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		outcome, note, finalResult, err := s.finalizeJ6Output(ctx, in, res.RunID, cid, lc.Occurrences, res.StructuredOutput)
		if err != nil {
			// 写像が硬いエラーで終わった run は、succeeded のままにせず失敗に数える（毎周の呼び直しを
			// failure_limit で止める）。ID を指定した操作はエラーを返し、自動の選択は他の課題を続ける。
			_ = s.invalidateRunOutput(ctx, res.RunID)
			if individual {
				return nil, nil, err
			}
			item.Result, item.Note = RunResultInvalidOutput, "mapping failed: "+err.Error()
			return item, nil, nil
		}
		item.Outcome, item.Note, item.Result = outcome, note, finalResult
	}
	return item, nil, nil
}
