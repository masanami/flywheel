package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 取り込み元の子（source_child）の記録・追従・unbind・bind の試験（#776。S2b）。
// 各試験の名前に対応する受入基準（AC-<番号>）を書く。上流は偽、ストアは実物。

type childFix struct {
	t       *testing.T
	s       *Store
	sources []SourceEntry
	up      *fakeUpstream
}

func newChildFix(t *testing.T) *childFix {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return &childFix{
		t: t, s: s,
		sources: []SourceEntry{
			excludeOthersSource("s", []string{"o/r"}, []string{"masanami"}),
			excludeOthersSource("s2", []string{"o/x"}, []string{"masanami"}),
		},
		up: &fakeUpstream{issues: map[string][]UpstreamIssue{}, login: "masanami", getIssue: map[string]UpstreamIssue{}, getIssueErr: map[string]error{}},
	}
}

// put は上流の一覧に Issue を足す（parent は親の外部キー。無ければ ""）。
func (f *childFix) put(key, parent string, assignees ...string) UpstreamIssue {
	repo, n, _ := splitExternalKey(key)
	is := issueFixture(key, fmt.Sprintf("https://github.com/%s/issues/%d", repo, n), "t-"+key, "b-"+key, "carol", assignees, nil)
	is.Repo, is.Number, is.ParentExternalKey = repo, n, parent
	f.up.issues[repo] = append(f.up.issues[repo], is)
	f.up.getIssue[key] = is
	return is
}

func (f *childFix) sel(specs ...string) (*IngestSelectedResult, error) {
	return f.s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: f.sources, Specs: specs, Upstream: f.up})
}

func (f *childFix) mustSel(specs ...string) *IngestSelectedResult {
	f.t.Helper()
	res, err := f.sel(specs...)
	if err != nil {
		f.t.Fatalf("IngestSelected(%v) error = %v", specs, err)
	}
	return res
}

func (f *childFix) follow() {
	f.t.Helper()
	if _, err := f.s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: f.sources, Upstream: f.up}); err != nil {
		f.t.Fatalf("Ingest() error = %v", err)
	}
}

func (f *childFix) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), q, args...).Scan(&n)
	}); err != nil {
		f.t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func (f *childFix) str(q string, args ...any) string {
	f.t.Helper()
	var v sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), q, args...).Scan(&v)
	}); err != nil {
		f.t.Fatalf("str %q: %v", q, err)
	}
	return v.String
}

func (f *childFix) children() int { return f.count(`SELECT COUNT(*) FROM source_child`) }

func (f *childFix) state(key string) string {
	return f.str(`SELECT upstream_state FROM source_child WHERE external_key = ?`, key)
}

// drop は上流の開いている一覧から key を外す（閉じた・消えた状態の準備）。
func (f *childFix) drop(key string) {
	repo, _, _ := splitExternalKey(key)
	var kept []UpstreamIssue
	for _, is := range f.up.issues[repo] {
		if is.ExternalKey != key {
			kept = append(kept, is)
		}
	}
	f.up.issues[repo] = kept
}

// twoChildren は o/r#1 の下に o/r#2・o/r#3 を置いて o/r#1 を取り込む。
func (f *childFix) twoChildren() *IngestSelectedResult {
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.put("o/r#3", "o/r#1")
	return f.mustSel("o/r#1")
}

func TestSourceChild_AC138_139_140_147_ParentWithTwoChildren(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	res := f.twoChildren()
	if n := countChallenges(t, f.s); n != 1 { // AC-138
		t.Errorf("AC-138 #776: challenges = %d, want 1", n)
	}
	if n := f.children(); n != 2 { // AC-139
		t.Errorf("AC-139 #776: source_child = %d, want 2", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM source_child WHERE challenge_id = 1`); n != 2 { // AC-140
		t.Errorf("AC-140 #776: children of the created challenge = %d, want 2", n)
	}
	if len(res.Ingested) != 1 || res.Ingested[0].Children != 2 { // AC-147
		t.Errorf("AC-147 #776: Ingested = %+v, want children=2", res.Ingested)
	}
	if got := f.str(`SELECT body FROM source_child WHERE external_key = 'o/r#2'`); got != "b-o/r#2" {
		t.Errorf("child body = %q, want the upstream body recorded", got)
	}
}

func TestSourceChild_AC141_142_GrandchildIsFlatWithDirectParent(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.put("o/r#3", "o/r#2")
	f.mustSel("o/r#1")
	if f.str(`SELECT external_key FROM source_child WHERE external_key = 'o/r#3'`) == "" || f.count(`SELECT COUNT(*) FROM source_child WHERE challenge_id = 1`) != 2 { // AC-141
		t.Errorf("AC-141 #776: grandchild is not recorded under the same challenge")
	}
	if got := f.str(`SELECT parent_external_key FROM source_child WHERE external_key = 'o/r#3'`); got != "o/r#2" { // AC-142
		t.Errorf("AC-142 #776: parent_external_key = %q, want o/r#2", got)
	}
}

func TestSourceChild_AC143_ClosedChildIsNotRecorded(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.drop("o/r#2") // 閉じた Issue は開いている Issue の一覧に無い
	f.mustSel("o/r#1")
	if n := f.children(); n != 0 {
		t.Errorf("AC-143 #776: source_child = %d, want 0", n)
	}
}

func TestSourceChild_AC144_OutOfPolicyChildIsNotRecorded(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1", "eve") // 他人に割り当て済み（exclude_others で方針に合わない）
	f.mustSel("o/r#1")
	if n := f.children(); n != 0 {
		t.Errorf("AC-144 #776: source_child = %d, want 0", n)
	}
}

func TestSourceChild_AC145_146_154_ExistingChallengeAndItsDescendantsAreNotChildren(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.put("o/r#3", "o/r#2")
	f.put("o/r#4", "o/r#1")
	f.mustSel("o/r#2") // 子を先に独立した課題にする（o/r#3 は o/r#2 の子として記録される）
	if f.children() != 1 {
		t.Fatalf("setup: source_child = %d, want 1 (o/r#3)", f.children())
	}
	f.mustSel("o/r#1")
	// o/r#1 の子として入るのは o/r#4 だけ。すでに課題の o/r#2 と、そのまた子の o/r#3 は入らない。
	if got := f.count(`SELECT COUNT(*) FROM source_child WHERE challenge_id = 2`); got != 1 {
		t.Errorf("children of the parent = %d, want 1 (o/r#4 only)", got)
	}
	if f.count(`SELECT COUNT(*) FROM source_child WHERE external_key = 'o/r#2'`) != 0 { // AC-145・AC-154
		t.Errorf("AC-145/154 #776: an existing challenge was recorded as a child")
	}
	if f.str(`SELECT challenge_id FROM source_child WHERE external_key = 'o/r#3'`) != "1" { // AC-146
		t.Errorf("AC-146 #776: the grandchild under an existing challenge moved to the parent")
	}
}

func TestSourceChild_AC148_149_ChildInAnotherDeclaredSourceKeepsItsSourceAndUndeclaredIsSkipped(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/x#5", "o/r#1") // 宣言にある別の取り込み元（s2）
	f.put("o/y#6", "o/r#1") // 宣言に無いリポジトリ
	f.mustSel("o/r#1")
	if got := f.str(`SELECT source_id FROM source_child WHERE external_key = 'o/x#5'`); got != "s2" { // AC-148
		t.Errorf("AC-148 #776: source_id = %q, want s2", got)
	}
	if f.count(`SELECT COUNT(*) FROM source_child WHERE external_key = 'o/y#6'`) != 0 { // AC-149
		t.Errorf("AC-149 #776: a child in an undeclared repo was recorded")
	}
}

func TestSourceChild_AC150_ARecordedChildCannotBeSelected(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	_, err := f.sel("o/r#2")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-150 #776: error = %v, want ErrValidation", err)
	}
	if countChallenges(t, f.s) != 1 {
		t.Errorf("a challenge was created")
	}
}

func TestSourceChild_AC151_ParentAndItsChildInOneCommandIsRejected(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	for _, specs := range [][]string{{"o/r#1", "o/r#2"}, {"o/r#2", "o/r#1"}} {
		_, err := f.sel(specs...)
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("AC-151 #776: %v error = %v, want ErrValidation", specs, err)
		}
	}
	if n := countChallenges(t, f.s); n != 0 {
		t.Errorf("AC-151 #776: challenges = %d, want 0", n)
	}
	if f.children() != 0 {
		t.Errorf("source_child rows were written")
	}
}

func TestSourceChild_AC152_153_AnIssueWithAParentCanBeSelectedAlone(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	res := f.mustSel("o/r#2")
	if len(res.Ingested) != 1 || res.Ingested[0].ExternalKey != "o/r#2" || countChallenges(t, f.s) != 1 { // AC-152
		t.Errorf("AC-152 #776: Ingested = %+v, want o/r#2 as a challenge", res.Ingested)
	}
	if f.children() != 0 { // AC-153
		t.Errorf("AC-153 #776: source_child = %d, want 0", f.children())
	}
}

func TestSourceChild_AC155_CyclicParentsAreNotEachOthersDescendants(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "o/r#2")
	f.put("o/r#2", "o/r#1")
	f.mustSel("o/r#1")
	if n := f.children(); n != 0 {
		t.Errorf("AC-155 #776: source_child = %d, want 0", n)
	}
}

// 輪の外の子は、輪の相手だけを外して記録される（AC-155 の陽性の対照）。
func TestSourceChild_AC155_ChildOutsideTheCycleIsStillRecorded(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "o/r#2")
	f.put("o/r#2", "o/r#1")
	f.put("o/r#3", "o/r#1")
	f.mustSel("o/r#1")
	if n := f.children(); n != 1 || f.count(`SELECT COUNT(*) FROM source_child WHERE external_key = 'o/r#3'`) != 1 {
		t.Errorf("source_child = %d, want only o/r#3", n)
	}
}

func TestSourceChild_ChainLongerThan16LevelsStopsAt16(t *testing.T) { // #776（16 段の上限）
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	for i := 2; i <= 19; i++ {
		f.put(fmt.Sprintf("o/r#%d", i), fmt.Sprintf("o/r#%d", i-1))
	}
	f.mustSel("o/r#1")
	if n := f.children(); n != 16 {
		t.Errorf("source_child = %d, want 16 (o/r#2..o/r#17)", n)
	}
}

func TestSourceChild_AC156_BindingKeyIsTheUpstreamCanonicalKey(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.mustSel("O/R#1")
	if got := f.str(`SELECT external_key FROM source_binding`); got != "o/r#1" {
		t.Errorf("AC-156 #776: external_key = %q, want o/r#1", got)
	}
}

func TestSourceChild_AC157_ChallengeParentIsStillSyncedBetweenChallenges(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.mustSel("o/r#2")
	f.mustSel("o/r#1")
	f.follow()
	if n := f.count(`SELECT COUNT(*) FROM challenge_parent WHERE challenge_id = 1 AND parent_id = 2`); n != 1 {
		t.Errorf("AC-157 #776: challenge_parent rows = %d, want 1", n)
	}
}

// --- 追従 ---

func TestSourceChild_AC114_ClosedUpstreamChildBecomesClosed(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.drop("o/r#2")
	closed := f.up.getIssue["o/r#2"]
	closed.State = "closed"
	f.up.getIssue["o/r#2"] = closed
	f.follow()
	if got := f.state("o/r#2"); got != "closed" {
		t.Errorf("AC-114 #776: upstream_state = %q, want closed", got)
	}
	if got := f.state("o/r#3"); got != "open" {
		t.Errorf("the other child = %q, want open", got)
	}
}

func TestSourceChild_AC115_GoneUpstreamChildBecomesMissing(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.drop("o/r#2")
	f.up.getIssueErr["o/r#2"] = ErrUpstreamIssueNotFound
	f.follow()
	if got := f.state("o/r#2"); got != "missing" {
		t.Errorf("AC-115 #776: upstream_state = %q, want missing", got)
	}
}

func TestSourceChild_AC116_UnconfirmedChildKeepsItsState(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.drop("o/r#2")
	f.up.getIssueErr["o/r#2"] = errors.New("network down")
	f.follow()
	if got := f.state("o/r#2"); got != "open" {
		t.Errorf("AC-116 #776: upstream_state = %q, want open (unchanged)", got)
	}
}

func TestSourceChild_AC117_ChangedTitleAndBodyAreFollowed(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	repo := f.up.issues["o/r"]
	for i := range repo {
		if repo[i].ExternalKey == "o/r#2" {
			repo[i].Title, repo[i].Body = "renamed", "new body"
		}
	}
	f.follow()
	if got := f.str(`SELECT title FROM source_child WHERE external_key = 'o/r#2'`); got != "renamed" {
		t.Errorf("AC-117 #776: title = %q, want renamed", got)
	}
	if got := f.str(`SELECT body FROM source_child WHERE external_key = 'o/r#2'`); got != "new body" {
		t.Errorf("body = %q, want new body", got)
	}
}

func TestSourceChild_AC118_ChildAddedUpstreamLaterIsNotRecorded(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.put("o/r#9", "o/r#1")
	f.follow()
	if n := f.children(); n != 2 {
		t.Errorf("AC-118 #776: source_child = %d, want 2", n)
	}
	if n := countChallenges(t, f.s); n != 1 {
		t.Errorf("challenges = %d, want 1 (following never creates one)", n)
	}
}

// --- unbind・取り下げ・bind ---

func (f *childFix) insertRun(ended bool) int64 {
	f.t.Helper()
	endedAt := "NULL"
	if ended {
		endedAt = "'2026-10-01T00:00:00Z'"
	}
	var id int64
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (1, 'contract', 1, 's', 1, 'h', 'x', 'x', ` + endedAt + `, NULL, 1000000, 'impl')`)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		f.t.Fatalf("insert run: %v", err)
	}
	return id
}

func (f *childFix) insertContractRun(startRun int64) {
	f.t.Helper()
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
			VALUES (1, 'tool', 'ext-1', ?, 'running', 's', 1000000, 'x', 'x')`, startRun)
		return err
	})
	if err != nil {
		f.t.Fatalf("insert contract_run: %v", err)
	}
}

func (f *childFix) unbind() error {
	_, err := f.s.UnbindChallenge(context.Background(), ChannelCLI, "C-1")
	return err
}

func TestSourceChild_AC158_UnbindRemovesTheChildren(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	if err := f.unbind(); err != nil {
		t.Fatalf("UnbindChallenge() error = %v", err)
	}
	if n := f.children(); n != 0 {
		t.Errorf("AC-158 #776: source_child = %d, want 0", n)
	}
	// AC-162: 消した子は取り込めるものへ戻る。
	scan, err := f.s.ScanIngestable(context.Background(), f.sources, f.up)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range scan.Candidates {
		got[c.Issue.ExternalKey] = true
	}
	if !got["o/r#2"] || !got["o/r#3"] || !got["o/r#1"] {
		t.Errorf("AC-162 #776: ingestable = %v, want the removed children (and the unbound parent) back", got)
	}
}

func TestSourceChild_AC159_UnbindWithAnUnfinishedContractRunIsRejected(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.insertContractRun(f.insertRun(true)) // 契約の実行は終わっていない（run は終わっている）
	if err := f.unbind(); !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-159 #776: error = %v, want ErrValidation", err)
	}
	if n := f.children(); n != 2 {
		t.Errorf("AC-159 #776: source_child = %d, want 2 (kept)", n)
	}
	if f.count(`SELECT COUNT(*) FROM source_binding`) != 1 {
		t.Errorf("the binding was removed")
	}
}

func TestSourceChild_AC160_UnbindWithAnUnfinishedRunIsRejected(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	f.insertRun(false)
	if err := f.unbind(); !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-160 #776: error = %v, want ErrValidation", err)
	}
	if n := f.children(); n != 2 {
		t.Errorf("AC-160 #776: source_child = %d, want 2 (kept)", n)
	}
}

func TestSourceChild_AC161_UnbindWithoutChildrenIgnoresAnUnfinishedContractRun(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.mustSel("o/r#1")
	f.insertContractRun(f.insertRun(true))
	if err := f.unbind(); err != nil {
		t.Fatalf("AC-161 #776: UnbindChallenge() error = %v, want success", err)
	}
	if f.count(`SELECT COUNT(*) FROM source_binding`) != 0 {
		t.Errorf("the binding was not removed")
	}
}

func TestSourceChild_AC163_WithdrawingKeepsTheChildren(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	c, err := f.s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: "C-1", ExpectedVersion: c.Version, Reason: "いらない"}, fuAtt(t, "C-1")); err != nil {
		t.Fatalf("ExecuteWithdraw() error = %v", err)
	}
	if n := f.children(); n != 2 {
		t.Errorf("AC-163 #776: source_child = %d, want 2", n)
	}
}

func TestSourceChild_AC164_BindRejectsARecordedChild(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "manual", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.BindChallenge(context.Background(), ChannelCLI, BindInput{ChallengeID: ch.ID, ExternalKey: "o/r#2", Sources: f.sources, Upstream: f.up})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-164 #776: error = %v, want ErrValidation", err)
	}
	if f.count(`SELECT COUNT(*) FROM source_binding`) != 1 {
		t.Errorf("a binding was created")
	}
}

func TestSourceChild_AC291_292_DetailCarriesTheChildren(t *testing.T) { // #776
	t.Parallel()
	f := newChildFix(t)
	f.twoChildren()
	d, err := f.s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.SourceChildren) != 2 || d.SourceChildren[0].ExternalKey != "o/r#2" || d.SourceChildren[0].ParentExternalKey != "o/r#1" ||
		d.SourceChildren[0].Title != "t-o/r#2" || d.SourceChildren[0].UpstreamState != "open" || d.SourceChildren[0].URL == "" {
		t.Errorf("SourceChildren = %+v", d.SourceChildren)
	}
}

// 記録した子の追従で、ストアへの書き込みに失敗したときは、黙って捨てず、その子の失敗の項目として出す
// （取り込みの全体は止めない。ほかの子の追従は続ける）。開いている一覧にある子と、一覧から消えた子の両方で確かめる。
func TestSourceChild_FollowUpWriteFailureIsReportedAsAFailedItem(t *testing.T) { // #770 の仕上げ
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "o/r#1")
	f.put("o/r#3", "o/r#1")
	f.put("o/r#4", "o/r#1")
	f.mustSel("o/r#1")
	// o/r#2 は題名が変わる（開いている一覧にある）。o/r#3 は上流で閉じた（一覧から消えた）。o/r#4 は題名が変わる（失敗させない）。
	repo := f.up.issues["o/r"]
	for i := range repo {
		switch repo[i].ExternalKey {
		case "o/r#2":
			repo[i].Title = "renamed 2"
		case "o/r#4":
			repo[i].Title = "renamed 4"
		}
	}
	f.drop("o/r#3")
	closed := f.up.getIssue["o/r#3"]
	closed.State = "closed"
	f.up.getIssue["o/r#3"] = closed
	// o/r#2 と o/r#3 の行の更新だけを失敗させる。
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_child_update BEFORE UPDATE ON source_child
			WHEN NEW.external_key IN ('o/r#2', 'o/r#3') BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		return err
	}); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	res, err := f.s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: f.sources, Upstream: f.up})
	if err != nil {
		t.Fatalf("Ingest() error = %v, want nil (a child's write failure must not stop the ingest)", err)
	}
	failed := map[string]string{}
	for _, sr := range res.Sources {
		for _, rr := range sr.Repos {
			for _, it := range rr.Items {
				if it.Result == IngestOutcomeFailed && it.Error != nil {
					failed[it.ExternalKey] = *it.Error
				}
			}
		}
	}
	for _, key := range []string{"o/r#2", "o/r#3"} {
		if msg, ok := failed[key]; !ok || !strings.Contains(msg, "boom") {
			t.Errorf("failed item of %s = %q (found=%v), want a failed item carrying the write error", key, msg, ok)
		}
	}
	if len(failed) != 2 {
		t.Errorf("failed items = %v, want exactly the 2 children whose write failed", failed)
	}
	if got := f.str(`SELECT title FROM source_child WHERE external_key = 'o/r#2'`); got != "t-o/r#2" {
		t.Errorf("title of o/r#2 = %q, want unchanged", got)
	}
	if got := f.state("o/r#3"); got != "open" {
		t.Errorf("state of o/r#3 = %q, want open (the write failed)", got)
	}
	if got := f.str(`SELECT title FROM source_child WHERE external_key = 'o/r#4'`); got != "renamed 4" {
		t.Errorf("title of o/r#4 = %q, want renamed 4 (the other child is still followed)", got)
	}
}

// AC-131（#775 の確かめの補い）: 上流で閉じている Issue（Issue はあるが、開いている Issue の一覧に無い）を指定すると、
// 検査のエラーになり、課題は増えない。
func TestIngestSelected_AC131_AClosedIssueCannotBeSelected(t *testing.T) {
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "")
	f.drop("o/r#1")
	closed := f.up.getIssue["o/r#1"]
	closed.State = "closed"
	f.up.getIssue["o/r#1"] = closed
	if _, err := f.sel("o/r#1"); !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-131: error = %v, want ErrValidation", err)
	}
	if _, err := f.sel("o/r#2", "o/r#1"); !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-131: error with an ingestable one = %v, want ErrValidation", err)
	}
	if n := countChallenges(t, f.s); n != 0 {
		t.Errorf("AC-131: challenges = %d, want 0", n)
	}
}

// 宣言にあるほかのリポジトリの一覧を読めなかったとき、<owner>/<repo>#<番号> の指定は、その Issue のリポジトリを
// 読めれば受ける（読めたリポジトリの取り込みは続ける）。読めなかったリポジトリにある子は、取り込めるものに入らないので、
// 子として記録されない（いまの決まり。読めなかったときに止めるかは、仕様が決めていない）。
func TestIngestSelected_UnreadableOtherRepoDoesNotBlockAnExplicitSelection(t *testing.T) { // #770 の仕上げ
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/x#5", "o/r#1") // 宣言にある別の取り込み元（s2）の子
	f.up.errs = map[string]error{"o/x": errors.New("list failed")}
	res := f.mustSel("o/r#1")
	if len(res.Ingested) != 1 || res.Ingested[0].Children != 0 {
		t.Fatalf("Ingested = %+v, want 1 challenge with 0 children (the child's repo could not be read)", res.Ingested)
	}
	// 読めなかったリポジトリの子は、読めるようになると、取り込めるものに（親の課題つきで）現れる。
	f.up.errs = nil
	scan, err := f.s.ScanIngestable(context.Background(), f.sources, f.up)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Candidates) != 1 || scan.Candidates[0].Issue.ExternalKey != "o/x#5" {
		t.Fatalf("ingestable = %+v, want o/x#5 only", scan.Candidates)
	}
}

// AC-136（#775 の確かめの補い）: 2 つを指定し、上流を読んだ後に、片方がほかの操作で課題になっていたら、
// 検査のエラーになり、もう片方も課題にならない（全部の作成は 1 つのトランザクション）。
func TestIngestSelected_AC136_OneTakenAfterTheUpstreamReadCreatesNeitherOfTheTwo(t *testing.T) {
	t.Parallel()
	f := newChildFix(t)
	f.put("o/r#1", "")
	f.put("o/r#2", "")
	// 上流の一覧を読んだ直後（書き込みのトランザクションの前）に、ほかの操作が o/r#2 を課題にする。
	other := &fakeUpstream{issues: f.up.issues, login: "masanami", getIssue: f.up.getIssue, getIssueErr: map[string]error{}}
	var once sync.Once
	f.up.onListOpenIssues = func(repo string) {
		if repo != "o/x" { // 宣言の最後のリポジトリの一覧を読み終える時に差し込む
			return
		}
		once.Do(func() {
			if _, err := f.s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: f.sources, Specs: []string{"o/r#2"}, Upstream: other}); err != nil {
				t.Errorf("the other operation failed: %v", err)
			}
		})
	}
	_, err := f.sel("o/r#1", "o/r#2")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("AC-136: error = %v, want ErrValidation", err)
	}
	// 課題は、ほかの操作が作った o/r#2 の 1 件だけ。o/r#1 は課題になっていない。
	if n := countChallenges(t, f.s); n != 1 {
		t.Fatalf("AC-136: challenges = %d, want 1 (only the one made by the other operation)", n)
	}
	if got := f.str(`SELECT external_key FROM source_binding`); got != "o/r#2" {
		t.Errorf("AC-136: binding = %q, want o/r#2 only", got)
	}
}
