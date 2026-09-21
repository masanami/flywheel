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
)
