package core

// このファイルは J5（検証）の「PR のマージ可否」の扱いを持つ。
//
//   - 取得した PR の mergeable・mergeable_state を J5 の区画「PR のマージ可否」へ書く
//   - mergeable が null（GitHub が計算中）の PR は、J5 の起動の前に設定の回数だけ取り直す
//   - 衝突（mergeable=false）の open な PR があれば、J5 の判定（met・uncertain・not_met）に関わらず core が
//     衝突による差し戻しの not_met に置き換える（課題は着手中へ戻り、ブランチの更新が差し戻しの指摘つきの追従の委譲になる。人には問わない）。
//     置き換えた出力には元の判定（conflict_overridden_from）と J5 の元の出力（j5_output）を残し、差し戻しの
//     上限と発生（verification_not_met）には数えない
//   - なお不明（null）のまま met になった課題は、承認の要約に「マージ可否は未確認」を出す
//
// BEHIND（base に遅れているだけ）は衝突として扱わない。取得と正規化は adapter が担い、判定は core が持つ。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 完了の検証（J5）を起動する口（`verify --auto`・周の検証の段）が、mergeable が null のときの読み直しの回数と間隔として渡す値。
const (
	J5MergeableRetries  = 2
	J5MergeableInterval = 3 * time.Second
)

// J5 の区画「PR のマージ可否」のラベル。
const labelPullRequestMergeability = "PR のマージ可否"

// MergeabilityUnconfirmedNote は、マージ可否を確かめられないまま完了確認待ちへ進めた課題の承認の要約に出す文。
const MergeabilityUnconfirmedNote = "マージ可否は未確認"

// activityKeyMergeabilityUnconfirmed は T8（verify_met）の作業ログの after に残す、マージ可否が未確認の
// PR の URL の配列のキー。
const activityKeyMergeabilityUnconfirmed = "mergeability_unconfirmed"

// mergeabilityLabels は PR の mergeable・mergeable_state を、区画に書く GitHub の語（MERGEABLE・
// CONFLICTING・UNKNOWN と mergeStateStatus）へ直す。
func mergeabilityLabels(pr UpstreamPullRequestChecks) (mergeable, state string) {
	switch {
	case pr.Mergeable == nil:
		mergeable = "UNKNOWN"
	case *pr.Mergeable:
		mergeable = "MERGEABLE"
	default:
		mergeable = "CONFLICTING"
	}
	state = strings.ToUpper(strings.TrimSpace(pr.MergeableState))
	if state == "" {
		state = "UNKNOWN"
	}
	return mergeable, state
}

// isOpenPR は pr が open か（マージ可否を気にするのは open の PR だけ）。
func isOpenPR(pr UpstreamPullRequestChecks) bool { return pr.State == string(artifactStateOpen) }

// conflictingPullRequests は prs のうち、base と衝突している（mergeable=false）open な PR。
func conflictingPullRequests(prs []UpstreamPullRequestChecks) []UpstreamPullRequestChecks {
	var out []UpstreamPullRequestChecks
	for _, pr := range prs {
		if isOpenPR(pr) && pr.Mergeable != nil && !*pr.Mergeable {
			out = append(out, pr)
		}
	}
	return out
}

// unconfirmedMergeability は prs のうち、マージ可否を確かめられていない（mergeable が null の）open な PR の URL。
func unconfirmedMergeability(prs []UpstreamPullRequestChecks) []string {
	var out []string
	for _, pr := range prs {
		if isOpenPR(pr) && pr.Mergeable == nil {
			out = append(out, pr.URL)
		}
	}
	return out
}

// formatMergeabilitySection は区画「PR のマージ可否」の本文。open な PR が無ければ "" （区画を入れない）。
func formatMergeabilitySection(prs []UpstreamPullRequestChecks) string {
	var lines []string
	for _, pr := range prs {
		if !isOpenPR(pr) {
			continue
		}
		m, st := mergeabilityLabels(pr)
		line := fmt.Sprintf("PR: %s\nmergeable=%s / mergeStateStatus=%s", pr.URL, m, st)
		if pr.Mergeable == nil {
			line += "\n（GitHub が計算中か取得できず、マージ可否は未確認）"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// refreshUnknownMergeability は mergeable が null の open な PR を、retries 回まで interval を空けて取り直す。
// 取り直しに失敗した PR は、前の値のまま（未確認）にする。retries が 0 以下なら何もしない。
func refreshUnknownMergeability(ctx context.Context, src UpstreamCheckSource, prs []UpstreamPullRequestChecks, retries int, interval time.Duration) []UpstreamPullRequestChecks {
	for n := 0; n < retries && len(unconfirmedMergeability(prs)) > 0; n++ {
		select {
		case <-ctx.Done():
			return prs
		case <-time.After(interval):
		}
		for i, pr := range prs {
			if !isOpenPR(pr) || pr.Mergeable != nil {
				continue
			}
			repo, num, ok := parsePullRequestURL(pr.URL)
			if !ok {
				continue
			}
			if again, err := src.GetPullRequestChecks(ctx, repo, num); err == nil {
				prs[i] = again
			}
		}
	}
	return prs
}

// conflictFeedback は衝突の PR のために core が書く差し戻しの指摘。
func conflictFeedback(conflicts []UpstreamPullRequestChecks) string {
	var b strings.Builder
	b.WriteString("PR が base のブランチと衝突していてマージできない。base の最新を取り込み、衝突を解消して同じブランチへ push すること（達成条件のほかの点は変えない）。")
	for _, pr := range conflicts {
		m, st := mergeabilityLabels(pr)
		fmt.Fprintf(&b, "\n- %s（base: %s、mergeable=%s / mergeStateStatus=%s）", pr.URL, pr.Base, m, st)
	}
	return b.String()
}

// overrideForConflict は取得した PR に衝突があるとき、J5 の出力を not_met（差し戻しの指摘は conflictFeedback）へ
// 置き換えて返し、run の保存済みの出力も同じ内容に書き換える（差し戻しの追従の委譲が、最新の J5 の run の出力から
// 指摘を読むため）。J5 の判定が met・uncertain・not_met のどれでも置き換える。J5 が j5.md の規則に従って衝突を理由に
// 自分で not_met を返す主な経路でも、衝突による差し戻しの印を付けて、差し戻しの上限と発生に数えないため。J5 が
// not_met で指摘を書いていれば、衝突の指摘の後ろにその指摘も残す。置き換えた出力には、J5 の元の判定
// （conflict_overridden_from）と元の出力そのもの（j5_output）を残す。置き換えなければ nil。
func (s *Store) overrideForConflict(ctx context.Context, runIDDisplay string, structured []byte, prs []UpstreamPullRequestChecks) ([]byte, error) {
	conflicts := conflictingPullRequests(prs)
	if len(conflicts) == 0 {
		return nil, nil
	}
	v, ok := validateJ5Output(structured)
	if !ok {
		return nil, nil
	}
	rid, ok := parseRunID(runIDDisplay)
	if !ok {
		return nil, ErrValidation
	}
	feedback := conflictFeedback(conflicts)
	if v.Verdict == J5VerdictNotMet && v.Feedback != nil && strings.TrimSpace(*v.Feedback) != "" {
		feedback += "\n\n（J5 の指摘）\n" + *v.Feedback
	}
	out, err := json.Marshal(map[string]any{
		"verdict":                  string(J5VerdictNotMet),
		"reason":                   "PR が base のブランチと衝突しているため、達成条件を満たさない（" + v.Reason + "）",
		"feedback":                 feedback,
		"conflict_overridden_from": string(v.Verdict),
		"j5_output":                json.RawMessage(structured),
	})
	if err != nil {
		return nil, err
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE run SET output = ? WHERE id = ?`, string(out), rid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// loadMergeabilityUnconfirmed は課題の最新の T8 の作業ログから、マージ可否が未確認のまま完了確認待ちへ
// 進めたかを返す。読めない値は未確認ではないものとして扱い、承認を止めない。
func loadMergeabilityUnconfirmed(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var after sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT after FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ? ORDER BY id DESC LIMIT 1`,
		cid, string(OpVerifyMet)).Scan(&after)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !after.Valid) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(after.String), &m) != nil {
		return false, nil
	}
	var urls []string
	if raw, ok := m[activityKeyMergeabilityUnconfirmed]; !ok || json.Unmarshal(raw, &urls) != nil {
		return false, nil
	}
	return len(urls) > 0, nil
}
