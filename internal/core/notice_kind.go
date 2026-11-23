package core

// 起きたことの知らせの種類（画面の知らせの kind）。種類の決めは core が持ち、view と画面は写すだけ。
const (
	NoticeKindTaskAdded           = "task_added"
	NoticeKindReleased            = "released"
	NoticeKindCycleEnded          = "cycle_ended"
	NoticeKindSupervisorEscalated = "supervisor_escalated"
)

// NoticeKindValues は知らせの種類の一覧（画面の表の順）。
func NoticeKindValues() []string {
	return []string{NoticeKindTaskAdded, NoticeKindReleased, NoticeKindCycleEnded, NoticeKindSupervisorEscalated}
}
