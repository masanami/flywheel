package core

// このファイルは意思決定の主体の判定（親要件チケット #98 §意思決定の主体の判定・
// 決定 M3P21）を持つ。エージェントの運用規約の 5 行の表を、委譲の起動の前に
// core が機械的に評価する。

// Decider は委譲の意思決定の主体（run.decider）の閉集合。
type Decider = decider

// Decider の 3 値。
const (
	DeciderHuman  Decider = deciderHuman
	DeciderParent Decider = deciderParent
	DeciderChild  Decider = deciderChild
)

// DecideDecider は、選ばれた操作の宣言と J2 の出力（複数リポジトリにまたがるか・
// 関係するリポジトリ）から、意思決定者と該当した行の番号（1〜5）を返す。行は上から
// 評価し、最初に一致した行を採る。
//
//  1. 操作が対話前提で、対話相手が human → human
//  2. 複数リポジトリにまたがる（crossRepo、または関係するリポジトリが 2 つ以上）→ parent
//  3. 操作が対話前提（対話相手が parent、または宣言が無い）→ parent
//  4. 操作が対話前提でなく、子に決定を委ねてよいと宣言されている → child
//  5. 上のどれにも当たらない → parent
//
// 宣言の省略は読み込みの時点で既定値（interactive は true、counterpart は parent、
// child_may_decide は false）に置き換わっており、省略を「子が決めてよい」と読まない。
func DecideDecider(op ConnectorOperation, crossRepo bool, relatedRepos []string) (Decider, int) {
	if op.Interactive && op.Counterpart == CounterpartHuman {
		return DeciderHuman, 1
	}
	distinct := map[string]bool{}
	for _, r := range relatedRepos {
		distinct[r] = true
	}
	if crossRepo || len(distinct) >= 2 {
		return DeciderParent, 2
	}
	if op.Interactive {
		return DeciderParent, 3
	}
	if op.ChildMayDecide {
		return DeciderChild, 4
	}
	return DeciderParent, 5
}
