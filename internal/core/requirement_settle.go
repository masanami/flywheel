package core

import (
	"context"
	"database/sql"
)

// このファイルは、サイクルの片付けの段（要求の解決）と、その対象の選び方を持つ（docs/features/request-to-plan-s3.md）。
// LLM を呼ばず、上流を読み書きしない。

// requirementSettleWhere は、片付けの段の対象の要求の記録（r）の条件: 解決の日時を持たず、解決した
// （指す課題が 1 件以上あり、すべて終端〔完了か取り下げ〕）。引数は requirementSettleArgs。
const requirementSettleWhere = `r.resolved_at IS NULL
	AND EXISTS (SELECT 1 FROM challenge c WHERE c.requirement_id = r.id)
	AND NOT EXISTS (SELECT 1 FROM challenge c WHERE c.requirement_id = r.id AND c.status NOT IN (?, ?))`

func requirementSettleArgs() []any { return []any{string(StatusDone), string(StatusWithdrawn)} }

type requirementSettleTarget struct {
	id           int64
	evidencePath string
}

// selectRequirementSettleTargets は片付けの段の対象を ID の昇順で返す（段と CountCycleWork が共有する）。
func selectRequirementSettleTargets(ctx context.Context, tx *sql.Tx) ([]requirementSettleTarget, error) {
	var out []requirementSettleTarget
	err := forEachRow(ctx, tx, `SELECT r.id, r.evidence_path FROM requirement r WHERE `+requirementSettleWhere+` ORDER BY r.id`,
		requirementSettleArgs(), func(rows *sql.Rows) error {
			var t requirementSettleTarget
			if err := rows.Scan(&t.id, &t.evidencePath); err != nil {
				return err
			}
			out = append(out, t)
			return nil
		})
	return out, err
}

// settleRequirements は対象の要求の記録ごとに、証跡を消してから解決の日時を書き、日時を書いた記録の ID を返す。
func (s *Store) settleRequirements(ctx context.Context) ([]string, error) {
	var targets []requirementSettleTarget
	err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		targets, err = selectRequirementSettleTargets(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	var resolved []string
	for _, t := range targets {
		marked, err := s.settleRequirement(ctx, actor, t)
		if err != nil {
			return nil, err
		}
		if marked {
			resolved = append(resolved, formatRequirementID(t.id))
		}
	}
	return resolved, nil
}

// settleRequirement は要求の記録 1 件の証跡を消し、解決の日時を書いて、作業ログ requirement_resolved を残す。
// 条件の確かめ直し・削除・書き込みは 1 つの書き込みのトランザクションで行う（課題を足す・直す書き込みと排他になり、
// 解決していない記録の証跡は消えない）。対象の条件を外れていれば、何も消さず何も書かずに false を返す。
// 証跡を消せなくても日時は書く（消せなかったことは作業ログに残り、次の回りで消し直さない）。書き込みが成立しなければ
// 記録は対象のまま残り、次の回りはファイルが無いものとして扱う。
func (s *Store) settleRequirement(ctx context.Context, actor string, t requirementSettleTarget) (bool, error) {
	marked := false
	err := s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		at := formatTimestamp(rec.at)
		res, err := tx.ExecContext(ctx, `UPDATE requirement AS r SET resolved_at = ? WHERE r.id = ? AND `+requirementSettleWhere,
			append([]any{at, t.id}, requirementSettleArgs()...)...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		marked = true
		if failed := s.removeRequirementEvidence(t.evidencePath); failed != nil {
			if err := recordRequirementEvidenceCleanupFailed(rec, "requirement", t.id, t.evidencePath, failed); err != nil {
				return err
			}
		}
		return rec.record("requirement", t.id, ActivityActionRequirementResolved, nil,
			map[string]any{"resolved_at": at, "evidence_path": t.evidencePath})
	})
	if err != nil {
		return false, err
	}
	return marked, nil
}
