package core

import (
	"runtime/debug"
)

// flywheelVersion は run の行を作るときに run.flywheel_version へ書く値を返す。
// 値の取り方はこの 1 つの関数だけに置く（テストはこの変数を差し替える）。
// NULL は nil。
var flywheelVersion = buildFlywheelVersion

func buildFlywheelVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return versionFromSettings(info.Settings)
}

// versionFromSettings は VCS の情報から版を作る。vcs.revision が無ければ ""（NULL）。
// vcs.modified=true なら末尾に +dirty を付ける。
func versionFromSettings(settings []debug.BuildSetting) string {
	var rev string
	modified := false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if modified {
		rev += "+dirty"
	}
	return rev
}
