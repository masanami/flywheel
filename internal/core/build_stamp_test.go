package core

import (
	"runtime/debug"
	"testing"
)

func TestOverlayStamp_PrefersLdflagsOverBuildInfo(t *testing.T) {
	t.Parallel()
	// 作業ツリーでは Go の VCS の刻印が外側のリポジトリのコミットを指す。ldflags の刻印が勝つ。
	outer := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "outer"}, {Key: "vcs.time", Value: "T-outer"}, {Key: "vcs.modified", Value: "false"},
		{Key: "GOOS", Value: "darwin"},
	}
	got := overlayStamp(outer, "flywheelrev", "T-fw", "true")

	v, err := versionFromBuildInfo(&debug.BuildInfo{Settings: got}, true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Commit != "flywheelrev" || v.Time != "T-fw" || !v.Modified {
		t.Fatalf("version = %+v", v)
	}
	if fv := versionFromSettings(got); fv != "flywheelrev+dirty" {
		t.Fatalf("flywheel_version = %q", fv)
	}
}

func TestOverlayStamp_NoStampKeepsBuildInfo(t *testing.T) {
	t.Parallel()
	in := []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}
	got := overlayStamp(in, "", "", "")
	if fv := versionFromSettings(got); fv != "abc+dirty" {
		t.Fatalf("flywheel_version = %q", fv)
	}
}

func TestOverlayStamp_StampWithoutBuildInfo(t *testing.T) {
	t.Parallel()
	got := overlayStamp(nil, "rev", "", "false")
	if fv := versionFromSettings(got); fv != "rev" {
		t.Fatalf("flywheel_version = %q", fv)
	}
	v, err := versionFromBuildInfo(&debug.BuildInfo{Settings: got}, true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Commit != "rev" || v.Time != UnknownBuildValue || v.Modified {
		t.Fatalf("version = %+v", v)
	}
}

// ReadVersion と run の flywheel_version の両方が刻印（readBuildInfo）を通ることを保つ。
// 一方が debug.ReadBuildInfo へ戻ると、作業ツリーで外側のリポジトリのコミットが再び出る。
// パッケージ変数を書き換えるので t.Parallel しない。
func TestReaders_UseStamp(t *testing.T) {
	oldR, oldT, oldM := stampRevision, stampTime, stampModified
	t.Cleanup(func() { stampRevision, stampTime, stampModified = oldR, oldT, oldM })
	stampRevision, stampTime, stampModified = "stamped-rev", "2026-01-02T03:04:05Z", "false"

	v, err := ReadVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v.Commit != "stamped-rev" || v.Time != "2026-01-02T03:04:05Z" || v.Modified {
		t.Fatalf("ReadVersion = %+v", v)
	}
	if got := buildFlywheelVersion(); got != "stamped-rev" {
		t.Fatalf("buildFlywheelVersion = %q", got)
	}
}
