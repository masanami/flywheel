package core

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// docs/features/core-redesign-s4d.md S4d-1「宣言」（AC-63・64・98・103・104・131・140）。

func loadChatDecl(t *testing.T, json string) (*AgentDeclaration, error) {
	t.Helper()
	return LoadAgentDeclaration(writeAgentJSON(t, json))
}

// AC-63: chat_tools が無いとき、道具の範囲は restricted。
func TestChatDeclaration_ToolsDefaultsToRestricted(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{"no_file": "", "key_absent": `{"position_file": "p.md"}`} {
		t.Run(name, func(t *testing.T) {
			var decl *AgentDeclaration
			var err error
			if content == "" {
				decl, err = LoadAgentDeclaration(t.TempDir())
			} else {
				decl, err = loadChatDecl(t, content)
			}
			if err != nil {
				t.Fatal(err)
			}
			if decl.ChatTools != ChatToolsRestricted {
				t.Errorf("ChatTools = %q, want restricted", decl.ChatTools)
			}
		})
	}
}

func TestChatDeclaration_ToolsFullIsAccepted(t *testing.T) {
	t.Parallel()
	decl, err := loadChatDecl(t, `{"chat_tools": "full"}`)
	if err != nil {
		t.Fatal(err)
	}
	if decl.ChatTools != ChatToolsFull {
		t.Errorf("ChatTools = %q, want full", decl.ChatTools)
	}
}

// AC-64: restricted・full 以外は読み込みが誤り。
func TestChatDeclaration_ToolsOtherValuesAreInvalid(t *testing.T) {
	t.Parallel()
	for _, v := range []string{`"all"`, `""`, `"Full"`, `"restricted "`, `null`, `1`, `true`, `["full"]`} {
		t.Run(v, func(t *testing.T) {
			_, err := loadChatDecl(t, fmt.Sprintf(`{"chat_tools": %s}`, v))
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// AC-98・AC-103: chat_max_turns は無いとき 30、0 以下は誤り。
func TestChatDeclaration_MaxTurns(t *testing.T) {
	t.Parallel()
	decl, err := loadChatDecl(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if decl.ChatMaxTurns != 30 {
		t.Errorf("default ChatMaxTurns = %d, want 30", decl.ChatMaxTurns)
	}
	decl, err = loadChatDecl(t, `{"chat_max_turns": 2}`)
	if err != nil || decl.ChatMaxTurns != 2 {
		t.Fatalf("chat_max_turns 2: %v, %+v", err, decl)
	}
	for _, v := range []string{"0", "-1", "1.5", `"3"`, "null"} {
		t.Run(v, func(t *testing.T) {
			if _, err := loadChatDecl(t, fmt.Sprintf(`{"chat_max_turns": %s}`, v)); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// AC-104: chat_timeout_sec は既定 600、0 以下は誤り。
func TestChatDeclaration_TimeoutSec(t *testing.T) {
	t.Parallel()
	decl, err := loadChatDecl(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if decl.ChatTimeoutSec != 600 {
		t.Errorf("default ChatTimeoutSec = %d, want 600", decl.ChatTimeoutSec)
	}
	decl, err = loadChatDecl(t, `{"chat_timeout_sec": 5}`)
	if err != nil || decl.ChatTimeoutSec != 5 {
		t.Fatalf("chat_timeout_sec 5: %v, %+v", err, decl)
	}
	for _, v := range []string{"0", "-5", "0.5", `"9"`, "null"} {
		t.Run(v, func(t *testing.T) {
			if _, err := loadChatDecl(t, fmt.Sprintf(`{"chat_timeout_sec": %s}`, v)); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// AC-131: chat_approvable_operations が無いとき、反映（release）だけ。空の一覧とは別。
func TestChatDeclaration_ApprovableOperations(t *testing.T) {
	t.Parallel()
	decl, err := loadChatDecl(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []OperationKind{OperationKindRelease}; !reflect.DeepEqual(decl.ChatApprovableOperations, want) {
		t.Errorf("default = %v, want %v", decl.ChatApprovableOperations, want)
	}
	if !decl.ChatCanApprove(OperationKindRelease) || decl.ChatCanApprove(OperationKindExternalSend) {
		t.Errorf("ChatCanApprove on the default: release=%v external_send=%v", decl.ChatCanApprove(OperationKindRelease), decl.ChatCanApprove(OperationKindExternalSend))
	}

	decl, err = loadChatDecl(t, `{"chat_approvable_operations": []}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(decl.ChatApprovableOperations) != 0 || decl.ChatApprovableOperations == nil || decl.ChatCanApprove(OperationKindRelease) {
		t.Errorf("empty list = %#v, want a non-nil empty list that approves nothing", decl.ChatApprovableOperations)
	}

	decl, err = loadChatDecl(t, `{"chat_approvable_operations": ["release", "external_send", "release"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []OperationKind{OperationKindRelease, OperationKindExternalSend}; !reflect.DeepEqual(decl.ChatApprovableOperations, want) {
		t.Errorf("list = %v, want %v (duplicates merged, order kept)", decl.ChatApprovableOperations, want)
	}
	if !decl.ChatCanApprove(OperationKindExternalSend) || decl.ChatCanApprove(OperationKindDelete) {
		t.Errorf("ChatCanApprove: external_send=%v delete=%v", decl.ChatCanApprove(OperationKindExternalSend), decl.ChatCanApprove(OperationKindDelete))
	}
}

// AC-140: 決まった種類でない値は読み込みが誤り。
func TestChatDeclaration_ApprovableOperationsInvalid(t *testing.T) {
	t.Parallel()
	for _, v := range []string{`["deploy"]`, `["release", "x"]`, `[""]`, `[" release"]`, `[1]`, `[null]`, `"release"`, `null`, `{}`} {
		t.Run(v, func(t *testing.T) {
			if _, err := loadChatDecl(t, fmt.Sprintf(`{"chat_approvable_operations": %s}`, v)); !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("error = %v, want ErrConfigInvalid", err)
			}
		})
	}
}

func TestChatDeclaration_KeysAreListedForTheForbiddenTerms(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, k := range AgentDeclarationKeys() {
		have[k] = true
	}
	for _, k := range []string{"chat_tools", "chat_max_turns", "chat_timeout_sec", "chat_approvable_operations"} {
		if !have[k] {
			t.Errorf("AgentDeclarationKeys lacks %q", k)
		}
	}
}

// 既定の一覧は呼び出しごとに別の値で、1 つの宣言の書き換えが他へ及ばない。
func TestChatDeclaration_DefaultListIsNotShared(t *testing.T) {
	t.Parallel()
	a, _ := loadChatDecl(t, `{}`)
	a.ChatApprovableOperations[0] = OperationKindDelete
	b, _ := loadChatDecl(t, `{}`)
	if b.ChatApprovableOperations[0] != OperationKindRelease {
		t.Errorf("default list was mutated through another declaration: %v", b.ChatApprovableOperations)
	}
}
