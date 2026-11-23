package view

// このファイルは、画面のボタンで受ける操作（回答・取り込み・サイクルを止める・フォローアップを済みにする／捨てる）と、サイクルの画面の値・起きたことの知らせの
// JSON の形を持つ。API（internal/server）が返す・受ける形で、`flywheel` のコマンドの --json とは共有しない
// （サイクルの画面の値は、CLI の `cycle status --json` の型 CycleStatus に足さず、別の型 CycleScreen に分ける）。

// HoldAnswerRequest は `POST …/challenges/{id}/holds/{hold}/answer` の本文。option は選んだ選択肢の識別、check は画面が保留を読んだときの照合の値
// （Hold.check をそのまま返す）。
type HoldAnswerRequest struct {
	Check  string `json:"check"`
	Option string `json:"option"`
}

// HoldAnswerResponse は選ぶだけの回答が成立したときの本文。hold は回答を記録した保留、cycle は回答の後にサイクルを始めたか（承認の応答と同じ形）。
type HoldAnswerResponse struct {
	Cycle CycleStart `json:"cycle"`
	Hold  Hold       `json:"hold"`
}

// IngestRequest は `POST …/ingest` の本文。items は取り込めるものの指定（`<owner>/<repo>#<番号>` か番号だけ。ターミナルの指定と同じ形）。
type IngestRequest struct {
	Items []string `json:"items"`
}

// IngestAcceptedResponse は取り込みが成立したときの本文（作った課題は指定の順。ターミナルの `ingest <指定>...` の ingested と同じ要素）。
// cycle は取り込みの後にサイクルを始めたか。
type IngestAcceptedResponse struct {
	Cycle    CycleStart     `json:"cycle"`
	Ingested []IngestedItem `json:"ingested"`
}

// CycleStopResponse は `POST …/cycle/stop` の本文。running は走っているサイクルがあったか、requested は止める指示を出したか
// （走っていなければ、何も変えず、どちらも false で cycle_id は null）。
type CycleStopResponse struct {
	CycleID   *string `json:"cycle_id"`
	Requested bool    `json:"requested"`
	Running   bool    `json:"running"`
}

// FollowupDoneRequest は `POST …/followups/done` の本文（ids は済みにするフォローアップ。note は確かめた結果で、空では送れない）。
type FollowupDoneRequest struct {
	IDs  []string `json:"ids"`
	Note string   `json:"note"`
}

// FollowupDropRequest は `POST …/followups/drop` の本文（ids は捨てるフォローアップ。reason は捨てる理由で、空では送れない）。
type FollowupDropRequest struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

// 済みにする・捨てるの成功の本文は、FollowupListResponse（変えたフォローアップ）。

// CycleSupervisorAction は、サイクルの画面のスーパーバイザーの記録の 1 行（対象のサイクルで打った手。新しい順）。
// hand は手の種類（判断できなかったときは null）、findings は調べたことの要旨、result は結果（executed・rejected・failed）、
// reject_reason は断られた理由（断られていなければ null）。task_id・task_title は、タスクに結び付かない手では null。
type CycleSupervisorAction struct {
	At             string  `json:"at"`
	ChallengeID    string  `json:"challenge_id"`
	ChallengeTitle string  `json:"challenge_title"`
	Findings       string  `json:"findings"`
	Hand           *string `json:"hand"`
	ID             int64   `json:"id"`
	RejectReason   *string `json:"reject_reason"`
	Result         string  `json:"result"`
	TaskID         *string `json:"task_id"`
	TaskTitle      *string `json:"task_title"`
}

// CycleCost は、サイクルの画面の費用の内訳の 1 行（対象のサイクルの、終わった実行を種類で分けた額）。
// kind は実行の種類（judgment・delegate・contract）、role は判断点の役割（判断点の実行だけ。それ以外は null）。
// 額は丸める前の値（合計はサイクルの費用と等しい）。
type CycleCost struct {
	CostUSD float64 `json:"cost_usd"`
	Kind    string  `json:"kind"`
	Role    *string `json:"role"`
}

// CycleScreen は `GET …/cycle` の本文。CycleStatus の欄に、対象のサイクル（走っていればそのサイクル、走っていなければ最後のサイクル。
// 個別の操作のために作られた周は除く）の supervisor_actions と costs を足した形。サイクルを一度も回していなければ、どちらも `[]`。
type CycleScreen struct {
	Costs             []CycleCost             `json:"costs"`
	LastEndReason     *string                 `json:"last_end_reason"`
	LastEndedAt       *string                 `json:"last_ended_at"`
	LastResult        *string                 `json:"last_result"`
	LastStartedAt     *string                 `json:"last_started_at"`
	Running           *CycleRunning           `json:"running"`
	State             string                  `json:"state"`
	SupervisorActions []CycleSupervisorAction `json:"supervisor_actions"`
}

// NewCycleScreen は CycleStatus に、サイクルの画面の 2 つの値を足して CycleScreen にする（nil は空の配列）。
func NewCycleScreen(s CycleStatus, actions []CycleSupervisorAction, costs []CycleCost) CycleScreen {
	if actions == nil {
		actions = []CycleSupervisorAction{}
	}
	if costs == nil {
		costs = []CycleCost{}
	}
	return CycleScreen{
		Costs: costs, LastEndReason: s.LastEndReason, LastEndedAt: s.LastEndedAt, LastResult: s.LastResult, LastStartedAt: s.LastStartedAt,
		Running: s.Running, State: s.State, SupervisorActions: actions,
	}
}

// Notice は起きたことの知らせ 1 件（記録から導く。保存しない）。画面が、kind と値から決まった文を組み立てる（core は文を持たない）。
// challenge_id・challenge_title は、サイクルが終わった知らせでは null。task_id・task_title は、タスクに結び付かないとき null。
// added_count・origin は kind が task_added のとき（足した件数と出どころ）、cycle_id・end_reason は kind が cycle_ended のとき
// （終わった理由。CYCLE_END_REASON_CODES の値）に入り、ほかの kind では null。
type Notice struct {
	AddedCount     *int    `json:"added_count"`
	At             string  `json:"at"`
	ChallengeID    *string `json:"challenge_id"`
	ChallengeTitle *string `json:"challenge_title"`
	CycleID        *string `json:"cycle_id"`
	EndReason      *string `json:"end_reason"`
	Kind           string  `json:"kind"`
	Origin         *string `json:"origin"`
	TaskID         *string `json:"task_id"`
	TaskTitle      *string `json:"task_title"`
}

// NoticesResponse は `GET …/notices` の本文（新しい順に、直近の 20 件まで）。
type NoticesResponse struct {
	Notices []Notice `json:"notices"`
}
