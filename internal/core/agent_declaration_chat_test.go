package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// docs/features/core-redesign-s4d.md S4d-1「宣言」（AC-63・64・98・103・104・131・140）。

func loadChatDecl(t *testing.T, json string) (*AgentDeclaration, error) {
	t.Helper()
	return LoadAgentDeclaration(writeAgentJSON(t, json))
}

// chat_tools・chat_max_turns・chat_timeout_sec は無くした項目で、書いた宣言は読み込みが誤り（値は問わない。
// 黙って無視しない）。
func TestChatDeclaration_RemovedKeysAreInvalid(t *testing.T) {
	t.Parallel()
	for key, values := range map[string][]string{
		"chat_tools":       {`"restricted"`, `"full"`, `null`},
		"chat_max_turns":   {`30`, `null`},
		"chat_timeout_sec": {`600`, `null`},
	} {
		for _, v := range values {
			_, err := loadChatDecl(t, fmt.Sprintf(`{%q: %s}`, key, v))
			if !errors.Is(err, ErrConfigInvalid) || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "removed") {
				t.Errorf("%s: %s: error = %v, want ErrConfigInvalid naming the key and \"removed\"", key, v, err)
			}
		}
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
	for _, k := range []string{"chat_approvable_operations"} {
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
