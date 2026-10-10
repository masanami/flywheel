package cli

import (
	"fmt"
	"io"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// flagDef は 1 つのフラグの宣言。HasValue が true なら値を取るフラグ
// （例: --title <t>）、false なら真偽の切り替えフラグ（例: --stdin）。
type flagDef struct {
	Name     string
	HasValue bool
	Required bool
}

// Command は 1 つの CLI コマンドの宣言的な定義。
// Path はコマンド名のトークン列（`op add` のような複合コマンドは 2 要素）。
// フラグ・位置引数の個数・OneOfGroups（ちょうど 1 つを要求するフラグの組）
// はここに宣言し、Run には規則を書かない（CLI は core の代わりに規則を
// 実装しない。本チケットでは Run は常にスタブ）。
type Command struct {
	Path          []string
	MinPositional int
	MaxPositional int
	Flags         []flagDef
	OneOfGroups   [][]string
	// RequiresStore が true のコマンドは、Run を呼ぶ前に --workspace の解決規則で
	// ワークスペースのストアを開く（internal/core/internal/store の直輸入は
	// Go の internal 規則で禁止されているため、internal/core の公開 API 経由）。
	// 開けなければ Run を呼ばずに store_not_found・store_too_new・store_busy・
	// store_error を返す（init と読み取りを含む全コマンドが対象＝完了条件）。
	// init だけは false にし、自分自身でワークスペースの作成を行う（PD6:
	// init は親へ遡らない。ワークスペースが無くても新規に作るのが init の役目）。
	// ゼロ値は false（ストアを開かない）。テスト専用のアドホックなコマンドは
	// この既定を利用し、ストアの有無に影響されない。
	RequiresStore bool
	Run           func(Args) (any, error)
}

// Args はコマンドの Run に渡される、解析済みの引数。
type Args struct {
	Positional []string
	Values     map[string]string
	Bools      map[string]bool
	Stdin      io.Reader
	// Store は RequiresStore が true のコマンドにだけ設定される、開いた
	// ワークスペースのストア。Run は Close してはならない（呼び出し元の run が
	// 責任を持つ）。
	Store *core.Store
}

// commonFlags はすべてのコマンドに共通のフラグ。
var commonFlags = []flagDef{
	{Name: "workspace", HasValue: true},
	{Name: "json", HasValue: false},
}

// runInit は `flywheel init` の実装。core.Init を呼ぶだけで、遷移や承認の規則は
// 一切持たない（P2・P4）。
func runInit(a Args) (any, error) {
	res, err := core.Init(a.Values["workspace"])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromInitResult(res),
		text: initText(res),
	}, nil
}

// initText は --json 無しの init の表示（show と同じ「項目: 値」の形）。
func initText(res *core.InitResult) string {
	created := "いいえ（既存のストアをそのまま使います）"
	if res.Created {
		created = "はい"
	}
	return fmt.Sprintf("ワークスペース: %s\nストア:         %s\n新規作成:       %s\n", res.Workspace, res.StorePath, created)
}

// defaultCommands は docs/features/m1-core.md §IF / API の全コマンドを登録する。
// この表に無いコマンド（例: version）は足さない。init 以外はすべて
// RequiresStore: true（完了条件「init と読み取りを含むすべてのコマンドを
// store_too_new で拒否する」の対象。init 自身は自分でワークスペースを作る
// ため false）。
func defaultCommands() []Command {
	return []Command{
		{
			Path: []string{"init"},
			Run:  runInit,
		},
		{
			Path:          []string{"create"},
			RequiresStore: true,
			Flags: []flagDef{
				{Name: "title", HasValue: true, Required: true},
				{Name: "description", HasValue: true},
				{Name: "done-criteria", HasValue: true},
				{Name: "urgency", HasValue: true},
			},
			Run: runCreate,
		},
		{
			Path:          []string{"show"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Run:           runShow,
		},
		{
			Path:          []string{"list"},
			RequiresStore: true,
			Flags:         []flagDef{{Name: "status", HasValue: true}},
			Run:           runList,
		},
		{
			Path:          []string{"edit"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "title", HasValue: true},
				{Name: "description", HasValue: true},
				{Name: "done-criteria", HasValue: true},
				{Name: "urgency", HasValue: true},
			},
			Run: runEdit,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」
			// （#84）が `--auto [<C-ID>]` を足す。`--auto` と `--priority` は
			// OneOfGroups でちょうど1つを要求する（同時指定・両方省略は
			// usage_error）。位置引数の数（0〜1）は「--auto は ID を省略
			// できる・--priority は ID が必須」という条件付きの規則になり
			// 宣言的な MinPositional では表せないため、runClassify が
			// --priority のときだけ位置引数の有無を検査する。
			Path:          []string{"classify"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "priority", HasValue: true},
				{Name: "auto", HasValue: false},
			},
			OneOfGroups: [][]string{{"priority", "auto"}},
			Run:         runClassify,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（#85）が
			// `--auto [<C-ID>]` を足す。`--auto`・`--file`・`--stdin` は OneOfGroups で
			// ちょうど 1 つを要求する（同時指定・すべて省略は usage_error）。位置
			// 引数の数（0〜1）は「--auto は ID を省略できる・--file／--stdin は ID が
			// 必須」という条件付きの規則になり宣言的な MinPositional では表せない
			// ため、runPlan が --file／--stdin のときだけ位置引数の有無を検査する
			// （classify と同じ形）。
			Path:          []string{"plan"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "file", HasValue: true},
				{Name: "stdin", HasValue: false},
				{Name: "auto", HasValue: false},
			},
			OneOfGroups: [][]string{{"file", "stdin", "auto"}},
			Run:         runPlan,
		},
		{
			Path:          []string{"submit"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Run:           runSubmit,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（S2）が
			// `--auto [<C-ID>]` を足す。`--auto` と `--result` は OneOfGroups でちょうど
			// 1 つを要求する（同時指定・両方省略は usage_error）。位置引数の数（0〜1）は
			// 「--auto は ID を省略できる・--result は ID が必須」という条件付きの規則に
			// なるため、runVerify が --result のときだけ位置引数の有無を検査する
			// （classify と同じ形）。
			Path:          []string{"verify"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "result", HasValue: true},
				// --question は T10 (verify --result uncertain) にだけ要るが、
				// 省略は validation_failed（usage_error ではない）にすると仕様で
				// 決まっているため、ここでは必須フラグにしない。
				{Name: "question", HasValue: true},
				{Name: "auto", HasValue: false},
			},
			OneOfGroups: [][]string{{"result", "auto"}},
			Run:         runVerify,
		},
		{
			Path:          []string{"hold"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			// --question の省略も validation_failed（usage_error ではない）。
			Flags: []flagDef{{Name: "question", HasValue: true}},
			Run:   runHold,
		},
		{
			Path:          []string{"answer"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "answer", HasValue: true, Required: true}},
			Run:           runAnswer,
		},
		{
			Path:          []string{"approve"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "hold-release", HasValue: false}},
			Run:           runApprove,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（S2）。本人確認つき。
			Path:          []string{"budget"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "impl-usd", HasValue: true, Required: true},
				{Name: "review-usd", HasValue: true},
			},
			Run: runBudget,
		},
		{
			Path:          []string{"reject"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "reason", HasValue: true, Required: true}},
			Run:           runReject,
		},
		{
			Path:          []string{"op", "add"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "kind", HasValue: true, Required: true},
				{Name: "summary", HasValue: true, Required: true},
				{Name: "ref", HasValue: true},
			},
			Run: runOpAdd,
		},
		{
			Path:          []string{"status"},
			RequiresStore: true,
			Run:           runStatus,
		},
		{
			Path:          []string{"log"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Run:           runLog,
		},
		{
			// Issue #54: 宣言の読み込み・--source の絞り込み・config_*/store_too_new
			// の写像だけを行う（取り込みの本体は #59）。--dry-run は S2 のため
			// ここでは足さない。
			Path:          []string{"ingest"},
			RequiresStore: true,
			Flags:         []flagDef{{Name: "source", HasValue: true}},
			Run:           runIngest,
		},
		{
			// docs/features/m2-github-issue-ingest.md §IF / API「`flywheel mark-read
			// <C-ID>`」（#72）。ネットワークへ接続しない（core.MarkRead は `gh` を
			// 呼ばない＝QH13）。
			Path:          []string{"mark-read"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Run:           runMarkRead,
		},
		{
			// docs/features/m3-invoker-delegation.md §観測・§IF / API「runs」
			// （#81）。`<C-ID>` は省略できる（全件）。
			Path:          []string{"runs"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "open", HasValue: false}},
			Run:           runRuns,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（#86）。位置引数は
			// 取らない。`--trigger` の省略は manual（runCycle）。
			Path:          []string{"cycle"},
			RequiresStore: true,
			Flags:         []flagDef{{Name: "trigger", HasValue: true}},
			Run:           runCycle,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（S2。#102）。
			// `<C-ID>` は省略できる（着手中で承認済みの計画を持つ課題すべて）。
			Path:          []string{"run"},
			RequiresStore: true,
			MinPositional: 0,
			MaxPositional: 1,
			Run:           runRun,
		},
		{
			// docs/features/m3-invoker-delegation.md §IF / API「CLI」（S2）。本人確認も
			// 端末も要らない単発の操作（作業ログには載せない）。
			Path:          []string{"slot", "clear"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Run:           runSlotClear,
		},
	}
}
