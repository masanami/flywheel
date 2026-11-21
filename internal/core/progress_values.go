package core

// このファイルは、画面と共有する閉集合のうち、タスクと課題の細かい進み・統合先へ入れる進みの値と、
// 既存の閉集合の値の一覧の写しを返す口を持つ。値の決め方（どのタスクがどの値になるか）は、ここでは持たない。

// TaskProgress はツリーのタスクの細かい進み（表示の区分 phase が running のタスクだけ。ほかは null）。
type TaskProgress string

// TaskProgress の 4 値。verified は検証を通っただけ（統合先へ入れる依頼はまだ）、landing は統合先へ入れる依頼を出している。
const (
	TaskProgressWorking   TaskProgress = "working"
	TaskProgressVerifying TaskProgress = "verifying"
	TaskProgressVerified  TaskProgress = "verified"
	TaskProgressLanding   TaskProgress = "landing"
)

// TaskProgressValues は TaskProgress の閉集合の写しを返す。
func TaskProgressValues() []TaskProgress {
	return []TaskProgress{TaskProgressWorking, TaskProgressVerifying, TaskProgressVerified, TaskProgressLanding}
}

// ChallengeProgress はツリーの課題の細かい進み（統合先を使う課題だけ。ほかは null）。
type ChallengeProgress string

// ChallengeProgress の 2 値。
const (
	ChallengeProgressReflecting ChallengeProgress = "reflecting"
	ChallengeProgressChecking   ChallengeProgress = "checking"
)

// ChallengeProgressValues は ChallengeProgress の閉集合の写しを返す。
func ChallengeProgressValues() []ChallengeProgress {
	return []ChallengeProgress{ChallengeProgressReflecting, ChallengeProgressChecking}
}

// TaskLanding はタスクの、統合先へ入れる進み。
type TaskLanding string

// TaskLanding の 3 値。
const (
	TaskLandingVerified TaskLanding = "verified"
	TaskLandingSent     TaskLanding = "sent"
	TaskLandingLanded   TaskLanding = "landed"
)

// TaskLandingValues は TaskLanding の閉集合の写しを返す。
func TaskLandingValues() []TaskLanding {
	return []TaskLanding{TaskLandingVerified, TaskLandingSent, TaskLandingLanded}
}

// VerifyResultValues は VerifyResult（タスクの検証の判定 verdict）の閉集合の写しを返す。
func VerifyResultValues() []VerifyResult {
	return append([]VerifyResult(nil), verifyResultValues...)
}

// ContractStateValues は ContractState（委譲先の実行の状態）の閉集合の写しを返す。
func ContractStateValues() []ContractState {
	return append([]ContractState(nil), contractStateValues...)
}

// OperationKindValues は OperationKind（不可逆操作の種類）の閉集合の写しを返す。
func OperationKindValues() []OperationKind {
	return append([]OperationKind(nil), operationKindValues...)
}

// OperationStateValues は OperationState（不可逆操作の状態）の閉集合の写しを返す。
func OperationStateValues() []OperationState {
	return append([]OperationState(nil), operationStateValues...)
}

// ContractRequestedKindValues は、委譲先が求めること（requested_action.kind）の閉集合の写しを返す。
func ContractRequestedKindValues() []string {
	return append([]string(nil), contractRequestedKindValues...)
}

// ArtifactKindValues は、成果物の種類（run_artifact.kind）の閉集合の写しを返す。
func ArtifactKindValues() []string {
	out := make([]string, 0, len(artifactKindValues))
	for _, k := range artifactKindValues {
		out = append(out, string(k))
	}
	return out
}

// ConflictFixTargetValues は、通したコンフリクトの解消の宛先（ConflictFixPassed.Target）の閉集合の写しを返す。
func ConflictFixTargetValues() []string {
	return append([]string(nil), conflictFixTargetValues...)
}

// ExpectedKindValues は ExpectedKind（予想の例の種別）の閉集合の写しを返す。
func ExpectedKindValues() []ExpectedKind {
	return append([]ExpectedKind(nil), expectedKindValues...)
}
