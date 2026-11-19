package view_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// 生成した型に、グラフの応答の型（GraphResponse）は無い（AC-76・#813）。
func TestGeneratedTypesHaveNoGraphResponse_AC76_Issue813(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api/types.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GraphResponse", "GraphNode", "GraphEdge", "GRAPH_CATEGORY_CODES", "GRAPH_EDGE_KIND_CODES", "GRAPH_REGISTERED_VIA_CODES"} {
		if strings.Contains(string(src), name) {
			t.Errorf("web/src/api/types.gen.ts still mentions %s", name)
		}
	}
}

// 照合用の見本（web/src/testing/relation-api.golden.json）は無い（AC-418・#813）。
func TestRelationAPIGoldenIsGone_AC418_Issue813(t *testing.T) {
	_, err := os.Stat("../../web/src/testing/relation-api.golden.json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("relation-api.golden.json should not exist (stat err = %v)", err)
	}
}
