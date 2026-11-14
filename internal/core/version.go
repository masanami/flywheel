package core

import "runtime/debug"

// UnknownBuildValue は、ビルド情報（vcs の設定）が無いときの commit・time の表示値。
const UnknownBuildValue = "unknown"

// VersionInfo は動いているバイナリの版。ネットワークにも Git にも触れず、バイナリへ埋め込まれた
// ビルド情報と、このバイナリが扱うストアのスキーマの版だけを持つ。
type VersionInfo struct {
	// Commit は vcs.revision。ビルド情報が無いときは UnknownBuildValue。
	Commit string
	// Time は vcs.time。ビルド情報が無いときは UnknownBuildValue。
	Time string
	// Modified は vcs.modified（作業ツリーに未コミットの変更があったか）。不明なら false。
	Modified bool
	// SchemaVersion はこのバイナリが扱うストアのスキーマの版（LatestSchemaVersion）。
	SchemaVersion int
}

// ReadVersion は動いているバイナリの版を返す。debug.ReadBuildInfo が失敗しても、vcs の設定が
// 無くても、Commit・Time は UnknownBuildValue になるだけでエラーにしない。
func ReadVersion() (VersionInfo, error) {
	info, ok := debug.ReadBuildInfo()
	return versionFromBuildInfo(info, ok)
}

func versionFromBuildInfo(info *debug.BuildInfo, ok bool) (VersionInfo, error) {
	v := VersionInfo{Commit: UnknownBuildValue, Time: UnknownBuildValue}
	if ok && info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if s.Value != "" {
					v.Commit = s.Value
				}
			case "vcs.time":
				if s.Value != "" {
					v.Time = s.Value
				}
			case "vcs.modified":
				v.Modified = s.Value == "true"
			}
		}
	}
	schema, err := LatestSchemaVersion()
	if err != nil {
		return VersionInfo{}, err
	}
	v.SchemaVersion = schema
	return v, nil
}
