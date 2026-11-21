package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// #964: 画面へ流す値の一覧（…Values）が、入力の検査・J2 の出力の型と同じ変数（正本）から出ることを固定する。
func TestClosedValues_SingleSource_964(t *testing.T) {
	t.Run("requested_action.kind: 一覧の全部が検査を通り、一覧に無い値は通らない", func(t *testing.T) {
		if got, want := ContractRequestedKindValues(), []string{"answer", "approve", "observe", "review"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ContractRequestedKindValues = %v, want %v", got, want)
		}
		for _, k := range ContractRequestedKindValues() {
			st := &ContractStatus{ContractVersion: contractVersion, RunID: "r", State: ContractStateWaiting, RequestedAction: &ContractRequestedAction{Kind: k, Decider: "human"}}
			if msg := validateContractStatus(st, ""); msg != "" {
				t.Errorf("kind %q は検査を通るはず: %s", k, msg)
			}
		}
		st := &ContractStatus{ContractVersion: contractVersion, RunID: "r", State: ContractStateWaiting, RequestedAction: &ContractRequestedAction{Kind: "bogus", Decider: "human"}}
		if msg := validateContractStatus(st, ""); msg == "" {
			t.Error("一覧に無い kind は検査で落ちるはず")
		}
	})
	t.Run("予想の例の種別: 一覧の全部が検査を通り、一覧に無い値は通らず、J2 の出力の型の enum と一致する", func(t *testing.T) {
		if got, want := ExpectedKindValues(), []ExpectedKind{ExpectedKindUI, ExpectedKindCLI, ExpectedKindBehavior}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ExpectedKindValues = %v, want %v", got, want)
		}
		var enum []string
		for _, k := range ExpectedKindValues() {
			enum = append(enum, string(k))
			if _, err := NormalizeExpected([]StageExpected{{Kind: k, Scene: "s", Before: "b", After: "a"}}); err != nil {
				t.Errorf("kind %q は検査を通るはず: %v", k, err)
			}
		}
		if _, err := NormalizeExpected([]StageExpected{{Kind: "bogus", Scene: "s", Before: "b", After: "a"}}); err == nil {
			t.Error("一覧に無い kind は検査で落ちるはず")
		}
		var schema map[string]any
		if err := json.Unmarshal(j2OutputSchema(), &schema); err != nil {
			t.Fatal(err)
		}
		if got := findEnumForKind(schema); !reflect.DeepEqual(got, enum) {
			t.Errorf("J2 の出力の型の expected.kind の enum = %v, want %v", got, enum)
		}
	})
	t.Run("コンフリクトの解消の宛先: 一覧は統合先・反映先の定数と同じ", func(t *testing.T) {
		if got, want := ConflictFixTargetValues(), []string{ConflictFixTargetIntegration, ConflictFixTargetRelease}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ConflictFixTargetValues = %v, want %v", got, want)
		}
	})
	t.Run("返す一覧は写し（書き換えても正本は変わらない）", func(t *testing.T) {
		ContractRequestedKindValues()[0] = "x"
		ConflictFixTargetValues()[0] = "x"
		ExpectedKindValues()[0] = "x"
		if contractRequestedKindValues[0] != "answer" || conflictFixTargetValues[0] != "integration" || expectedKindValues[0] != "ui" {
			t.Error("正本が書き換わった")
		}
	})
}

// findEnumForKind は J2 の出力の型の中から、properties.kind.enum（予想の例の種別）を探して返す。
func findEnumForKind(v any) []string {
	switch x := v.(type) {
	case map[string]any:
		if props, ok := x["properties"].(map[string]any); ok {
			if k, ok := props["kind"].(map[string]any); ok {
				if en, ok := k["enum"].([]any); ok {
					var out []string
					for _, e := range en {
						out = append(out, e.(string))
					}
					return out
				}
			}
		}
		for _, c := range x {
			if r := findEnumForKind(c); r != nil {
				return r
			}
		}
	case []any:
		for _, c := range x {
			if r := findEnumForKind(c); r != nil {
				return r
			}
		}
	}
	return nil
}
