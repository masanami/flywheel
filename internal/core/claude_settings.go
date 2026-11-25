package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// このファイルは flywheel init が作る Claude Code の設定のファイル（docs/features/chat-session.md「init が作る設定」）を
// 持つ。Claude Code に固有の形は invoker に閉じる決まりの例外（init は core の口で、invoker は init から呼ばれない）。

// claudeSettingsContents は対話の欄で、尋ねずに使える操作を決める設定。ファイルの編集やほかのコマンドは含めない。
const claudeSettingsContents = `{
  "permissions": {
    "allow": [
      "Bash(flywheel:*)",
      "Bash(gh issue view:*)",
      "Bash(gh issue list:*)",
      "Bash(gh pr view:*)",
      "Bash(gh pr list:*)",
      "Bash(gh pr checks:*)",
      "Bash(gh pr diff:*)"
    ]
  }
}
`

// writeClaudeSettingsIfMissing は workspace の .claude/settings.json が無いときだけ作り（.claude/ が無ければ作る）、
// 作ったかを返す。あれば（壊れたシンボリックリンクも含む）、中身によらず読まず触らない。
func writeClaudeSettingsIfMissing(workspace string) (bool, error) {
	dir := filepath.Join(workspace, ".claude")
	path := filepath.Join(dir, "settings.json")
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("%w: stat %s: %w", ErrStoreError, path, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("%w: create %s: %w", ErrStoreError, dir, err)
	}
	// O_EXCL で、確かめた後にできたファイルを上書きしない。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: create %s: %w", ErrStoreError, path, err)
	}
	_, werr := f.WriteString(claudeSettingsContents)
	if err := errors.Join(werr, f.Close()); err != nil {
		// 書きかけを残すと、次の init が触らないままになる。
		_ = os.Remove(path)
		return false, fmt.Errorf("%w: write %s: %w", ErrStoreError, path, err)
	}
	return true, nil
}
