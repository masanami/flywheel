package core

// このファイルは、課題の取り込み元の Issue の番号を、core がフローの開始と反映の依頼へ入力 challenge_issue として渡すこと
// （flywheel#1424 の 1 点目）を、実ストア・偽の契約のコマンドで確かめる。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// bindIssueForTest は課題に、取り込み元の対応（externalKey）を作る。
func bindIssueForTest(t *testing.T, s *Store, challengeID, externalKey string) {
	t.Helper()
	cid := mustChallengeInternalID(t, challengeID)
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertSourceBinding(context.Background(), tx, time.Now(), cid, createSourceBindingInput{
			SourceID: "src", ExternalKey: externalKey, URL: "https://github.com/o/r/issues/1419",
			Fingerprint: "1:abc", UpstreamState: upstreamState("open"), PolicyState: policyStateInPolicy,
		})
		return err
	})
	if err != nil {
		t.Fatalf("insertSourceBinding: %v", err)
	}
}

// challengeIssueInputs はコマンドの `--input challenge_issue=` の値の並び。
func challengeIssueInputs(cmd []string) []string {
	var out []string
	for _, a := range cmd {
		if v, ok := strings.CutPrefix(a, "challenge_issue="); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestFlowCommand_ChallengeIssueInput(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	s.SetFlowExecutable(flowTestExe)
	for _, c := range []struct {
		cmd  contractCmd
		last string
	}{{contractCmdStart, "ticket"}, {contractCmdReflect, "reflect"}} {
		f := contractFill{ChallengeID: "C-1", EmptySource: true, Flow: "ticket", ArtifactRef: "integration/c-1", ChallengeIssue: "1419"}
		got, err := s.contractCommandArgs(flowConnector(t, ""), c.cmd, f)
		if err != nil || !containsPair(got, "--input", "challenge_issue=1419") || got[len(got)-1] != c.last {
			t.Errorf("%s = %q err %v, want --input challenge_issue=1419 before %s", c.cmd, got, err, c.last)
		}
		f.ChallengeIssue = ""
		got, err = s.contractCommandArgs(flowConnector(t, ""), c.cmd, f)
		if err != nil || len(challengeIssueInputs(got)) != 0 {
			t.Errorf("%s = %q err %v, want no challenge_issue input", c.cmd, got, err)
		}
	}
}

// 統合先を使う複数タスクの課題は、タスクの開始のすべてと反映の依頼に、取り込み元の番号を渡す（issue の入力は空のまま）。
// 取り込み元が無いとき・取り込み元の Issue が PR を作るリポジトリのものでないときは渡さない。
func TestFlowLaunch_IntegrationChallengePassesTheSourceIssue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, key, want string
	}{
		{name: "取り込み元がある", key: "o/r#1419", want: "1419"},
		{name: "リポジトリの大文字小文字は問わない", key: "O/R#1419", want: "1419"},
		{name: "取り込み元が無い"},
		{name: "取り込み元が別のリポジトリ", key: "o/ledger#1419"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			shim := &flowShimClient{}
			if c.key != "" {
				shim.setup = func(f *contractFixture, id string) { bindIssueForTest(t, f.s, id, c.key) }
			}
			_, _, _, res, _ := runIntegrationCycleWith(t, []string{"met"}, shim)
			tickets, reflects := 0, 0
			for _, cmd := range shim.raw {
				if cmd[len(cmd)-1] == "reflect" {
					reflects++
				} else {
					tickets++
					if !containsPair(cmd, "--input", "issue=") {
						t.Errorf("ticket start = %q, want an empty issue input for a task of a multi-task plan", cmd)
					}
				}
				got := challengeIssueInputs(cmd)
				if c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || got[0] != c.want) {
					t.Errorf("start = %q: challenge_issue = %v, want %q", cmd, got, c.want)
				}
			}
			if tickets != 2 || reflects != 1 {
				t.Fatalf("starts = %q, want 2 tickets and 1 reflect (end_reason %q)", shim.raw, res.Cycle.EndReason)
			}
		})
	}
}

// 統合先を使わない 1 タスクの課題は、取り込み元の番号を issue の入力で渡し（タスクの PR が Closes を書く）、challenge_issue は渡さない。
func TestFlowLaunch_SingleTaskWithoutIntegrationPassesOnlyTheIssueInput(t *testing.T) {
	t.Parallel()
	f := newFlowContractFixture(t, "")
	bindIssueForTest(t, f.s, f.cid, "o/r#1419")
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want one", f.client.starts)
	}
	cmd := f.client.starts[0]
	if !containsPair(cmd, "--input", "issue=1419") || !containsPair(cmd, "--input", "uses_integration=false") || len(challengeIssueInputs(cmd)) != 0 {
		t.Fatalf("start = %q, want issue=1419 and no challenge_issue", cmd)
	}
}

// 反映の段が始める反映の依頼も、統合先を使う課題では取り込み元の番号を渡し、統合先を使わない課題（成果物が PR）では渡さない。
func TestReflect_ReleaseStagePassesTheSourceIssueOnlyForIntegration(t *testing.T) {
	t.Parallel()
	for _, integ := range []bool{true, false} {
		f := newReflFixture(t)
		f.s.SetFlowExecutable(flowTestExe)
		conn, err := parseConnectorsDeclaration([]byte(strings.Replace(flowOneRepoConnectorsJSON(""), `"clone", "paths": ["slot1"]`, `"worktree", "base": "wt", "count": 2`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.validate(); err != nil {
			t.Fatal(err)
		}
		f.conn = conn
		bindIssueForTest(t, f.s, formatChallengeID(f.cid), "o/r#1419")
		if integ {
			if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE challenge SET uses_integration = 1, integration_ref = 'integration/c-1' WHERE id = ?`, f.cid)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		f.started(t)
		if len(f.client.starts) != 1 {
			t.Fatalf("starts = %v, want one reflect", f.client.starts)
		}
		got := challengeIssueInputs(f.client.starts[0])
		if integ && (len(got) != 1 || got[0] != "1419") || !integ && len(got) != 0 {
			t.Errorf("integration %v: reflect = %q, challenge_issue = %v", integ, f.client.starts[0], got)
		}
	}
}

// 反映の依頼の口が番号を受けない（form: cli）か、統合先を使う課題で口が form: flow のときで、タスクの開始に番号を渡すかが分かれる。
// 受けない口では渡さない（タスクの PR の Closes が残り、昇格の PR へ写る）。
func TestFlowLaunch_ChallengeIssueNeedsAFlowReflectConnector(t *testing.T) {
	t.Parallel()
	const cliReflect = `{"id": "up", "form": "cli", "permission_mode": "default", "contract_version": 1,
     "commands": {"start": ["up", "start", "{challenge_id}"], "status": ["up", "status", "{run_id}"], "resume": ["up", "resume", "{run_id}"],
       "cancel": ["up", "cancel", "{run_id}"], "reflect": ["up", "reflect", "{artifact_ref}", "{challenge_id}"]},
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]},
    {"id": "tool",`
	for _, cli := range []bool{false, true} {
		f := newFlowContractFixture(t, "")
		if cli {
			js := strings.Replace(flowOneRepoConnectorsJSON(""), `{"id": "tool",`, cliReflect, 1)
			js = strings.Replace(js, `"connector": "tool",`, `"connector": "tool", "reflect_connector": "up",`, 1)
			conn, err := parseConnectorsDeclaration([]byte(js))
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.validate(); err != nil {
				t.Fatal(err)
			}
			f.conn = conn
		}
		bindIssueForTest(t, f.s, f.cid, "o/r#1419")
		cid := mustChallengeInternalID(t, f.cid)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET uses_integration = 1 WHERE id = ?`, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.start(t); err != nil {
			t.Fatalf("start: %v", err)
		}
		if len(f.client.starts) != 1 || !containsPair(f.client.starts[0], "--input", "uses_integration=true") {
			t.Fatalf("starts = %q, want one integration start", f.client.starts)
		}
		got := challengeIssueInputs(f.client.starts[0])
		if cli && len(got) != 0 || !cli && (len(got) != 1 || got[0] != "1419") {
			t.Errorf("cli reflect %v: challenge_issue = %v (%q)", cli, got, f.client.starts[0])
		}
	}
}
