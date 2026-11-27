package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 仕様の文書の本文を返す口（GET …/challenges/{id}/spec）と、計画の承認の要約の 3 項目。AC の番号は、Issue #1381 の受け持ち。

const (
	specBodyPath      = "/api/v1/workspaces/w/challenges/C-1/spec"
	specDraftLocation = ".flywheel/specs/c-1.md"
	specDocLocation   = "docs/features/a.md"
	specDocBody       = "# 仕様\n\n## 受入基準\n\n- [ ] 基準 1\n"
	specMaxBytes      = 2097152
)

// specBodyWorkspace は、仕様の文書の場所も下書きづくりの記録も持たない課題 C-1 のあるワークスペース。
func specBodyWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	approvalFixture(t, ws, "classified")
	return ws
}

// putSpecPath は課題の仕様の文書の場所を、検査を通さずにストアへ直接置く。
func putSpecPath(t *testing.T, ws, path string) {
	t.Helper()
	coretest.Exec(t, ws, `UPDATE challenge SET spec_path = ? WHERE id = 1`, path)
}

// putSpecRecord は課題 C-1 に、状態が state の下書きづくりの記録を置く。
func putSpecRecord(t *testing.T, ws, state string) {
	t.Helper()
	coretest.InsertSpecDraftHold(t, ws, 1, "spec_question", "", `[]`)
	coretest.Exec(t, ws, `UPDATE spec_draft SET state = ? WHERE challenge_id = 1`, state)
}

func writeWorkspaceFile(t *testing.T, ws, rel, body string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func linkWorkspaceFile(t *testing.T, ws, rel, target string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// getSpecBody は ws の課題の本文の口を読み、ステータス 200 の応答を、生の文字列と復号した形で返す。
func getSpecBody(t *testing.T, ws string) (string, map[string]any) {
	t.Helper()
	code, body := apiGet(t, approvalServer(t, ws), specBodyPath)
	if code != http.StatusOK {
		t.Fatalf("GET spec = %d %s", code, body)
	}
	return body, decodeMap(t, body)
}

func assertSpecResponse(t *testing.T, ws string, want map[string]any) string {
	t.Helper()
	raw, got := getSpecBody(t, ws)
	if !reflect.DeepEqual(got, want) {
		if len(raw) > 500 {
			raw = raw[:500]
		}
		t.Errorf("spec response mismatch: got %s, want path=%v is_draft=%v reason=%v", raw, want["path"], want["is_draft"], want["unreadable_reason"])
	}
	return raw
}

func readSpec(path string, isDraft bool, body string) map[string]any {
	return map[string]any{"body": body, "is_draft": isDraft, "path": path, "unreadable_reason": nil}
}

func unreadSpec(path string, isDraft bool, reason string) map[string]any {
	return map[string]any{"body": nil, "is_draft": isDraft, "path": path, "unreadable_reason": reason}
}

// AC-237・AC-238・AC-239・AC-240・AC-241・AC-243
func TestSpecBody_ReturnsTheWholeDocumentAtTheChallengePath(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDocLocation, specDocBody)
	putSpecPath(t, ws, specDocLocation)
	// キーが 4 つだけであることは、写像の全体の一致で確かめる。
	assertSpecResponse(t, ws, readSpec(specDocLocation, false, specDocBody))
}

// AC-242
func TestSpecBody_PathUnderTheDraftDirectoryIsADraft(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDraftLocation, specDocBody)
	putSpecPath(t, ws, specDraftLocation)
	assertSpecResponse(t, ws, readSpec(specDraftLocation, true, specDocBody))
}

// AC-244・AC-245
func TestSpecBody_AwaitingOwnerRecordReturnsTheDraft(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDraftLocation, "# 下書き\n")
	putSpecRecord(t, ws, "awaiting_owner")
	assertSpecResponse(t, ws, readSpec(specDraftLocation, true, "# 下書き\n"))
}

// AC-246
func TestSpecBody_RunningRecordUsesTheDraftPath(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDraftLocation, "# 書きかけ\n")
	putSpecRecord(t, ws, "running")
	assertSpecResponse(t, ws, readSpec(specDraftLocation, true, "# 書きかけ\n"))
}

// AC-247・AC-248・AC-249
func TestSpecBody_PendingRecordWithoutAFileIsMissing(t *testing.T) {
	ws := specBodyWorkspace(t)
	putSpecRecord(t, ws, "pending")
	assertSpecResponse(t, ws, unreadSpec(specDraftLocation, true, "missing"))
}

// AC-250・AC-251・AC-252・AC-253・AC-254
func TestSpecBody_NoLocationIsNotFound(t *testing.T) {
	for _, tc := range []struct{ name, record, path string }{
		{"neither a path nor a record (AC-250, AC-251)", "", specBodyPath},
		{"skipped record (AC-252)", "skipped", specBodyPath},
		{"done record after the path was removed (AC-253)", "done", specBodyPath},
		{"unknown challenge (AC-254)", "", "/api/v1/workspaces/w/challenges/C-99/spec"},
		{"malformed challenge ID (AC-254)", "", "/api/v1/workspaces/w/challenges/nope/spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := specBodyWorkspace(t)
			// 下書きのファイルがあっても、場所が決まらなければ読まない。
			writeWorkspaceFile(t, ws, specDraftLocation, specDocBody)
			if tc.record != "" {
				putSpecRecord(t, ws, tc.record)
			}
			code, body := apiGet(t, approvalServer(t, ws), tc.path)
			if code != http.StatusNotFound || errCode(t, body) != "not_found" {
				t.Errorf("GET = %d %s, want 404 not_found", code, body)
			}
		})
	}
}

// AC-255
func TestSpecBody_ChallengePathWinsOverTheRecordPath(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDraftLocation, "# 下書き\n")
	writeWorkspaceFile(t, ws, specDocLocation, specDocBody)
	putSpecRecord(t, ws, "done")
	putSpecPath(t, ws, specDocLocation)
	assertSpecResponse(t, ws, readSpec(specDocLocation, false, specDocBody))
}

// AC-256・AC-257。空の文書も、本文は null でなく空の文字列。
func TestSpecBody_DoesNotRequireAcceptanceCriteria(t *testing.T) {
	for name, doc := range map[string]string{
		"zero criteria (AC-256)":       "# 仕様\n\n## 受入基準\n\nまだ無い\n",
		"no criteria section (AC-257)": "# 仕様\n\n本文だけ\n",
		"empty file":                   "",
	} {
		t.Run(name, func(t *testing.T) {
			ws := specBodyWorkspace(t)
			writeWorkspaceFile(t, ws, specDocLocation, doc)
			putSpecPath(t, ws, specDocLocation)
			assertSpecResponse(t, ws, readSpec(specDocLocation, false, doc))
		})
	}
}

// AC-258
func TestSpecBody_ExactlyTwoMiBIsReturnedWhole(t *testing.T) {
	ws := specBodyWorkspace(t)
	doc := strings.Repeat("a", specMaxBytes)
	writeWorkspaceFile(t, ws, specDocLocation, doc)
	putSpecPath(t, ws, specDocLocation)
	assertSpecResponse(t, ws, readSpec(specDocLocation, false, doc))
}

// AC-259・AC-260
func TestSpecBody_OneByteOverTwoMiBIsTooLarge(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDocLocation, strings.Repeat("a", specMaxBytes+1))
	putSpecPath(t, ws, specDocLocation)
	assertSpecResponse(t, ws, unreadSpec(specDocLocation, false, "too_large"))
}

// AC-261・AC-265・AC-268
func TestSpecBody_LinkLeadingOutsideTheWorkspaceIsInvalid(t *testing.T) {
	ws := specBodyWorkspace(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("SECRET-OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkWorkspaceFile(t, ws, specDocLocation, outside)
	putSpecPath(t, ws, specDocLocation)
	raw := assertSpecResponse(t, ws, unreadSpec(specDocLocation, false, "invalid_path"))
	if strings.Contains(raw, "SECRET-OUTSIDE") {
		t.Errorf("the content outside the workspace leaked: %s", raw)
	}
	assertNoWorkspacePath(t, ws, raw)
}

// AC-262・AC-263。場所の検査を通らない値は、ストアへ直接置く。
func TestSpecBody_AbsoluteAndDotDotPathsAreInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(ws string) string
	}{
		{"absolute (AC-262)", func(ws string) string { return filepath.Join(ws, filepath.FromSlash(specDocLocation)) }},
		{"dot-dot element (AC-263)", func(string) string { return "docs/../docs/features/a.md" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := specBodyWorkspace(t)
			writeWorkspaceFile(t, ws, specDocLocation, specDocBody)
			p := tc.path(ws)
			putSpecPath(t, ws, p)
			assertSpecResponse(t, ws, unreadSpec(p, false, "invalid_path"))
		})
	}
}

// AC-264
func TestSpecBody_LinkInsideTheWorkspaceReturnsTheTarget(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, "docs/real.md", specDocBody)
	linkWorkspaceFile(t, ws, specDocLocation, filepath.Join("..", "real.md"))
	putSpecPath(t, ws, specDocLocation)
	assertSpecResponse(t, ws, readSpec(specDocLocation, false, specDocBody))
}

// AC-266・AC-268
func TestSpecBody_DirectoryIsNotARegularFile(t *testing.T) {
	ws := specBodyWorkspace(t)
	if err := os.MkdirAll(filepath.Join(ws, filepath.FromSlash(specDocLocation)), 0o755); err != nil {
		t.Fatal(err)
	}
	putSpecPath(t, ws, specDocLocation)
	assertNoWorkspacePath(t, ws, assertSpecResponse(t, ws, unreadSpec(specDocLocation, false, "not_regular_file")))
}

// AC-268。ファイルの系の誤り（絶対パスを含む文を持つ）になる読み取り。
func TestSpecBody_UnreadableResponsesDoNotCarryTheWorkspacePath(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		ws := specBodyWorkspace(t)
		putSpecPath(t, ws, specDocLocation)
		assertNoWorkspacePath(t, ws, assertSpecResponse(t, ws, unreadSpec(specDocLocation, false, "missing")))
	})
	t.Run("no read permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root は権限の無いファイルも読める")
		}
		ws := specBodyWorkspace(t)
		writeWorkspaceFile(t, ws, specDocLocation, specDocBody)
		if err := os.Chmod(filepath.Join(ws, filepath.FromSlash(specDocLocation)), 0); err != nil {
			t.Fatal(err)
		}
		putSpecPath(t, ws, specDocLocation)
		assertNoWorkspacePath(t, ws, assertSpecResponse(t, ws, unreadSpec(specDocLocation, false, "unreadable")))
	})
}

// assertNoWorkspacePath は、書かれたとおりの形と、リンクを解決した形の、どちらのワークスペースの絶対パスも応答に無いことを確かめる。
func assertNoWorkspacePath(t *testing.T, ws, raw string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ws, resolved} {
		if strings.Contains(raw, p) {
			t.Errorf("the response carries the workspace path: %s", raw)
		}
	}
}

// AC-269
func TestSpecBody_IgnoresAPathInTheQuery(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDocLocation, specDocBody)
	writeWorkspaceFile(t, ws, "docs/other.md", "OTHER")
	putSpecPath(t, ws, specDocLocation)
	code, body := apiGet(t, approvalServer(t, ws), specBodyPath+"?path=docs/other.md")
	if code != http.StatusOK || !reflect.DeepEqual(decodeMap(t, body), readSpec(specDocLocation, false, specDocBody)) {
		t.Errorf("GET with ?path= = %d %s", code, body)
	}
}

// AC-270
func TestSpecBody_PostIsMethodNotAllowed(t *testing.T) {
	ws := specBodyWorkspace(t)
	putSpecPath(t, ws, specDocLocation)
	code, body := apiPost(t, approvalServer(t, ws), specBodyPath, `{}`)
	if code != http.StatusMethodNotAllowed || errCode(t, body) != CodeUsageError {
		t.Errorf("POST spec = %d %s, want 405", code, body)
	}
}

// AC-271
func TestSpecBody_ChangesNothing(t *testing.T) {
	ws := specBodyWorkspace(t)
	writeWorkspaceFile(t, ws, specDraftLocation, specDocBody)
	putSpecRecord(t, ws, "awaiting_owner")
	port := approvalServer(t, ws)
	tables := []string{"challenge", "activity", "spec_draft", "hold"}
	before := coretest.DumpTables(t, ws, tables...)
	if code, body := apiGet(t, port, specBodyPath); code != http.StatusOK {
		t.Fatalf("GET spec = %d %s", code, body)
	}
	if after := coretest.DumpTables(t, ws, tables...); after != before {
		t.Errorf("the store changed:\n%s\n---\n%s", before, after)
	}
	if got, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(specDraftLocation))); err != nil || string(got) != specDocBody {
		t.Errorf("the draft changed: %q %v", got, err)
	}
}

// approvalDoc は計画承認待ちの課題 C-1 の承認の要約の approval。
func approvalDoc(t *testing.T, port int) map[string]any {
	t.Helper()
	code, body := apiGet(t, port, approvalPath)
	if code != http.StatusOK {
		t.Fatalf("GET approval = %d %s", code, body)
	}
	doc, ok := decodeMap(t, body)["approval"].(map[string]any)
	if !ok {
		t.Fatalf("no approval object: %s", body)
	}
	return doc
}

func assertApprovalSpec(t *testing.T, doc, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, ok := doc[k]
		if !ok || !reflect.DeepEqual(got, v) {
			raw, _ := json.Marshal(got)
			t.Errorf("approval.%s = %s (present %v), want %v", k, raw, ok, v)
		}
	}
}

// AC-272・AC-276
func TestApprovalSummary_CarriesTheSpecPathAndNoAnswersWithoutARecord(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	putSpecPath(t, ws, specDocLocation)
	assertApprovalSpec(t, approvalDoc(t, approvalServer(t, ws)), map[string]any{
		"spec_path": specDocLocation, "spec_is_draft": false, "spec_draft_answers": []any{},
	})
}

// AC-273
func TestApprovalSummary_SpecPathIsNullWithoutALocation(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	assertApprovalSpec(t, approvalDoc(t, approvalServer(t, ws)), map[string]any{
		"spec_path": nil, "spec_is_draft": false, "spec_draft_answers": []any{},
	})
}

// AC-274・AC-275。種類を持たずに保存した要素も、詳細と同じ形（種類は answer）になる。
func TestApprovalSummary_DraftSpecCarriesTheSameAnswersAsTheDetail(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	putSpecRecord(t, ws, "done")
	putSpecPath(t, ws, specDraftLocation)
	coretest.SetSpecDraftAnswers(t, ws, 1, `[`+
		`{"attempt":1,"question":"Q-old","answer":"a","by":"owner","basis":[]},`+
		`{"kind":"answer","attempt":1,"question":"Q-sv","answer":"b","by":"supervisor","basis":[{"path":"docs/a.md","locator":"節 1"}]},`+
		`{"kind":"note","attempt":1,"question":"","answer":"添え書き","by":"owner","basis":[]}]`)
	port := approvalServer(t, ws)
	code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-1")
	if code != http.StatusOK {
		t.Fatalf("GET detail = %d %s", code, body)
	}
	var detail struct {
		Challenge struct {
			SpecDraft struct {
				Answers []any `json:"answers"`
			} `json:"spec_draft"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Challenge.SpecDraft.Answers) != 3 {
		t.Fatalf("detail answers = %v, want 3 elements: %s", detail.Challenge.SpecDraft.Answers, body)
	}
	assertApprovalSpec(t, approvalDoc(t, port), map[string]any{
		"spec_path": specDraftLocation, "spec_is_draft": true, "spec_draft_answers": detail.Challenge.SpecDraft.Answers,
	})
}
