package core

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want VersionInfo
	}{
		{"ビルド情報なし", nil, false, VersionInfo{Commit: "unknown", Time: "unknown", SchemaVersion: latest}},
		{"vcs 設定なし", &debug.BuildInfo{}, true, VersionInfo{Commit: "unknown", Time: "unknown", SchemaVersion: latest}},
		{"vcs 設定あり", &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.time", Value: "2026-11-14T00:00:00Z"}, {Key: "vcs.modified", Value: "true"},
		}}, true, VersionInfo{Commit: "abc123", Time: "2026-11-14T00:00:00Z", Modified: true, SchemaVersion: latest}},
	}
	for _, c := range cases {
		got, err := versionFromBuildInfo(c.info, c.ok)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
