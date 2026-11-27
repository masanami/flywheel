package cli

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// AC-5・AC-6: スーパーバイザーの出力スキーマを実際の検証器へ掛ける。answers の null の要素は通し、basis の無いオブジェクトは通さない。
// 検証器（internal/flow/workflow）と core の両方を import できるのが cli なので、ここに置く。
func TestSVOutputSchema_AC5_AC6_NullAnswerPassesAndMissingBasisFails(t *testing.T) {
	t.Parallel()
	schema, err := workflow.ParseOutputSchema("/sv-output.json", core.SVOutputSchema())
	if err != nil {
		t.Fatalf("ParseOutputSchema: %v", err)
	}
	for name, tc := range map[string]struct {
		answers string
		valid   bool
	}{
		"AC-5 オブジェクトと null":  {`[{"answer":"A","basis":[{"path":"p","locator":"l"}]},null]`, true},
		"AC-6 basis の無い要素":   {`[{"answer":"A"}]`, false},
		"answer の無い要素":       {`[{"basis":[]}]`, false},
		"オブジェクトでも null でもない": {`[""]`, false},
		"answers そのものが null": {`null`, false},
	} {
		var instance any
		raw := `{"hand":"answer","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":` + tc.answers + `}`
		if err := json.Unmarshal([]byte(raw), &instance); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := schema.Validate(instance); (err == nil) != tc.valid {
			t.Errorf("%s: Validate = %v, want valid=%v", name, err, tc.valid)
		}
	}
}
