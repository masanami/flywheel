package engine

import (
	"strings"
	"testing"
)

func markerValues(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, DelegatedRunEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// AC-49・AC-51: claude の子の環境には DelegatedRun の値が 1 つだけ入り（親の環境の目印は外す）、
// claude でない子（つなぎのスクリプト）の環境には目印が無い。
func TestChildEnvsCarryTheMarkerOnlyForClaude(t *testing.T) {
	t.Setenv(DelegatedRunEnv, "R-parent")
	e := &Engine{DelegatedRun: "R-1"}
	if got := markerValues(e.claudeEnv()); len(got) != 1 || got[0] != DelegatedRunEnv+"=R-1" {
		t.Errorf("claudeEnv marker = %v, want exactly R-1", got)
	}
	if got := markerValues(e.childEnv()); len(got) != 0 {
		t.Errorf("childEnv (glue scripts) has the marker: %v", got)
	}
	if got := markerValues((&Engine{}).claudeEnv()); len(got) != 0 {
		t.Errorf("claudeEnv without DelegatedRun has the marker: %v", got)
	}
}
