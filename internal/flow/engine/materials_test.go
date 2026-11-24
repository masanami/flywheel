package engine

import (
	"os"
	"testing"
)

// 同梱の資材（元の runtime/workflows の scripts・schemas と plugin/scripts）を読む試験の前提。
// 元のリポジトリでは試験の位置から ../../workflows と ../../../plugin/scripts にあったが、
// flywheel には資材がまだ無い（資材の埋め込みと展開は後続のタスク）。資材が無い間は
// 読めない理由を付けて飛ばし、資材を置いたこのパッケージの試験で外す。
func needBundledMaterials(t *testing.T) {
	t.Helper()
	// 資材を internal/flow/assets へ置いたら、この飛ばしと呼び出しを外して試験の資材の位置を直す。
	// 置いた後も黙って飛ばし続けないよう、ここで落とす。
	if _, err := os.Stat("../assets"); err == nil {
		t.Fatal("internal/flow/assets exists: remove needBundledMaterials and point these tests at the embedded materials")
	}
	for _, p := range []string{"../../workflows/scripts", "../../workflows/schemas", "../../../plugin/scripts"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("bundled material %s is not in this repository yet (arrives with the embedding task): %v", p, err)
		}
	}
}
