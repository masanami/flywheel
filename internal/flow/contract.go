package flow

// 接続契約（版 1）の JSON。start・status・resume・cancel は、run の状態（畳み込んだ状態）を契約の形へ写して、
// 標準出力に 1 つ出す。契約の終了コードは「JSON を出力できたか」だけで、待ち・成功・失敗は state に出る。
// flow・steps は任意の欄（版は 1 のまま）。

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/flow/engine"
	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// ContractVersion は出力する接続契約の版。
const ContractVersion = 1

// 契約の requested_action.kind と decider。
const (
	actionAnswer  = "answer"
	actionApprove = "approve"
	actionObserve = "observe"
	// actionReview はレビュー待ち（定義の contract_kind: review のゲート）。flow の呼び出し元は、いつも受け付ける。
	actionReview = "review"

	contractHuman  = "human"
	contractParent = "parent"
)

// 契約の artifacts[].kind。
const (
	artifactPR     = "pr"
	artifactBranch = "branch"
	artifactCommit = "commit"
)

// contractDoc は契約の JSON。RunID は run を始められなかったときは null。Artifacts は空でも [] を出す。
type contractDoc struct {
	ContractVersion int              `json:"contract_version"`
	RunID           *string          `json:"run_id"`
	State           string           `json:"state"`
	Summary         string           `json:"summary"`
	RequestedAction *requestedAction `json:"requested_action"`
	Artifacts       []artifact       `json:"artifacts"`
	CostUSD         *float64         `json:"cost_usd"`
	// Facts は任意のキー（名前 → 値。どちらも文字列）。定義が facts を宣言し、値が 1 つでもあるときだけ出す。
	Facts map[string]string `json:"facts,omitempty"`
	// Flow は始めたフローの名前、Steps は単位 main のステップの実行（始まった順）。run を読めたときだけ出し、
	// ステップがまだ無くても Steps は [] を出す。
	Flow  *string         `json:"flow,omitempty"`
	Steps *[]contractStep `json:"steps,omitempty"`
}

// contractStep は steps の要素（ステップ 1 回ぶん。キーは辞書順）。Seq は step_started の記録の通し番号。
// Effort は変数を指したステップの解決値（claude の --effort に付けた値。指さなければ null）、Model は変数を指したステップの解決値（claude の --model に付けた値。指さなければ null）、ModelsUsed は claude の結果の
// modelUsage の名前（無ければ []）。
type contractStep struct {
	Attempt     int      `json:"attempt"`
	CostUnknown bool     `json:"cost_unknown"`
	CostUSD     *float64 `json:"cost_usd"`
	EndedAt     *string  `json:"ended_at"`
	Effort      *string  `json:"effort"`
	Kind        string   `json:"kind"`
	Model       *string  `json:"model"`
	ModelsUsed  []string `json:"models_used"`
	Outcome     *string  `json:"outcome"`
	Seq         int      `json:"seq"`
	StartedAt   string   `json:"started_at"`
	State       string   `json:"state"`
	Step        string   `json:"step"`
}

// mainUnit は steps の対象の単位の名前。
const mainUnit = engine.MainUnit

// requestedAction は契約の requested_action。ResolvableBy は resume では解けない端末が要るゲートにだけ terminal を出す
// （無ければ answer として扱う）。Options はゲートが受け付ける選択肢を定義の順に出す（選択肢の無い待ちでは出さない）。
type requestedAction struct {
	Kind         string   `json:"kind"`
	Decider      string   `json:"decider"`
	Text         string   `json:"text"`
	ResolvableBy string   `json:"resolvable_by,omitempty"`
	Options      []string `json:"options,omitempty"`
}

const resolvableTerminal = "terminal"

type artifact struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	// Role は、PR・作業ブランチ・commit 以外の役目を持つ要素の印。integration は、統合先を使う run の統合ブランチとその先頭。
	Role string `json:"role,omitempty"`
}

const roleIntegration = "integration"

// waitingView は待っているゲート 1 つ。
type waitingView struct {
	Unit            string
	Gate            string
	Type            string
	Decider         string
	RequestedAction string
	Inputs          []string
	RequiresTTY     bool
	ContractKind    string
	Detail          string
}

// runView は契約へ写す元: 畳み込んだ状態に、待っているゲートと runner の生死を添える。
type runView struct {
	State *runstate.State
	// RunnerAlive は running の run についてだけ入る（runner が居なければ、誰も進めていない）。
	RunnerAlive *bool
	Waiting     []waitingView
	// Kinds は定義のステップ id → 種類（定義を読めなければ空。steps の kind は空になる）。
	Kinds map[string]string
}

func newView(st *runstate.State) *runView {
	v := &runView{State: st, Kinds: stepKinds(st.Workflow.Path, st.Workflow.Hash)}
	if st.Status == runstate.StatusRunning {
		alive := runstate.RunnerAlive(st)
		v.RunnerAlive = &alive
	}
	if !runstate.Terminal(st.Status) {
		for _, u := range st.Units {
			if u.Gate != nil {
				g := u.Gate
				v.Waiting = append(v.Waiting, waitingView{Unit: u.Key, Gate: g.Gate, Type: g.Type, Decider: g.Decider,
					RequestedAction: g.RequestedAction, Inputs: g.Inputs, RequiresTTY: g.RequiresTTY, ContractKind: g.ContractKind, Detail: g.Detail})
			}
		}
	}
	return v
}

// writeContract は JSON を 1 つ出力する。書けなかったときだけ非 0（1）で終わる。
func writeContract(stdout, stderr io.Writer, d contractDoc) int {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "flywheel flow: 契約の JSON を組み立てられない: %v\n", err)
		return ExitFailed
	}
	if _, err := stdout.Write(append(b, '\n')); err != nil {
		_, _ = fmt.Fprintf(stderr, "flywheel flow: 契約の JSON を書けない: %v\n", err)
		return ExitFailed
	}
	return ExitOK
}

// failedDoc は run の状態を読めないときの failed。費用は分からないので null。
func failedDoc(runID *string, why string) contractDoc {
	if why == "" {
		why = "the command failed"
	}
	return contractDoc{ContractVersion: ContractVersion, RunID: runID, State: runstate.StatusFailed, Summary: why, Artifacts: []artifact{}}
}

// contractFromView は run の状態を契約へ写す。note はコマンドが失敗で終わった理由（resume の拒否等）。
// state は run の汎用状態そのもの: コマンドが失敗しても run の状態は変わっていないので、failed にしない。
func contractFromView(v *runView, note string) contractDoc {
	st := v.State
	id := st.RunID
	d := contractDoc{ContractVersion: ContractVersion, RunID: &id, State: st.Status, Artifacts: artifacts(st)}
	cost, unknown := runCost(st)
	d.CostUSD = &cost
	for _, u := range st.Units {
		for k, val := range u.Facts {
			if d.Facts == nil {
				d.Facts = map[string]string{}
			}
			d.Facts[k] = val
		}
	}
	if !runstate.Terminal(st.Status) {
		d.RequestedAction = pickAction(st.RunID, v.Waiting)
	}
	d.Summary = summarize(v, unknown, note)
	flow := st.Workflow.ID
	d.Flow = &flow
	steps := contractSteps(v)
	d.Steps = &steps
	return d
}

// stepKinds は定義（記録したパスの YAML。構文だけ読み、開始時のハッシュと照合する）のステップ id → 種類。読めなければ nil。
func stepKinds(path, hash string) map[string]string {
	if path == "" {
		return nil
	}
	wf, err := workflow.Load(path)
	if err != nil || wf.Hash != hash {
		// 読めない・開始時と違う定義（ハッシュが合わない）からは種類を引かない。
		return nil
	}
	kinds := make(map[string]string, len(wf.Steps))
	for _, s := range wf.Steps {
		kinds[s.ID] = s.Kind
	}
	return kinds
}

// contractSteps は単位 main の全ラウンドのステップの実行を、始まった記録の通し番号の順に写す。
// cost_usd は、そのステップが終わった記録の「実行の累計へ加えた額」（終わっていない・費用を持たない実行は null）。
func contractSteps(v *runView) []contractStep {
	out := []contractStep{}
	u := v.State.Unit(mainUnit)
	if u == nil {
		return out
	}
	for _, r := range u.Rounds {
		for _, x := range r.Steps {
			cs := contractStep{Attempt: x.Attempt, CostUnknown: x.CostUnknown, CostUSD: x.CostUSD, Kind: v.Kinds[x.Step],
				ModelsUsed: []string{}, Seq: x.Seq, StartedAt: x.StartedAt, State: x.Status, Step: x.Step}
			if x.Effort != "" {
				e := x.Effort
				cs.Effort = &e
			}
			if x.Model != "" {
				m := x.Model
				cs.Model = &m
			}
			if len(x.ModelsUsed) > 0 {
				cs.ModelsUsed = append(cs.ModelsUsed, x.ModelsUsed...)
			}
			if x.FinishedAt != "" {
				t := x.FinishedAt
				cs.EndedAt = &t
			}
			if x.Outcome != "" {
				o := x.Outcome
				cs.Outcome = &o
			}
			out = append(out, cs)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// summarize は人が読む 1 行の要約。
func summarize(v *runView, unknownCost int, note string) string {
	st := v.State
	s := fmt.Sprintf("workflow %s: %s", st.Workflow.ID, st.Status)
	switch {
	case st.Reason != "":
		s += " (" + st.Reason + ")"
	case st.Status == runstate.StatusWaiting && len(v.Waiting) > 0:
		w := v.Waiting[0]
		s += fmt.Sprintf(" at gate %s (unit %s)", w.Gate, w.Unit)
	case st.Status == runstate.StatusRunning && len(st.Units) > 0 && st.Units[0].CurrentStep != "":
		s += fmt.Sprintf(" (unit %s at step %s)", st.Units[0].Key, st.Units[0].CurrentStep)
	}
	if v.RunnerAlive != nil && !*v.RunnerAlive {
		s += fmt.Sprintf("; the runner (pid %d) is not alive, so nothing is advancing this run until status or resume", st.PID)
	}
	if unknownCost > 0 {
		s += fmt.Sprintf("; cost_usd counts %d launch(es) whose cost was not reported at the limit granted to them (fail-closed)", unknownCost)
	}
	if note != "" {
		s += "; " + note
	}
	if st.Status == runstate.StatusWaiting && len(v.Waiting) > 0 && v.Waiting[0].Detail != "" {
		s += "; detail: " + v.Waiting[0].Detail
	}
	return s
}

// runCost は run の累計費用（unit の累計の和）と、費用が得られなかった実行の数。費用が得られなかった実行は、付与した
// 上限額を消費したものとして累計に入っている（fail-closed）ので、その額のまま出す（少なく見せない）。
func runCost(st *runstate.State) (float64, int) {
	var cost float64
	unknown := 0
	for _, u := range st.Units {
		if u.Budget != nil {
			cost += u.Budget.SpentUSD
			unknown += u.Budget.UnknownCostCount
		}
	}
	return cost, unknown
}

// artifacts は unit ごとの PR・ブランチ・commit。
func artifacts(st *runstate.State) []artifact {
	out := []artifact{}
	for _, u := range st.Units {
		ws := u.Workspace
		if ws == nil {
			continue
		}
		switch {
		case ws.PRURL != "":
			out = append(out, artifact{Kind: artifactPR, Ref: ws.PRURL})
		case ws.PRNumber > 0:
			out = append(out, artifact{Kind: artifactPR, Ref: "#" + strconv.Itoa(ws.PRNumber)})
		}
		if ws.Branch != "" {
			out = append(out, artifact{Kind: artifactBranch, Ref: ws.Branch})
		}
		if sha := commitRef(ws); sha != "" {
			out = append(out, artifact{Kind: artifactCommit, Ref: sha})
		}
		// 統合ブランチ（名前と先頭は一緒に書かれるので、いつも 1 つずつ出る）。role の無い commit の数には数えない。
		if ws.IntegrationBranch != "" && ws.IntegrationHead != "" {
			out = append(out, artifact{Kind: artifactBranch, Ref: ws.IntegrationBranch, Role: roleIntegration},
				artifact{Kind: artifactCommit, Ref: ws.IntegrationHead, Role: roleIntegration})
		}
	}
	return out
}

// commitRef は成果物の commit の識別: 宛先へ入れて終わった run は入れた後のコミット、そうでなければ PR のいまの先頭。
func commitRef(ws *runstate.Workspace) string {
	switch {
	case ws.MergeCommit != "":
		return ws.MergeCommit
	case ws.HeadSHA != "":
		return ws.HeadSHA
	}
	return lastHead(ws.HeadSHAs)
}

// lastHead は最も新しいラウンドで push した head（キーはラウンド番号）。
func lastHead(heads map[string]string) string {
	best, sha := -1, ""
	for k, v := range heads {
		if n, err := strconv.Atoi(k); err == nil && n > best {
			best, sha = n, v
		}
	}
	return sha
}

// gateAction はゲート 1 つを requested_action へ写す。
//
//	input × human（TTY を要求する）  → approve / human   人が端末から解決する（resolvable_by: terminal）
//	input × parent・any               → answer  / parent  呼び出し元が flywheel flow resume --input で答える
//	observe × human                   → observe / human   人が外部で操作する（例: マージ）。その後 resume で観測し直す
//	observe × parent・any             → observe / parent  外部の状態が変わるのを待つ。resume で観測し直す
//
// answer になるゲートのうち、定義に contract_kind: review と書かれたもの（ゲート名では決めない）は kind を review にする。
// decider・text・解決の仕方は answer と同じ。options は input 型のゲートにだけ、ゲートの inputs を入れる。
func gateAction(runID string, w waitingView) *requestedAction {
	decider := contractParent
	if w.Decider == workflow.DeciderHuman {
		decider = contractHuman
	}
	where := fmt.Sprintf("gate %s, unit %s", w.Gate, w.Unit)
	lead := w.RequestedAction
	if w.Detail != "" {
		// 開き直した理由（gate_opened.detail）を、定義の固定の文（先頭の文）の後ろに足す。
		lead += " Detail: " + w.Detail
	}
	switch {
	case w.Type == workflow.GateObserve:
		return &requestedAction{Kind: actionObserve, Decider: decider, Text: fmt.Sprintf(
			"%s [%s]. Then resume re-checks the state: flywheel flow resume --workspace <workspace> --unit %s %s", lead, where, w.Unit, runID)}
	case w.RequiresTTY:
		return &requestedAction{Kind: actionApprove, Decider: contractHuman, ResolvableBy: resolvableTerminal, Options: options(w.Inputs), Text: fmt.Sprintf(
			"%s [%s; choices: %s]. A person resolves it from a terminal; flywheel flow resume does not resolve it",
			lead, where, strings.Join(w.Inputs, "|"))}
	}
	kind := actionAnswer
	if w.ContractKind == workflow.ContractKindReview {
		kind = actionReview
	}
	return &requestedAction{Kind: kind, Decider: decider, Options: options(w.Inputs), Text: fmt.Sprintf(
		"%s [%s; choices: %s]. Answer with: flywheel flow resume --workspace <workspace> --unit %s --input <%s> [--note <text>] %s%s",
		lead, where, strings.Join(w.Inputs, "|"), w.Unit, strings.Join(w.Inputs, "|"), runID, landNote(w.Inputs))}
}

// landNote は、選択肢に land があるときの添え書き。land は人のマージを通らずに宛先へ入れる道で、承認を持つ呼び出し元だけが、承認した
// 先頭のコミットを --approved-ref に付けて使う（付けなければ engine が断る）。承認を持たない呼び出し元は渡さない。
func landNote(inputs []string) string {
	for _, in := range inputs {
		if in == "land" {
			return ". land enters the destination without a person's merge: only a caller that holds the approval passes it, with --approved-ref <the approved leading commit of the PR>"
		}
	}
	return ""
}

// options は requested_action.options の値: ゲートの inputs の写し。空なら nil にして項目を省く。
func options(inputs []string) []string {
	if len(inputs) == 0 {
		return nil
	}
	return append([]string(nil), inputs...)
}

// actionRank は pickAction の順位（小さいほど先に選ぶ）: 人の承認 → 呼び出し元が答えるもの → レビュー → 観測。
var actionRank = map[string]int{actionApprove: 0, actionAnswer: 1, actionReview: 2, actionObserve: 3}

// pickAction は待っているゲートから 1 つを選ぶ（契約の requested_action は 1 つ）。同じ順位なら unit の順。
// ほかにも待っている unit があれば text に数を添える（fan-out の run。現行のフローの unit は 1 つ）。
func pickAction(runID string, ws []waitingView) *requestedAction {
	if len(ws) == 0 {
		return nil
	}
	acts := make([]*requestedAction, len(ws))
	for i, w := range ws {
		acts[i] = gateAction(runID, w)
	}
	order := make([]int, len(ws))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return actionRank[acts[order[a]].Kind] < actionRank[acts[order[b]].Kind] })
	a := acts[order[0]]
	if n := len(ws) - 1; n > 0 {
		a.Text += fmt.Sprintf(" (%d other unit(s) also wait at gates; see flywheel flow status %s)", n, runID)
	}
	return a
}
