package server

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// representativeBinaryTest は、実際にビルドした flywheel のバイナリを子プロセスとして起動する
// テストのうち軽いもの。目印の検査では、これを目印付きで走らせて通ることを確かめる。
const representativeBinaryTest = "TestStatusAPI_WithoutGHIs200AndEqualsCLI"

// 委譲の子（flywheel 自身のリポジトリで make check を回す子）の環境には目印
// （core.DelegatedRunEnvVar）がある。TestMain が外さないと、テストが起動する実バイナリが
// os.Environ() を引き継いで M3H13 の拒否（verification_rejected）で全滅する。通常の環境には
// 目印が無く、TestMain の Unsetenv を消しても通ってしまうため、目印を付けた子プロセスとして
// 自分自身のテストバイナリを起動し、実バイナリを起動するテストが通ることで確かめる
// （値が空文字でも設定ありとなる）。子は代表のテストだけを走らせるので、自分自身を再帰起動しない。
func TestMain_UnsetsTheDelegatedMarkerForRealBinaries(t *testing.T) {
	for _, value := range []string{"R-7", ""} {
		t.Run("value="+value, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^"+representativeBinaryTest+"$", "-test.v")
			cmd.Env = append(os.Environ(), core.DelegatedRunEnvVar+"="+value)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child with the marker %q failed: %v\n%s", value, err, out)
			}
			if !strings.Contains(string(out), "--- PASS: "+representativeBinaryTest) {
				t.Errorf("child output lacks the PASS line of %s:\n%s", representativeBinaryTest, out)
			}
		})
	}
}
