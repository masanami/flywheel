package core

import "errors"

// core が返すストア関連の sentinel error。internal/cli はこれらを errors.Is で
// 判定し、cli.ErrorCode（usage_error 等と同じ閉集合）へ写像する。
//
// core 自身は ErrorCode 型のコード文字列を持たない（internal/cli/errors.go の
// コメントに書いた 2 案のうち (a) を採用: 「core が型付き sentinel error を返し
// cli 側で ErrorCode へ写像する」）。
var (
	// ErrStoreNotFound はワークスペースの探索でストアが見つからなかったことを表す
	// （init 以外のコマンド。fail-closed でストアは作らない）。
	ErrStoreNotFound = errors.New("core: store not found")

	// ErrStoreTooNew はストアのスキーマ版がこのバイナリの知る最新版より新しい
	// ことを表す。ファイルは変更しない。
	ErrStoreTooNew = errors.New("core: store schema version is newer than this binary supports")

	// ErrStoreBusy は他のプロセスの書き込みトランザクションを待つ上限を超えた
	// ことを表す。
	ErrStoreBusy = errors.New("core: timed out waiting for the store's write lock")

	// ErrStoreError はストアを開けない・読めない・書けないその他の失敗を表す。
	ErrStoreError = errors.New("core: store error")

	// ErrNotFound は指定した ID（課題・不可逆操作）が存在しないことを表す。
	// ID の形式が閉集合（例: ^C-[1-9][0-9]*$）に一致しない場合も、存在しない ID と
	// 同じく ErrNotFound を返す（fail-closed。docs/features/m1-core.md
	// 「課題の起票・参照・編集」§ID の解釈）。
	ErrNotFound = errors.New("core: not found")

	// ErrValidation は入力が規則に反することを表す（必須値の欠落・閉集合外の値）。
	ErrValidation = errors.New("core: validation failed")

	// ErrTerminalState は完了（done）した課題を変えようとしたことを表す。
	ErrTerminalState = errors.New("core: challenge is in a terminal state")

	// ErrActorUnavailable は actor（OS のログインユーザー名）を解決できなかった
	// ことを表す（os/user.Current の Username → 環境変数 USER → LOGNAME の
	// いずれも得られない場合）。対象は一切変更しない。
	ErrActorUnavailable = errors.New("core: actor could not be resolved")
)
