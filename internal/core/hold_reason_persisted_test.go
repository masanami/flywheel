package core

import "testing"

// S3b で足した保留の理由 9 つは、どれも保留の行（hold.reason）に値で書く理由で、発生の種類を持たない（発生を作らない）。
func TestS3bHoldReasonsArePersisted(t *testing.T) {
	t.Parallel()
	for _, r := range []holdReason{
		holdReasonIntegrationRefInvalid, holdReasonChecksFailed, holdReasonLandRefused, holdReasonDelegateEnded, holdReasonMergedUnverified,
		holdReasonReflectUnavailable, holdReasonReflectFailed, holdReasonChallengeCheckNotMet, holdReasonTasksBlocked,
	} {
		if !r.persisted() {
			t.Errorf("%q is not persisted: hold.reason would stay NULL", r)
		}
	}
	for _, r := range []holdReason{holdReasonNone, holdReasonFailureLimit, holdReasonReworkLimit, holdReasonQuestions, holdReasonBlocked, holdReasonUnapprovedMerge} {
		if r.persisted() {
			t.Errorf("%q is persisted although it decides the occurrence kind", r)
		}
	}
}
