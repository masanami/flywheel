package cli

import "io"

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
	Run           func(Args) (any, error)
}

// Args はコマンドの Run に渡される、解析済みの引数。
type Args struct {
	Positional []string
	Values     map[string]string
	Bools      map[string]bool
	Stdin      io.Reader
}

// commonFlags はすべてのコマンドに共通のフラグ。
var commonFlags = []flagDef{
	{Name: "workspace", HasValue: true},
	{Name: "json", HasValue: false},
}

// stubRun は本チケットでの未実装コマンドの共通の振る舞い。解析を通過しても
// 何も変更せず internal_error で終わる（後続チケットが実装を差し替える）。
func stubRun(Args) (any, error) {
	return nil, NewError(CodeInternalError, "未実装（後続チケットで実装）")
}

// defaultCommands は docs/features/m1-core.md §IF / API の全コマンドを登録する。
// この表に無いコマンド（例: version）は足さない。
func defaultCommands() []Command {
	return []Command{
		{
			Path: []string{"init"},
			Run:  stubRun,
		},
		{
			Path: []string{"create"},
			Flags: []flagDef{
				{Name: "title", HasValue: true, Required: true},
				{Name: "description", HasValue: true},
				{Name: "done-criteria", HasValue: true},
				{Name: "urgency", HasValue: true},
			},
			Run: stubRun,
		},
		{
			Path:          []string{"show"},
			MinPositional: 1,
			MaxPositional: 1,
			Run:           stubRun,
		},
		{
			Path:  []string{"list"},
			Flags: []flagDef{{Name: "status", HasValue: true}},
			Run:   stubRun,
		},
		{
			Path:          []string{"edit"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "title", HasValue: true},
				{Name: "description", HasValue: true},
				{Name: "done-criteria", HasValue: true},
				{Name: "urgency", HasValue: true},
			},
			Run: stubRun,
		},
		{
			Path:          []string{"classify"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "priority", HasValue: true, Required: true}},
			Run:           stubRun,
		},
		{
			Path:          []string{"plan"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "file", HasValue: true},
				{Name: "stdin", HasValue: false},
			},
			OneOfGroups: [][]string{{"file", "stdin"}},
			Run:         stubRun,
		},
		{
			Path:          []string{"submit"},
			MinPositional: 1,
			MaxPositional: 1,
			Run:           stubRun,
		},
		{
			Path:          []string{"verify"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "result", HasValue: true, Required: true},
				// --question は T10 (verify --result uncertain) にだけ要るが、
				// 省略は validation_failed（usage_error ではない）にすると仕様で
				// 決まっているため、ここでは必須フラグにしない。
				{Name: "question", HasValue: true},
			},
			Run: stubRun,
		},
		{
			Path:          []string{"hold"},
			MinPositional: 1,
			MaxPositional: 1,
			// --question の省略も validation_failed（usage_error ではない）。
			Flags: []flagDef{{Name: "question", HasValue: true}},
			Run:   stubRun,
		},
		{
			Path:          []string{"answer"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "answer", HasValue: true, Required: true}},
			Run:           stubRun,
		},
		{
			Path:          []string{"approve"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "hold-release", HasValue: false}},
			Run:           stubRun,
		},
		{
			Path:          []string{"reject"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags:         []flagDef{{Name: "reason", HasValue: true, Required: true}},
			Run:           stubRun,
		},
		{
			Path:          []string{"op", "add"},
			MinPositional: 1,
			MaxPositional: 1,
			Flags: []flagDef{
				{Name: "kind", HasValue: true, Required: true},
				{Name: "summary", HasValue: true, Required: true},
				{Name: "ref", HasValue: true},
			},
			Run: stubRun,
		},
		{
			Path: []string{"status"},
			Run:  stubRun,
		},
		{
			Path:          []string{"log"},
			MinPositional: 0,
			MaxPositional: 1,
			Run:           stubRun,
		},
	}
}
