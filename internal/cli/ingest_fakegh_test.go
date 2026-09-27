package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// このファイルは Issue #59（ingest に取得と反映をつなぐ）の CLI レベルの
// テストが使う、より作り込んだ偽の `gh` を提供する。ingest_test.go の
// writeFakeGH／withFakeGHOnPATH（呼び出しの記録だけを行い、応答は返さない）
// を置き換えるのではなく、実際に gh api の応答（一覧・1件取得）を返す必要が
// あるテストのために別に用意する。

// fakeGHRoute は偽の gh の 1 つの応答規則。match は呼び出しの引数列
// （スペース区切りの "$*"）の**末尾**に対する一致（シェルの
// `case "$*" in *match)`。先頭にだけ `*` を付け、末尾には付けない）。
// gh に渡す endpoint は常に最後の引数（listIssuesURL・getIssueURL の戻り値。
// internal/adapters/github/issues.go）なので、match は必ず "$*" の末尾に
// 現れる。末尾を固定することで、"page=1" が "page=10"・"page=19" の接尾辞にも
// なる、"issues/5" が "issues/50"〜"issues/59" の接尾辞にもなる、といった
// 意図しない部分一致を防ぐ（code-reviewer 指摘の再発防止）。複数の route が
// match すると、先に定義した方が優先される。
type fakeGHRoute struct {
	match string
	// stdout は標準出力にそのまま書く内容（JSON 配列、または `-i` の
	// HTTP 応答そのもの）。
	stdout string
	// exit はこの呼び出しの終了コード（gh api は 404 でも終了コード 1 を
	// 返しうるため、成功=0 に固定しない）。
	exit int
}

// writeFakeGHRoutes は route にマッチする呼び出しへ規則どおりの応答を返し、
// どれにもマッチしない呼び出しは標準エラーへ理由を書いて終了コード 1 で
// 失敗する偽の `gh` を t.TempDir() に置く。戻り値の calls は、記録した
// 呼び出しの引数列（"$*" の値。呼ばれた順）を返す。
func writeFakeGHRoutes(t *testing.T, routes []fakeGHRoute) (dir string, calls func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir = t.TempDir()
	logPath := filepath.Join(dir, "gh-calls.log")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(fakeGHLogAppendLine(logPath))
	b.WriteString("case \"$*\" in\n")
	for i, r := range routes {
		// heredoc の終端語はテスト・route ごとに変え、応答本文に偶然同じ行が
		// 含まれて誤って終端してしまう事故を避ける。
		delim := fmt.Sprintf("FAKEGH_EOF_%d", i)
		fmt.Fprintf(&b, "*%s)\ncat <<'%s'\n%s\n%s\nexit %d\n;;\n", shellSingleQuote(r.match), delim, r.stdout, delim, r.exit)
	}
	b.WriteString("*)\n")
	b.WriteString("printf 'fake gh: no route for: %s\\n' \"$*\" >&2\n")
	b.WriteString("exit 1\n")
	b.WriteString(";;\n")
	b.WriteString("esac\n")

	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return dir, func() []string { return readFakeGHCallLog(t, logPath) }
}

// fakeGHLogAppendLine は、呼ばれるたびに "$*"（渡された引数列）を1行として
// logPath へ追記するシェルスクリプトの断片を返す。書式（"CALL " の前置・
// readFakeGHCallLog が読む形）は、この関数と readFakeGHCallLog の対を
// writeFakeGH（ingest_test.go）・writeFakeGHRoutes・writeSleepyFakeGH の
// 3箇所すべてが共有することで1箇所だけに定める（design-reviewer 指摘の
// 再発防止: 以前はログの書き出し・読み取りが3箇所に逐語コピーされていた）。
// "CALL " を前置するのは、引数なしの呼び出し（空行）と「1回も呼ばれていない
// （ログファイル自体が無い）」を行の中身からも区別できるようにするため。
func fakeGHLogAppendLine(logPath string) string {
	return "printf 'CALL %s\\n' \"$*\" >> " + shellSingleQuote(logPath) + "\n"
}

// readFakeGHCallLog は fakeGHLogAppendLine が書いたログファイルを読み、
// 記録された呼び出しの引数列（"$*" の値。呼ばれた順）を返す。ログファイルが
// 無ければ（1回も呼ばれていなければ）nil を返す。
func readFakeGHCallLog(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read fake gh log: %v", err)
	}
	// 各呼び出しは必ず1つの "\n" で終わるため、Split は末尾に余分な空要素を
	// 1つ生む。それだけを取り除く（TrimRight で全体を trim すると、
	// 引数なしの呼び出し=空行が正当な記録ごと失われる）。
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// shellSingleQuote は s をシェルの単一引用符で囲む（s 自身に含まれる `'` は
// `'\”` へ置き換える。標準的な shell クォートの手法）。case パターンの中で
// 単一引用符に囲まれた部分は、`?`・`*`・`&` を含めグロブ・制御文字としての
// 意味を持たず常にリテラル一致になる（このファイルが作る match は gh api の
// endpoint 文字列で `&`・`?` を含むため、バックスラッシュでの個別エスケープでは
// `&`〔シェルの制御文字〕を無効化できず syntax error になる。単一引用符で
// 丸ごと囲むのが正しい対処）。呼び出し側（fakeGHRoute のコメント参照）は、
// 引用符の外側・先頭にだけ `*` を置いて末尾一致にする（末尾にも `*` を置くと
// 部分一致になり、match の値がたまたま別の呼び出しの末尾の接尾辞にもなる
// ケース〔例: "page=1" が "page=10" の接尾辞〕を誤って拾ってしまう。
// review round2 指摘の再発防止: このコメントが前提としていた「前後を `*` で
// 挟む部分一致」は、その後 fakeGHRoute 側を末尾一致に変更したときに
// 追随できておらず、実装と食い違ったまま残っていた）。
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// withFakeGHRoutesOnPATH は writeFakeGHRoutes の偽の gh を PATH の先頭に足す。
func withFakeGHRoutesOnPATH(t *testing.T, routes []fakeGHRoute) func() []string {
	t.Helper()
	dir, calls := writeFakeGHRoutes(t, routes)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// --- gh api の応答（Issue 一覧・1 件取得）を組み立てるヘルパー ---

type fakeGHUser struct {
	Login string `json:"login"`
}

type fakeGHPullRequestRef struct {
	URL string `json:"url"`
}

// fakeGHIssue は gh api の Issue 応答（一覧の要素・1 件取得の本文のどちらも
// 同じ形。internal/adapters/github/normalize.go の issuePayload と対になる）を
// 組み立てるための入力。
type fakeGHIssue struct {
	Number      int
	Title       string
	Body        string
	Reporter    string
	Assignees   []string
	Labels      []string
	State       string // "open" | "closed"
	Repo        string // "<owner>/<name>"（html_url・repository_url の組み立てに使う）
	PullRequest bool
	// Comments・UpdatedAt は観測値（docs/features/m2-github-issue-ingest.md
	// §上流の更新の観測と既読）。UpdatedAt を省略すると既定値
	// "2026-01-01T00:00:00Z" になる（空文字列は core 内部の「未設定」
	// センチネルと衝突するため、gh の応答としては使わない）。
	Comments  int
	UpdatedAt string
}

// marshalFakeGHIssue は 1 件の Issue を gh api の応答と同じ JSON（1 行）へ
// 変換する。
func marshalFakeGHIssue(t *testing.T, iss fakeGHIssue) string {
	t.Helper()
	reporter := iss.Reporter
	if reporter == "" {
		reporter = "reporter"
	}
	state := iss.State
	if state == "" {
		state = "open"
	}
	updatedAt := iss.UpdatedAt
	if updatedAt == "" {
		updatedAt = "2026-01-01T00:00:00Z"
	}
	assignees := make([]fakeGHUser, 0, len(iss.Assignees))
	for _, a := range iss.Assignees {
		assignees = append(assignees, fakeGHUser{Login: a})
	}
	labels := iss.Labels
	if labels == nil {
		labels = []string{}
	}
	payload := map[string]any{
		"number":         iss.Number,
		"title":          iss.Title,
		"body":           iss.Body,
		"user":           fakeGHUser{Login: reporter},
		"assignees":      assignees,
		"labels":         labels,
		"state":          state,
		"html_url":       fmt.Sprintf("https://github.com/%s/issues/%d", iss.Repo, iss.Number),
		"repository_url": fmt.Sprintf("https://api.github.com/repos/%s", iss.Repo),
		"comments":       iss.Comments,
		"updated_at":     updatedAt,
	}
	if iss.PullRequest {
		payload["pull_request"] = fakeGHPullRequestRef{URL: fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", iss.Repo, iss.Number)}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fake gh issue: %v", err)
	}
	return string(b)
}

// fakeGHIssueListBody は複数の Issue から gh api の一覧応答（JSON 配列。
// 1 行）を組み立てる。
func fakeGHIssueListBody(t *testing.T, issues []fakeGHIssue) string {
	t.Helper()
	elems := make([]string, 0, len(issues))
	for _, iss := range issues {
		elems = append(elems, marshalFakeGHIssue(t, iss))
	}
	return "[" + strings.Join(elems, ",") + "]"
}

// fakeGHListRoute は repo の page ページ目の一覧取得（GET
// repos/<repo>/issues?state=open&per_page=100&page=<page>）に対する応答規則を
// 作る。
func fakeGHListRoute(repo string, page int, body string, exit int) fakeGHRoute {
	return fakeGHRoute{
		match:  fmt.Sprintf("repos/%s/issues?state=open&per_page=100&page=%d", repo, page),
		stdout: body,
		exit:   exit,
	}
}

// writeSleepyFakeGH は、呼ばれるたびに（あれば）startedFile へ touch してから
// sleepArg 秒（`sleep` コマンドへそのまま渡す）眠り続け、その後 `[]` を返す
// 偽の `gh` を置く（AC-20 の時間の上限・AC-21 の書き込みロックの検証に使う。
// startedFile は「呼ばれたことの合図」で、呼び出し側はこれを待ってから次の
// 操作をする＝タイミング依存にしない）。
func writeSleepyFakeGH(t *testing.T, sleepArg, startedFile string) (dir string, calls func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir = t.TempDir()
	logPath := filepath.Join(dir, "gh-calls.log")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(fakeGHLogAppendLine(logPath))
	if startedFile != "" {
		fmt.Fprintf(&b, "touch %s\n", shellSingleQuote(startedFile))
	}
	fmt.Fprintf(&b, "sleep %s\n", sleepArg)
	b.WriteString("echo '[]'\n")
	b.WriteString("exit 0\n")

	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write sleepy fake gh: %v", err)
	}
	return dir, func() []string { return readFakeGHCallLog(t, logPath) }
}

// fakeGHGetIssueStatusLineRoute は repo の number 番の 1 件取得（`gh api -i`）に
// 対する応答規則を作る（HTTP のステータス行を含む生の応答。exit は gh api の
// 終了コード。404・410 は gh 自身が終了コード 1 で終わる＝#55 の実測どおり）。
func fakeGHGetIssueStatusLineRoute(repo string, number int, statusLine, body string, exit int) fakeGHRoute {
	return fakeGHRoute{
		match:  fmt.Sprintf("-i repos/%s/issues/%d", repo, number),
		stdout: statusLine + "\r\nContent-Type: application/json; charset=utf-8\r\n\r\n" + body,
		exit:   exit,
	}
}
