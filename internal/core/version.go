package core

import "runtime/debug"

// このファイルは `flywheel version`（Issue #593）の、動いているバイナリの版（埋め込まれたビルド情報と
// ストアのスキーマの版）の読み取りを持つ。

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
	info, ok := readBuildInfo()
	return versionFromBuildInfo(info, ok)
}

// stampRevision・stampTime・stampModified は、ビルドの手順（Makefile の -ldflags -X）が刻む flywheel 自身の
// コミット・時刻・未コミットの変更の有無。Go の VCS の刻印は、作業ツリー（.git がファイル）では外側の
// リポジトリを拾うため、刻印があればそちらを優先する。刻印が無いビルド（go install 等）は Go の VCS の刻印のまま。
var (
	stampRevision string
	stampTime     string
	stampModified string
)

// readBuildInfo は debug.ReadBuildInfo に刻印を重ねたビルド情報を返す。版の読み方（ReadVersion と
// run の flywheel_version）はどちらもここを通る。
func readBuildInfo() (*debug.BuildInfo, bool) {
	info, ok := debug.ReadBuildInfo()
	if stampRevision == "" {
		return info, ok
	}
	out := &debug.BuildInfo{}
	if ok && info != nil {
		cp := *info
		out = &cp
	}
	out.Settings = overlayStamp(out.Settings, stampRevision, stampTime, stampModified)
	return out, true
}

// overlayStamp は settings の vcs.revision・vcs.time・vcs.modified を刻印の値で置き換えた新しい
// スライスを返す。revision が空なら settings をそのまま返す。
// 刻印がある場合、元の vcs.* は外側のリポジトリの値でありうるため、time・modified が空でも元の値を残さない
// （time は無し、modified は false 扱い）。
func overlayStamp(settings []debug.BuildSetting, revision, time, modified string) []debug.BuildSetting {
	if revision == "" {
		return settings
	}
	out := make([]debug.BuildSetting, 0, len(settings)+3)
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision", "vcs.time", "vcs.modified":
			continue
		}
		out = append(out, s)
	}
	out = append(out, debug.BuildSetting{Key: "vcs.revision", Value: revision})
	if time != "" {
		out = append(out, debug.BuildSetting{Key: "vcs.time", Value: time})
	}
	return append(out, debug.BuildSetting{Key: "vcs.modified", Value: modified})
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

// CommitLabel はコミットの表示（version のテキスト・status のバイナリのコミット）。未コミットの変更つきの
// ビルドは、run の flywheel_version と同じく末尾に +dirty を付ける。
func (v VersionInfo) CommitLabel() string {
	if v.Modified {
		return v.Commit + "+dirty"
	}
	return v.Commit
}
