package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは契約の requested_action.resolvable_by（answer | terminal）の扱いを、実ストア・偽の ContractClient で検証する。

func (f *contractFixture) lastQuestion(t *testing.T) string {
	t.Helper()
	var q string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold ORDER BY id DESC LIMIT 1`).Scan(&q)
	}); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestResolvableByTerminal_OnlyTheValueDecides(t *testing.T) {
	for v, want := range map[string]bool{"": false, "answer": false, "terminal": true, "other": true, "invalid": true} {
		if got := resolvableByTerminal(v); got != want {
			t.Errorf("resolvableByTerminal(%q) = %v, want %v", v, got, want)
		}
	}
}

// 写し先は resolvable_by だけで分かれ、text や接続ツール固有の語は読まない（text を変えても写し先は変わらない）。
func TestContractWaitDestination_DoesNotReadText(t *testing.T) {
	for _, text := range []string{"", "answer", "terminal", "harness approve <run>"} {
		for _, rb := range []string{"", "answer", "terminal"} {
			got := contractWaitDestination(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: text, ResolvableBy: rb}, nil)
			if got != contractWaitOwnerHold {
				t.Errorf("text=%q resolvable_by=%q: dest = %v, want owner hold", text, rb, got)
			}
		}
	}
}

// decider が parent で kind が answer の待ちでも、terminal（閉集合の外の値を含む）なら J4（resume で答えを返す）へ回さず
// オーナーの保留へ写す。answer・項目なしは今までどおり J4 へ回す。
func TestContractWaitDestination_ParentAnswerTerminalGoesToOwnerHold(t *testing.T) {
	for rb, want := range map[string]contractWaitDest{
		"":         contractWaitParent,
		"answer":   contractWaitParent,
		"terminal": contractWaitOwnerHold,
		"unknown":  contractWaitOwnerHold,
	} {
		got := contractWaitDestination(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "x", ResolvableBy: rb}, nil)
		if got != want {
			t.Errorf("resolvable_by=%q: dest = %v, want %v", rb, got, want)
		}
	}
}

// answer・項目なしの保留は、今までどおり resume で答えを返すと案内する。
func TestContractWaitingQuestion_AnswerStillSaysResume(t *testing.T) {
	for _, rb := range []string{"", "answer"} {
		q := contractWaitingQuestion("ext-1", "要約", &ContractRequestedAction{Kind: "approve", Decider: "human", Text: "T", ResolvableBy: rb}, true)
		if !strings.Contains(q, "回答すると、その文を接続ツールへ返して続ける。") || !strings.Contains(q, "求められた操作: T") {
			t.Errorf("resolvable_by=%q: question = %q", rb, q)
		}
	}
}

// terminal（と閉集合の外の値）の保留は、text を逐語で先頭に案内し、resume を送ると案内しない。
func TestContractWaitingQuestion_TerminalLeadsWithTextAndDoesNotPromiseResume(t *testing.T) {
	for _, rb := range []string{"terminal", "other"} {
		text := "  端末で  X を実行 してください  "
		q := contractWaitingQuestion("ext-1", "要約", &ContractRequestedAction{Kind: "approve", Decider: "human", Text: text, ResolvableBy: rb}, true)
		if !strings.HasPrefix(q, "接続ツールの出力（text）: "+strings.TrimSpace(text)+"\n") {
			t.Errorf("resolvable_by=%q: question = %q, want text first", rb, q)
		}
		if strings.Contains(q, "返して続ける") || strings.Contains(q, "再開の指示だけを") {
			t.Errorf("resolvable_by=%q: question = %q, must not promise resume", rb, q)
		}
		if !strings.Contains(q, "resume では解けない") || !strings.Contains(q, "次の周で接続ツールの run の状態を読み直す") {
			t.Errorf("resolvable_by=%q: question = %q", rb, q)
		}
	}
}

// 保留の問いは画面にそのまま出るので、画面の文では「端末」と書かず「ターミナル」と書く（仕様 core-redesign-s3d の語の見直し）。
func TestContractWaitingQuestion_TerminalSaysTerminalNotTanmatsu(t *testing.T) {
	for _, rb := range []string{"terminal", "other"} {
		q := contractWaitingQuestion("ext-1", "要約", &ContractRequestedAction{Kind: "approve", Decider: "human", Text: "T", ResolvableBy: rb}, true)
		if !strings.Contains(q, "この求めは接続ツールの外のターミナルで人が解く必要があり、") {
			t.Errorf("resolvable_by=%q: question = %q, want ターミナル", rb, q)
		}
		if strings.Contains(q, "端末") {
			t.Errorf("resolvable_by=%q: question = %q, must not contain 端末", rb, q)
		}
	}
}

// terminal の待ちへの回答では resume を呼ばず、次の周で status を読み直す。同じ答えで周ごとに読み直さない。
func TestPollContractRuns_TerminalAnswerDoesNotResumeAndRereadsStatus(t *testing.T) {
	for _, rb := range []string{"terminal", "other"} {
		t.Run(rb, func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			ra := &ContractRequestedAction{Kind: "approve", Decider: "human", Text: "TERMINAL-TEXT", ResolvableBy: rb}
			f.client.queue = []ContractLaunchOutput{waitingOut(ra, nil)}
			f.poll(t)
			if !strings.HasPrefix(f.lastQuestion(t), "接続ツールの出力（text）: TERMINAL-TEXT") {
				t.Fatalf("question = %q", f.lastQuestion(t))
			}
			f.answerLatestHold(t, f.cid, "done in terminal")
			statusesBefore := len(f.client.statuses)
			// 人が端末で解いたので run は進んだ。
			f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
			reps := f.poll(t)
			if len(f.client.resumes) != 0 {
				t.Fatalf("resumes = %v, want none for a terminal request", f.client.resumes)
			}
			if len(f.client.statuses) != statusesBefore+1 {
				t.Fatalf("statuses = %d, want %d（status を読み直す）", len(f.client.statuses), statusesBefore+1)
			}
			if len(reps) != 1 || reps[0].State != ContractStateRunning || reps[0].Held {
				t.Fatalf("report = %+v, want running not held", reps)
			}
			f.poll(t)
			if len(f.client.resumes) != 0 {
				t.Fatalf("resumes = %v, want none", f.client.resumes)
			}
		})
	}
}

// 答えたのにまだ同じ terminal の待ちなら、新しい保留で改めて問う（resume は送らない）。
func TestPollContractRuns_TerminalStillWaitingAsksAgain(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	ra := &ContractRequestedAction{Kind: "approve", Decider: "human", Text: "T", ResolvableBy: "terminal"}
	f.client.queue = []ContractLaunchOutput{waitingOut(ra, nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "tried")
	reps := f.poll(t)
	if len(f.client.resumes) != 0 || len(reps) != 1 || !reps[0].Held {
		t.Fatalf("resumes = %v report = %+v, want held again without resume", f.client.resumes, reps)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 2 {
		t.Fatalf("holds = %d, want 2", n)
	}
}

// resolvable_by の値は契約の run へ保存され、次の周でも読める。
func TestRecordContractStatus_StoresResolvableBy(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "T", ResolvableBy: "terminal"}, nil)}
	f.poll(t)
	var got sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT requested_resolvable_by FROM contract_run`).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got.String != "terminal" {
		t.Fatalf("requested_resolvable_by = %v, want terminal", got)
	}
}

// resolvable_by が answer（または無し）なら、text が "terminal" でも答えは resume で返る（text は読まない）。
func TestPollContractRuns_AnswerStillResumesEvenIfTextSaysTerminal(t *testing.T) {
	for _, rb := range []string{"", "answer"} {
		f := newContractFixture(t)
		if _, err := f.start(t); err != nil {
			t.Fatal(err)
		}
		f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "terminal", ResolvableBy: rb}, nil)}
		f.poll(t)
		f.answerLatestHold(t, f.cid, "A")
		f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
		f.poll(t)
		if len(f.client.resumes) != 1 {
			t.Fatalf("resolvable_by=%q: resumes = %d, want 1", rb, len(f.client.resumes))
		}
	}
}
