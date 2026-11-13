// Package invoker は core.JudgmentInvoker（internal/core/judgment.go）を、
// PATH 上の claude を子プロセスとして起動して実装する。
//
// docs/features/m3-invoker-delegation.md §機能全体の設計「規則は core、
// 入出力はinvoker」に従い、このパッケージは claude の起動・結果の判別・
// 費用の抽出（生の値）・枠超過の判定・出力の保存だけを行い、課題の状態への
// 写像・予算の評価・対象の選び方といった「規則」は一切持たない。依存は
// internal/core（IF・型）と標準ライブラリだけで、ストア
// （internal/core/internal/store）を import しない（import の向き）。
package invoker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ErrClaudeNotFound は PATH 上に claude が見つからなかったことを表す
// sentinel error（errors.Is で判定できる。internal/adapters/github.ErrGHNotFound
// と同じ契約）。
var ErrClaudeNotFound = errors.New("invoker: claude executable not found in PATH")

// defaultInvokeTimeout は呼び出し元が TimeoutSec を渡さなかった（0以下）
// ときに使う最後の砦の上限（判断の呼び出しの既定値900秒。
// docs/features/m3-invoker-delegation.md §IF / API `.flywheel/agent.json`）。
// 本番では #84・#85 が AgentDeclaration.TimeoutSec.Judgment を必ず渡す想定。
const defaultInvokeTimeout = 900

// Launcher は core.JudgmentInvoker の実装。claude の絶対パスをコードに
// 埋め込まず、呼び出しのたびに exec.LookPath で解決する（D13）。
type Launcher struct{}

// NewLauncher は Launcher を返す。
func NewLauncher() *Launcher { return &Launcher{} }

// Available は PATH 上に claude があるかを確かめる。無ければ
// errors.Is(err, ErrClaudeNotFound) が true になるエラーを返す。
func (l *Launcher) Available(_ context.Context) error {
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("%w: %s", ErrClaudeNotFound, err.Error())
	}
	return nil
}
