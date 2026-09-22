package core

import (
	"os"
	"os/user"
)

// actorSourceFuncs はテストが差し替え可能な形で actor 解決の入力元を持つ
// （docs/features/m1-core.md §作業ログ「actor は OS のログインユーザー名とする。
// 取得は Go の os/user.Current() の Username を使い、取得できないときは
// 環境変数 USER、次に LOGNAME を使う」）。
type actorSourceFuncs struct {
	userCurrent func() (*user.User, error)
	getenv      func(string) string
}

// actorSource は本番の既定実装。internal/core のテストだけが t.Cleanup で
// 復元しつつ差し替える（該当テストは t.Parallel にしない）。
var actorSource = actorSourceFuncs{
	userCurrent: user.Current,
	getenv:      os.Getenv,
}

// resolveActor は actor（＝起票者 reporter と同じ値）を解決する。
// 優先順位: os/user.Current().Username（空でない）→ 環境変数 USER →
// LOGNAME。どれも得られなければ ErrActorUnavailable を返し、呼び出し側
// （mutate）は一切の変更を行わない。
func resolveActor() (string, error) {
	if u, err := actorSource.userCurrent(); err == nil && u != nil && u.Username != "" {
		return u.Username, nil
	}
	if v := actorSource.getenv("USER"); v != "" {
		return v, nil
	}
	if v := actorSource.getenv("LOGNAME"); v != "" {
		return v, nil
	}
	return "", ErrActorUnavailable
}
