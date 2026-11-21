package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// TaskDelegate は課題の詳細のタスクの、最後の契約の実行（contract_run の kind が task で、そのタスクの行のうち ID が最大のもの。終わった行も含む）。
// State・Summary は記録のまま。RequestedKind は委譲先が求めていること（無ければ nil）。RunID は契約の実行を始めた flywheel の run の ID（"R-<n>"）。
type TaskDelegate struct {
	RequestedKind *string
	RunID         string
	State         ContractState
	Summary       string
}

// TaskArtifactView は課題の詳細のタスクの成果物 1 件。Kind・Ref・State は記録のまま。Role は統合先の印（`integration`）か nil。
type TaskArtifactView struct {
	Kind  string
	Ref   string
	Role  *string
	State string
}

// TaskVerificationView はタスクの検証の最後の作業ログ。Verdict・Reason は記録のまま、At は記録の時刻。
type TaskVerificationView struct {
	At      time.Time
	Reason  string
	Verdict string
}

// TaskDetail は課題の詳細（GetChallenge）だけが埋める、タスクごとの委譲先の実行・成果物・値・検証・統合先へ入れる進み。
// 値が無いとき Artifacts は空、Delegate・Landing・Verification は nil、Facts は空の対応。
type TaskDetail struct {
	Artifacts    []TaskArtifactView
	Delegate     *TaskDelegate
	Facts        map[string]string
	Landing      *TaskLanding
	Verification *TaskVerificationView
}

// fillTaskDetails は課題の詳細のために、tasks の各要素の Detail を、契約の実行・成果物・作業ログから組み立てる（読むだけ）。
// usesIntegration は課題が統合先を使うか（nil は使わない扱い）。
func fillTaskDetails(ctx context.Context, tx *sql.Tx, cid int64, tasks []Task, usesIntegration *bool) error {
	for i := range tasks {
		d, err := loadTaskDetail(ctx, tx, cid, &tasks[i], usesIntegration != nil && *usesIntegration)
		if err != nil {
			return err
		}
		tasks[i].Detail = d
	}
	return nil
}

func loadTaskDetail(ctx context.Context, tx *sql.Tx, cid int64, t *Task, integration bool) (TaskDetail, error) {
	d := TaskDetail{Artifacts: []TaskArtifactView{}, Facts: map[string]string{}}
	tid := taskIntID(t)

	var state, summary, reqKind, facts, landRef, verifiedRef string
	var startRun int64
	var ended bool
	err := tx.QueryRowContext(ctx, `SELECT state, summary, COALESCE(requested_kind, ''), COALESCE(facts, ''), COALESCE(land_ref, ''), COALESCE(verified_ref, ''),
		start_run_id, ended_at IS NOT NULL FROM contract_run
		WHERE challenge_id = ? AND task_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, cid, tid, contractRunKindTask).
		Scan(&state, &summary, &reqKind, &facts, &landRef, &verifiedRef, &startRun, &ended)
	switch {
	case err == nil:
		dg := &TaskDelegate{RunID: formatRunID(startRun), State: ContractState(state), Summary: summary}
		if reqKind != "" {
			dg.RequestedKind = &reqKind
		}
		d.Delegate = dg
		if facts != "" {
			var m map[string]string
			if json.Unmarshal([]byte(facts), &m) == nil && m != nil {
				d.Facts = m
			}
		}
		if integration && !ended && t.Status != TaskStatusDone {
			var l TaskLanding
			switch {
			case landRef != "":
				l = TaskLandingSent
			case verifiedRef != "":
				l = TaskLandingVerified
			}
			if l != "" {
				d.Landing = &l
			}
		}
	case err != sql.ErrNoRows:
		return d, err
	}
	if integration && t.Status == TaskStatusDone {
		l := TaskLandingLanded
		d.Landing = &l
	}

	var runID int64
	err = tx.QueryRowContext(ctx, `SELECT r.id FROM run r WHERE r.challenge_id = ? AND r.task_id = ? AND EXISTS (SELECT 1 FROM run_artifact a WHERE a.run_id = r.id)
		ORDER BY r.id DESC LIMIT 1`, cid, tid).Scan(&runID)
	switch {
	case err == nil:
		rows, err := tx.QueryContext(ctx, `SELECT kind, ref, COALESCE(state, ''), COALESCE(role, '') FROM run_artifact WHERE run_id = ? ORDER BY id`, runID)
		if err != nil {
			return d, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a TaskArtifactView
			var role string
			if err := rows.Scan(&a.Kind, &a.Ref, &a.State, &role); err != nil {
				return d, err
			}
			if role == contractRoleIntegration {
				a.Role = &role
			}
			d.Artifacts = append(d.Artifacts, a)
		}
		if err := rows.Err(); err != nil {
			return d, err
		}
	case err != sql.ErrNoRows:
		return d, err
	}

	var at string
	var after sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT at, after FROM activity WHERE entity = 'task' AND entity_id = ? AND action = ? ORDER BY id DESC LIMIT 1`,
		tid, ActivityActionTaskVerify).Scan(&at, &after)
	switch {
	case err == nil:
		var rec struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		}
		if after.Valid {
			_ = json.Unmarshal([]byte(after.String), &rec)
		}
		ts, perr := parseTimestamp(at)
		if perr != nil {
			return d, perr
		}
		d.Verification = &TaskVerificationView{At: ts, Reason: rec.Reason, Verdict: rec.Verdict}
	case err != sql.ErrNoRows:
		return d, err
	}
	return d, nil
}
