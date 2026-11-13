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

// actorSource は本番の既定実装。パッケージ変数を差し替えるテストは t.Parallel にしない
// （t.Parallel にするテストは Store.actor で Store ごとに差し替える）。
var actorSource = actorSourceFuncs{
	userCurrent: user.Current,
	getenv:      os.Getenv,
}

// resolveActor は actor（＝起票者 reporter と同じ値）を解決する。
// 優先順位: os/user.Current().Username（空でない）→ 環境変数 USER →
// LOGNAME。どれも得られなければ ErrActorUnavailable を返し、呼び出し側
// （mutate）は一切の変更を行わない。
func resolveActor() (string, error) {
	return resolveActorFrom(actorSource)
}

// resolveActor は s の入力元（テストが Store ごとに持たせたもの。無ければ既定）で actor を解決する。
// パッケージ変数 actorSource を書き換えずに、テストごとに actor を固定できる。
func (s *Store) resolveActor() (string, error) {
	if s.actor != nil {
		return resolveActorFrom(*s.actor)
	}
	return resolveActor()
}

func resolveActorFrom(src actorSourceFuncs) (string, error) {
	if u, err := src.userCurrent(); err == nil && u != nil && u.Username != "" {
		return u.Username, nil
	}
	if v := src.getenv("USER"); v != "" {
		return v, nil
	}
	if v := src.getenv("LOGNAME"); v != "" {
		return v, nil
	}
	return "", ErrActorUnavailable
}

// CurrentActor は resolveActor の公開ラッパー。internal/cli の Verifier 実装
// （tty_confirm 等）が、本人確認済みの actor を core と同じ規則
// （os/user.Current().Username → 環境変数 USER → LOGNAME）で解決するために使う
// （#11。tty_confirm の actor は OS のログインユーザー名＝仕様の actor 規則と
// 同じ）。
func CurrentActor() (string, error) {
	return resolveActor()
}
