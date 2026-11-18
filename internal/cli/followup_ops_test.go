package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは区切り S2b の課題 #774（followup done・drop・promote）の受入基準（AC-47〜AC-72・AC-76〜AC-98・AC-307）を、
// 実ストアの CLI で確かめる。本人確認は疑似端末を割り当てた子プロセスと、標準入力をパイプにした子プロセスで確かめる。
// 受入基準の番号はテスト名・コメントに書く。

const fuRelayed = "--relayed --session SESS-F --quote QUOTE-F"

// fuWorkspace は課題 C-1 と、未のフォローアップ F-1（C-1）・F-2（C-1）・F-3（課題に属さない・detail なし）、
// 済み F-4・捨てた F-5 を持つワークスペースを作る。
func fuWorkspace(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	createForCase(t, ws)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "題名 1", Detail: "説明 1"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "題名 2", Detail: "説明 2"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "題名 3"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "済み", Status: "done", Note: "n"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "捨てた", Status: "dropped", Reason: "r"})
	return ws
}

// fuRun は同じプロセスで実行する（標準入力は端末でない）。
func fuRun(t *testing.T, ws string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var out, errb bytes.Buffer
	code = runWithLookPath(full, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t))
	return code, out.String(), errb.String()
}

func fuArgs(s string, more ...string) []string {
	return append(strings.Fields(s), more...)
}

func fuStatus(t *testing.T, ws string, n int) string {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT status FROM followup WHERE id = ?`, n)[0][0].(string)
}

func fuCount(t *testing.T, ws, query string, args ...any) string {
	t.Helper()
	return fmt.Sprint(coretest.QueryRows(t, ws, query, args...)[0][0])
}

func fuWantCode(t *testing.T, ac string, code int, stderr string, want ErrorCode) {
	t.Helper()
	if code == 0 || readErrorCode(t, stderr) != string(want) {
		t.Errorf("%s: code=%d stderr=%s, want %s", ac, code, stderr, want)
	}
}

func fuActivity(t *testing.T, ws, action string) [][]any {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT actor, channel, verification, after FROM activity WHERE entity = 'followup' AND action = ? ORDER BY id`, action)
}

func fuAfter(t *testing.T, row []any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(row[3])), &m); err != nil {
		t.Fatalf("after is not JSON: %v", err)
	}
	return m
}

// AC-47・AC-48 #774: 未のフォローアップを済みにすると status が done、note に指定した文が残る。
func TestFollowupDone_AC47_AC48_MarksDoneWithNote(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	if code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "--note", "確かめた結果"); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := fuStatus(t, ws, 1); got != "done" { // AC-47
		t.Errorf("AC-47: status = %s", got)
	}
	if got := coretest.QueryRows(t, ws, `SELECT note FROM followup WHERE id = 1`)[0][0]; got != "確かめた結果" { // AC-48
		t.Errorf("AC-48: note = %v", got)
	}
}

// AC-49 #774: 端末が無く --relayed も付けなくても成功する（標準入力をパイプにした子プロセス）。
func TestFollowupDone_AC49_SucceedsWithoutTerminalAndWithoutRelayed(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := runPiped(t, []string{"followup", "done", "F-1", "--note", "n", "--workspace", ws, "--json"})
	if code != 0 || fuStatus(t, ws, 1) != "done" {
		t.Errorf("AC-49: code=%d stderr=%s status=%s", code, stderr, fuStatus(t, ws, 1))
	}
}

// AC-50 #774: --note が無ければ usage_error。
func TestFollowupDone_AC50_MissingNoteIsUsageError(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, "followup", "done", "F-1")
	fuWantCode(t, "AC-50", code, stderr, CodeUsageError)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-50: status changed")
	}
}

// AC-51 #774: --note が空の文字列なら validation_failed。
func TestFollowupDone_AC51_EmptyNoteIsValidationFailed(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "--note", "")
	fuWantCode(t, "AC-51", code, stderr, CodeValidationFailed)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-51: status changed")
	}
}

// AC-52 #774: 未の 2 件を並べると 2 件とも done。
func TestFollowupDone_AC52_TwoIDsBothDone(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	if code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "F-2", "--note", "n"); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if fuStatus(t, ws, 1) != "done" || fuStatus(t, ws, 2) != "done" {
		t.Error("AC-52: not both done")
	}
}

// AC-53 #774: 未のものと済みのものを並べると validation_failed で、未のものは open のまま。
func TestFollowupDone_AC53_OpenAndDoneIsValidationFailedAndChangesNothing(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "F-4", "--note", "n")
	fuWantCode(t, "AC-53", code, stderr, CodeValidationFailed)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-53: F-1 changed")
	}
}

// AC-54 #774: 存在しない ID は not_found。
func TestFollowupDone_AC54_UnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "F-99", "--note", "n")
	fuWantCode(t, "AC-54", code, stderr, CodeNotFound)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-54: F-1 changed")
	}
}

// AC-55 #774: 同じ ID を 2 度並べると usage_error。
func TestFollowupDone_AC55_DuplicateIDIsUsageError(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, "followup", "done", "F-1", "F-1", "--note", "n")
	fuWantCode(t, "AC-55", code, stderr, CodeUsageError)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-55: F-1 changed")
	}
}

// AC-56 #774: 済みにすると作業ログに followup_done が 1 件増える。
func TestFollowupDone_AC56_RecordsOneActivity(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	before := len(fuActivity(t, ws, "followup_done"))
	fuRun(t, ws, "followup", "done", "F-1", "--note", "n")
	if got := len(fuActivity(t, ws, "followup_done")); got != before+1 {
		t.Errorf("AC-56: followup_done rows %d -> %d", before, got)
	}
}

// AC-57・AC-58 #774: 端末での本人確認（対象 F-1 を打ち込む）を通した drop で dropped、reason に指定した文が残る。
func TestFollowupDrop_AC57_AC58_TerminalConfirmationDropsWithReason(t *testing.T) {
	ws := fuWorkspace(t)
	code, _, stdout, stderr := runConfirmedChild(t, []string{"followup", "drop", "F-1", "--reason", "不要になった", "--workspace", ws, "--json"}, "F-1")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := fuStatus(t, ws, 1); got != "dropped" { // AC-57
		t.Errorf("AC-57: status = %s", got)
	}
	if got := coretest.QueryRows(t, ws, `SELECT reason FROM followup WHERE id = 1`)[0][0]; got != "不要になった" { // AC-58
		t.Errorf("AC-58: reason = %v", got)
	}
	rows := fuActivity(t, ws, "followup_drop")
	if len(rows) != 1 || rows[0][2] != "tty_confirm" {
		t.Errorf("activity = %v, want one tty_confirm record", rows)
	}
}

// AC-59 #774: 端末が無く --relayed も無い drop は tty_required で、status は open のまま。
func TestFollowupDrop_AC59_NoTerminalNoRelayedIsTTYRequired(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := runPiped(t, []string{"followup", "drop", "F-1", "--reason", "r", "--workspace", ws, "--json"})
	if code != 1 || readErrorCode(t, stderr) != string(CodeTTYRequired) {
		t.Errorf("AC-59: code=%d stderr=%s", code, stderr)
	}
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-59: status changed")
	}
}

// AC-60・AC-61・AC-62・AC-67 #774: --relayed の drop は端末が無くても成功し、作業ログの followup_drop が 1 件増え、
// after に relay_session・relay_quote が残る。
func TestFollowupDrop_AC60_AC61_AC62_AC67_RelayedRecordsSessionQuoteAndOneActivity(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	before := len(fuActivity(t, ws, "followup_drop"))
	code, _, stderr := fuRun(t, ws, fuArgs("followup drop F-1 --reason r "+fuRelayed)...)
	if code != 0 { // AC-60
		t.Fatalf("AC-60: exit=%d stderr=%s", code, stderr)
	}
	rows := fuActivity(t, ws, "followup_drop")
	if len(rows) != before+1 { // AC-67
		t.Fatalf("AC-67: followup_drop rows %d -> %d", before, len(rows))
	}
	after := fuAfter(t, rows[len(rows)-1])
	if after["relay_session"] != "SESS-F" { // AC-61
		t.Errorf("AC-61: after = %v", after)
	}
	if after["relay_quote"] != "QUOTE-F" { // AC-62
		t.Errorf("AC-62: after = %v", after)
	}
}

// AC-63 #774: --reason が無い drop は usage_error。
func TestFollowupDrop_AC63_MissingReasonIsUsageError(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup drop F-1 "+fuRelayed)...)
	fuWantCode(t, "AC-63", code, stderr, CodeUsageError)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-63: status changed")
	}
}

// AC-64 #774: 未のものと存在しない ID を並べると not_found で、未のものは open のまま。
func TestFollowupDrop_AC64_UnknownIDIsNotFoundAndChangesNothing(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup drop F-1 F-99 --reason r "+fuRelayed)...)
	fuWantCode(t, "AC-64", code, stderr, CodeNotFound)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-64: F-1 changed")
	}
}

// AC-65 #774: 同じ ID を 2 度並べた drop は usage_error。
func TestFollowupDrop_AC65_DuplicateIDIsUsageError(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup drop F-1 F-1 --reason r "+fuRelayed)...)
	fuWantCode(t, "AC-65", code, stderr, CodeUsageError)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-65: F-1 changed")
	}
}

// AC-66 #774: 未のものと捨てたものを並べると validation_failed で、未のものは open のまま。
func TestFollowupDrop_AC66_NonOpenIsValidationFailedAndChangesNothing(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup drop F-1 F-5 --reason r "+fuRelayed)...)
	fuWantCode(t, "AC-66", code, stderr, CodeValidationFailed)
	if fuStatus(t, ws, 1) != "open" {
		t.Error("AC-66: F-1 changed")
	}
}

// AC-68・AC-69・AC-70・AC-71 #774: 2 件を F-2 F-1 の順に指定しても、要約に 2 件の ID と題名が出て、打ち込む対象は
// F-1,F-2。それを打ち込むと 2 件とも dropped。
func TestFollowupDrop_AC68_AC69_AC70_AC71_SummaryAndTargetStringForTwoIDs(t *testing.T) {
	ws := fuWorkspace(t)
	code, pty, stdout, stderr := runConfirmedChild(t, []string{"followup", "drop", "F-2", "F-1", "--reason", "r", "--workspace", ws, "--json"}, "F-1,F-2")
	if code != 0 {
		t.Fatalf("exit=%d pty=%q stdout=%s stderr=%s", code, pty, stdout, stderr)
	}
	for _, want := range []string{"F-1", "F-2"} { // AC-68
		if !strings.Contains(pty, want) {
			t.Errorf("AC-68: summary lacks %s: %q", want, pty)
		}
	}
	for _, want := range []string{"題名 1", "題名 2"} { // AC-69
		if !strings.Contains(pty, want) {
			t.Errorf("AC-69: summary lacks %s: %q", want, pty)
		}
	}
	if !strings.Contains(pty, confirmPrompt("F-1,F-2")) { // AC-70
		t.Errorf("AC-70: prompt does not ask for F-1,F-2: %q", pty)
	}
	if fuStatus(t, ws, 1) != "dropped" || fuStatus(t, ws, 2) != "dropped" { // AC-71
		t.Error("AC-71: not both dropped")
	}
}

// AC-72 #774: 打ち込んだ文字列が対象と違うと confirmation_mismatch で、2 件とも open のまま。
func TestFollowupDrop_AC72_MismatchedConfirmationChangesNothing(t *testing.T) {
	ws := fuWorkspace(t)
	code, _, _, stderr := runConfirmedChild(t, []string{"followup", "drop", "F-1", "F-2", "--reason", "r", "--workspace", ws, "--json"}, "F-2,F-1")
	if code == 0 || readErrorCode(t, stderr) != string(CodeConfirmationMismatch) {
		t.Errorf("AC-72: code=%d stderr=%s", code, stderr)
	}
	if fuStatus(t, ws, 1) != "open" || fuStatus(t, ws, 2) != "open" {
		t.Error("AC-72: a followup changed")
	}
}

func fuPromote(t *testing.T, ws, id string) map[string]any {
	t.Helper()
	code, stdout, stderr := fuRun(t, ws, fuArgs("followup promote "+id+" "+fuRelayed)...)
	if code != 0 {
		t.Fatalf("promote %s: exit=%d stderr=%s", id, code, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout: %v", err)
	}
	return doc
}

func fuChallengeRow(t *testing.T, ws, id string) []any {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT status, title, description, done_criteria, origin, followup_of FROM challenge WHERE id = ?`, challengeIDToInternalID(t, id))[0]
}

// AC-76〜AC-86 #774: 課題に属する未のフォローアップを端末での本人確認で上げると、課題が 1 つ増え、作った課題の各列と
// フォローアップの上げた先が決まりどおりになる。
func TestFollowupPromote_AC76_to_AC86_CreatesChallengeFromFollowup(t *testing.T) {
	ws := fuWorkspace(t)
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	code, _, stdout, stderr := runConfirmedChild(t, []string{"followup", "promote", "F-1", "--workspace", ws, "--json"}, "F-1")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != fmt.Sprint(atoiOrFail(t, before)+1) { // AC-76
		t.Errorf("AC-76: challenge rows %s -> %s", before, got)
	}
	var doc struct {
		Challenge struct {
			ID string `json:"id"`
		} `json:"challenge"`
		Followup struct {
			ID                  string `json:"id"`
			Status              string `json:"status"`
			PromotedChallengeID string `json:"promoted_challenge_id"`
		} `json:"followup"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	row := fuChallengeRow(t, ws, doc.Challenge.ID)
	if row[0] != "unclassified" { // AC-77
		t.Errorf("AC-77: status = %v", row[0])
	}
	if row[1] != "題名 1" { // AC-78
		t.Errorf("AC-78: title = %v", row[1])
	}
	if row[2] != "説明 1" { // AC-79
		t.Errorf("AC-79: description = %v", row[2])
	}
	if row[3] != "" { // AC-81
		t.Errorf("AC-81: done_criteria = %v", row[3])
	}
	if row[4] != "followup" { // AC-82
		t.Errorf("AC-82: origin = %v", row[4])
	}
	if row[5] != "C-1" { // AC-83
		t.Errorf("AC-83: followup_of = %v", row[5])
	}
	if got := coretest.QueryRows(t, ws, `SELECT promoted_challenge_id FROM followup WHERE id = 1`)[0][0]; fmt.Sprint(got) != fmt.Sprint(challengeIDToInternalID(t, doc.Challenge.ID)) { // AC-85
		t.Errorf("AC-85: promoted_challenge_id = %v, want %s", got, doc.Challenge.ID)
	}
	if fuStatus(t, ws, 1) != "open" || doc.Followup.Status != "open" { // AC-86
		t.Errorf("AC-86: status = %s / %s", fuStatus(t, ws, 1), doc.Followup.Status)
	}
	if doc.Followup.ID != "F-1" || doc.Followup.PromotedChallengeID != doc.Challenge.ID {
		t.Errorf("--json root followup = %+v", doc.Followup)
	}
}

func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-80・AC-84 #774: detail が NULL で課題に属さないフォローアップから作った課題は、description が空の文字列、origin が manual。
func TestFollowupPromote_AC80_AC84_NullDetailAndNoChallenge(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	doc := fuPromote(t, ws, "F-3")
	row := fuChallengeRow(t, ws, doc["challenge"].(map[string]any)["id"].(string))
	if row[2] != "" { // AC-80
		t.Errorf("AC-80: description = %#v", row[2])
	}
	if row[4] != "manual" { // AC-84
		t.Errorf("AC-84: origin = %v", row[4])
	}
	if row[5] != nil {
		t.Errorf("followup_of = %v, want NULL", row[5])
	}
}

// AC-87 #774: 端末が無く --relayed も無い promote は tty_required で、challenge の行は増えない。
func TestFollowupPromote_AC87_NoTerminalNoRelayedIsTTYRequired(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	code, _, stderr := runPiped(t, []string{"followup", "promote", "F-1", "--workspace", ws, "--json"})
	if code != 1 || readErrorCode(t, stderr) != string(CodeTTYRequired) {
		t.Errorf("AC-87: code=%d stderr=%s", code, stderr)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != before {
		t.Errorf("AC-87: challenge rows %s -> %s", before, got)
	}
}

// AC-88・AC-89・AC-90・AC-91・AC-92 #774: --relayed の promote は端末が無くても成功し、followup_promote の after に
// relay_quote・relay_session が残り、作業ログに followup_promote と、作った課題の create が 1 件ずつ増える。
func TestFollowupPromote_AC88_to_AC92_RelayedRecordsEvidenceAndActivities(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	beforeCreate := fuCount(t, ws, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND action = 'create'`)
	beforePromote := len(fuActivity(t, ws, "followup_promote"))
	code, _, stderr := fuRun(t, ws, fuArgs("followup promote F-1 "+fuRelayed)...)
	if code != 0 { // AC-88
		t.Fatalf("AC-88: exit=%d stderr=%s", code, stderr)
	}
	rows := fuActivity(t, ws, "followup_promote")
	if len(rows) != beforePromote+1 { // AC-91
		t.Fatalf("AC-91: followup_promote rows %d -> %d", beforePromote, len(rows))
	}
	after := fuAfter(t, rows[len(rows)-1])
	if after["relay_quote"] != "QUOTE-F" { // AC-89
		t.Errorf("AC-89: after = %v", after)
	}
	if after["relay_session"] != "SESS-F" { // AC-90
		t.Errorf("AC-90: after = %v", after)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND action = 'create'`); got != fmt.Sprint(atoiOrFail(t, beforeCreate)+1) { // AC-92
		t.Errorf("AC-92: challenge create rows %s -> %s", beforeCreate, got)
	}
}

// AC-93 #774: 済みのフォローアップの promote は validation_failed。
func TestFollowupPromote_AC93_DoneIsValidationFailed(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup promote F-4 "+fuRelayed)...)
	fuWantCode(t, "AC-93", code, stderr, CodeValidationFailed)
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != "1" {
		t.Errorf("AC-93: challenge rows = %s, want 1", got)
	}
}

// AC-94 #774: 存在しない ID の promote は not_found。
func TestFollowupPromote_AC94_UnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	code, _, stderr := fuRun(t, ws, fuArgs("followup promote F-99 "+fuRelayed)...)
	fuWantCode(t, "AC-94", code, stderr, CodeNotFound)
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != "1" {
		t.Errorf("AC-94: challenge rows = %s, want 1", got)
	}
}

// AC-95 #774: 上げてあり、上げた先の課題が取り下げでないフォローアップの promote は validation_failed で、challenge の行は増えない。
func TestFollowupPromote_AC95_AlreadyPromotedIsValidationFailed(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	fuPromote(t, ws, "F-1")
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	code, _, stderr := fuRun(t, ws, fuArgs("followup promote F-1 "+fuRelayed)...)
	fuWantCode(t, "AC-95", code, stderr, CodeValidationFailed)
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != before {
		t.Errorf("AC-95: challenge rows %s -> %s", before, got)
	}
}

// AC-96 #774: 上げた先の課題が取り下げのフォローアップは、もう一度上げると新しい課題が作られ、上げた先が置き換わる。
func TestFollowupPromote_AC96_PromotedToWithdrawnCanBePromotedAgain(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	first := fuPromote(t, ws, "F-1")["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, first), "withdrawn")
	second := fuPromote(t, ws, "F-1")["challenge"].(map[string]any)["id"].(string)
	if second == first {
		t.Fatalf("AC-96: second challenge = first = %s", first)
	}
	if got := coretest.QueryRows(t, ws, `SELECT promoted_challenge_id FROM followup WHERE id = 1`)[0][0]; fmt.Sprint(got) != fmt.Sprint(challengeIDToInternalID(t, second)) {
		t.Errorf("AC-96: promoted_challenge_id = %v, want %s", got, second)
	}
}

// AC-97 #774: ID を 2 つ並べた promote は usage_error。
func TestFollowupPromote_AC97_TwoIDsIsUsageError(t *testing.T) {
	t.Parallel()
	ws := fuWorkspace(t)
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	code, _, stderr := fuRun(t, ws, fuArgs("followup promote F-1 F-2 "+fuRelayed)...)
	fuWantCode(t, "AC-97", code, stderr, CodeUsageError)
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != before {
		t.Errorf("AC-97: challenge rows %s -> %s", before, got)
	}
}

// AC-98 #774: 上げて作った課題に classify --auto を実行すると、分類の判断点（J1）の対象になる。
func TestFollowupPromote_AC98_PromotedChallengeIsClassifiedByJ1(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	createForCase(t, ws)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "題名 1", Detail: "説明 1"})
	id := fuPromote(t, ws, "F-1")["challenge"].(map[string]any)["id"].(string)
	_, stdinLog := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1MineFixture("P1")})
	runJSON(t, ws, "classify", "--auto", id)
	if got := fuChallengeRow(t, ws, id)[0]; got != "classified" {
		t.Errorf("AC-98: status = %v, want classified", got)
	}
	if b := fuReadFile(t, stdinLog); !strings.Contains(b, "題名 1") {
		t.Errorf("AC-98: the J1 input lacks the promoted challenge title: %q", b)
	}
}

func fuReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// AC-307 #774: docs/features/m1-core.md のコマンドの表に、followup list・done・drop・promote の行がある。
func TestFollowupCommandsAreInM1CoreCommandTable_AC307(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m1-core.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"followup list", "followup done", "followup drop", "followup promote"} {
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "| `flywheel "+sub) {
				found = true
			}
		}
		if !found {
			t.Errorf("AC-307: the command table has no row for %q", sub)
		}
	}
}

// AC-95 #774: 上げてあるフォローアップの promote は、端末での本人確認を求める前に validation_failed で終わる
// （確認の入力を待たず、challenge の行も増えない）。
func TestFollowupPromote_AC95_AlreadyPromotedFailsBeforeConfirmation(t *testing.T) {
	ws := fuWorkspace(t)
	fuPromote(t, ws, "F-1")
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	code, pty, _, stderr := runConfirmedChild(t, []string{"followup", "promote", "F-1", "--workspace", ws, "--json"}, "F-1")
	fuWantCode(t, "AC-95", code, stderr, CodeValidationFailed)
	if strings.Contains(pty, confirmPrompt("F-1")) {
		t.Errorf("AC-95: the confirmation prompt was shown: %q", pty)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != before {
		t.Errorf("AC-95: challenge rows %s -> %s", before, got)
	}
}
