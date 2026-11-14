package view

import "github.com/masanami/flywheel/internal/core"

// VersionResponse は `version` の成功の形。ビルド情報が無いとき commit・time は "unknown"。
type VersionResponse struct {
	Commit        string `json:"commit"`
	Modified      bool   `json:"modified"`
	SchemaVersion int    `json:"schema_version"`
	Time          string `json:"time"`
}

// FromVersion は core.VersionInfo を VersionResponse へ変換する。
func FromVersion(v core.VersionInfo) VersionResponse {
	return VersionResponse{Commit: v.Commit, Modified: v.Modified, SchemaVersion: v.SchemaVersion, Time: v.Time}
}
