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

	// ErrTTYRequired は本人確認の方式（Verifier）の成立条件を満たさなかった
	// ことを表す（docs/features/m1-core.md §クリティカル設計決定 1。
	// tty_confirm では「標準入力が端末」「/dev/tty を開ける」
	// 「CLAUDECODE が未設定」のいずれかを満たさない場合）。
	ErrTTYRequired = errors.New("core: a terminal is required for this verification method")

	// ErrConfirmationMismatch は本人確認の入力が対象の ID と完全一致しな
	// かったことを表す（空行・"y"・別の ID・入力の終端＝EOF を含む）。
	ErrConfirmationMismatch = errors.New("core: confirmation input did not match the expected ID")

	// ErrVerificationRejected は (Channel, Verification) の組が登録簿に無い
	// ことを表す。Verifier の Confirm は呼ばれておらず、状態は一切変わらない
	// （§クリティカル設計決定 1「登録簿に無い組の要求を verification_rejected
	// で拒否する」）。
	ErrVerificationRejected = errors.New("core: this (channel, verification) pair is not in the registry")
)
