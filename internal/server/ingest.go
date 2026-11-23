package server

import (
	"net/http"

	"github.com/masanami/flywheel/internal/core"
)

// ingestHandler は POST …/ingest（画面のボタンでの取り込み）。本文は view.IngestRequest、成功の本文は view.IngestAcceptedResponse。
// issues は上流の Issue を読む口の組み立て（取り込めるものの読み取りと同じもの）。実装は後続のタスクが埋める。
func ingestHandler(fleet *Fleet, issues func() (core.UpstreamIssueSource, error), launch CycleStarter) http.HandlerFunc {
	_, _, _ = fleet, issues, launch
	return notImplementedPost
}
