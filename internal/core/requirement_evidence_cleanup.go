package core

import (
	"context"
	"database/sql"
	"errors"
)

// このファイルは、要求の証跡のファイルを消す共通の処理を持つ。区切りを分けない回答と、区切りを分ける保留を開いたままの
// 取り下げは discardRequirementEvidence を呼ぶ。要求の解決は、自分の書き込みのトランザクションの中で、同じ削除
// （removeRequirementEvidence）と作業ログの記録を呼ぶ。

var (
	errRequirementEvidenceOutside    = errors.New("the evidence resolves outside the requirement evidence directory")
	errRequirementEvidenceNotRegular = errors.New("the evidence is not a regular file")
)

// discardRequirementEvidence は証跡の場所 evidencePath のファイルを消す。消すのは、場所のリンクをたどった先が、
// 証跡の置き場の実体の下にある通常のファイルのときだけ。ファイルがすでに無ければ何もしない。消さなかった・消せなかった
// ときは、作業ログに requirement_evidence_cleanup_failed を残す。作業ログの対象は entity と entityID。呼ぶのは、呼び出し元の書き込みが成立した後。誤りは返さない（ctx の取り消しにも
// 従わない）。
func (s *Store) discardRequirementEvidence(ctx context.Context, evidencePath, entity string, entityID int64, actor string) {
	ctx = context.WithoutCancel(ctx)
	failed := s.removeRequirementEvidence(evidencePath)
	if failed == nil {
		return
	}
	// 作業ログも書けなければ、残す先が無い。
	_ = s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(_ *sql.Tx, rec *activityRecorder) error {
		return recordRequirementEvidenceCleanupFailed(rec, entity, entityID, evidencePath, failed)
	})
}

// removeRequirementEvidence は証跡の場所 evidencePath のファイルを、discardRequirementEvidence の守りで消す。
func (s *Store) removeRequirementEvidence(evidencePath string) error {
	return removeRegularFileUnder(s.workspace, evidencePath, requirementEvidenceDir,
		errRequirementEvidenceOutside, errRequirementEvidenceNotRegular)
}

func recordRequirementEvidenceCleanupFailed(rec *activityRecorder, entity string, entityID int64, evidencePath string, failed error) error {
	return rec.record(entity, entityID, ActivityActionRequirementEvidenceCleanupFailed, nil,
		map[string]any{"path": evidencePath, "reason": failed.Error()})
}
