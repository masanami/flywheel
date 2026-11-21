package core

// このファイルは、画面と共有する閉集合のうち、タスクと課題の細かい進み・統合先へ入れる進みの値と、
// 既存の閉集合の値の一覧の写しを返す口を持つ。値の決め方（どのタスクがどの値になるか）は、ここでは持たない。

// TaskProgress はツリーのタスクの細かい進み（表示の区分 phase が running のタスクだけ。ほかは null）。
type TaskProgress string

// TaskProgress の 3 値。
const (
	TaskProgressWorking   TaskProgress = "working"
	TaskProgressVerifying TaskProgress = "verifying"
	TaskProgressLanding   TaskProgress = "landing"
)

// TaskProgressValues は TaskProgress の閉集合の写しを返す。
func TaskProgressValues() []TaskProgress {
	return []TaskProgress{TaskProgressWorking, TaskProgressVerifying, TaskProgressLanding}
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
