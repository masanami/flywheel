package cli

import (
	"io"

	"github.com/masanami/flywheel/internal/core"
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
	return map[string]any{
		"workspace":  res.Workspace,
		"store_path": res.StorePath,
		"created":    res.Created,
	}, nil
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
			Path:          []string{"classify"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "priority", HasValue: true, Required: true}},
			Run:           runClassify,
		},
		{
			Path:          []string{"plan"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "file", HasValue: true},
				{Name: "stdin", HasValue: false},
			},
			OneOfGroups: [][]string{{"file", "stdin"}},
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
			Path:          []string{"verify"},
			RequiresStore: true,
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "result", HasValue: true, Required: true},
				// --question は T10 (verify --result uncertain) にだけ要るが、
				// 省略は validation_failed（usage_error ではない）にすると仕様で
				// 決まっているため、ここでは必須フラグにしない。
				{Name: "question", HasValue: true},
			},
			Run: runVerify,
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
	}
}
