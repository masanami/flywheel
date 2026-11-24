package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 対応する claude-harness プラグイン（<root>/.claude-plugin/plugin.json の version）の範囲 [PluginMin, PluginMaxExclusive)。
// 範囲は、フローの資材を写した元（claude-harness の runtime）が対応していた値のまま。子の claude -p が使う agents/ は
// インストール済みのプラグインから解決されるため、範囲外のプラグインでは動き続けない。
const (
	PluginMin          = "4.9.0"
	PluginMaxExclusive = "6.0.0"
)

// LauncherName はプラグインの場所を教えてくれる実行ファイル（PATH 上に置く）。
const LauncherName = "claude-harness-run"

// PluginRange は範囲の表記（標準エラーに出す）。
func PluginRange() string {
	return fmt.Sprintf("%s 以上 %s 未満", PluginMin, PluginMaxExclusive)
}

// ErrLauncherNotFound は PATH に claude-harness-run が無いこと。
var ErrLauncherNotFound = errors.New(LauncherName + " が PATH に見つからない")

// Mismatch はプラグインの版が対応する範囲の外であること。
type Mismatch struct{ Plugin string }

func (m *Mismatch) Error() string {
	return fmt.Sprintf("claude-harness プラグインの版 %s は、対応する範囲（%s）の外にある", m.Plugin, PluginRange())
}

// Plugin は解決したプラグイン。
type Plugin struct {
	Root       string
	ScriptsDir string
	AgentsDir  string
	Version    string
}

// ResolvePlugin は claude-harness-run --plugin-root でプラグインの場所を解決し、plugin.json の版が対応する範囲に
// あるかを確かめる。PATH は getenv("PATH") を読む（lookPath が非 nil ならそれで実行ファイルを探す）。
func ResolvePlugin(getenv func(string) string, lookPath func(string) (string, error)) (Plugin, error) {
	var bin string
	var err error
	if lookPath != nil {
		bin, err = lookPath(LauncherName)
	} else {
		bin, err = findInPath(LauncherName, getenv("PATH"))
	}
	if err != nil || bin == "" {
		return Plugin{}, ErrLauncherNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--plugin-root").Output()
	if err != nil {
		return Plugin{}, fmt.Errorf("%s --plugin-root が失敗した: %w", LauncherName, err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return Plugin{}, fmt.Errorf("%s --plugin-root がプラグインの場所を返さなかった", LauncherName)
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return Plugin{}, fmt.Errorf("%s が示すプラグイン（%s）の plugin.json を読めない: %w", LauncherName, root, err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Plugin{}, fmt.Errorf("%s が示すプラグイン（%s）の plugin.json を読めない: %w", LauncherName, root, err)
	}
	if err := CheckPlugin(manifest.Version); err != nil {
		return Plugin{}, err
	}
	return Plugin{Root: root, ScriptsDir: filepath.Join(root, "scripts"), AgentsDir: filepath.Join(root, "agents"), Version: manifest.Version}, nil
}

func findInPath(name, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue // カレントディレクトリや相対の置き場の実行ファイルは使わない
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", ErrLauncherNotFound
}

// CheckPlugin はプラグインの版が範囲内かを確かめる。範囲外なら *Mismatch、版として読めなければその誤りを返す。
func CheckPlugin(plugin string) error {
	p, err := parseSemver(plugin)
	if err != nil {
		return fmt.Errorf("claude-harness プラグインの版 %q は semver（X.Y.Z）として読めない（対応する範囲: %s）", plugin, PluginRange())
	}
	lo, _ := parseSemver(PluginMin)
	hi, _ := parseSemver(PluginMaxExclusive)
	if compareSemver(p, lo) < 0 || compareSemver(p, hi) >= 0 {
		return &Mismatch{Plugin: plugin}
	}
	return nil
}

// semver は X.Y.Z[-pre][+build]（build は比較に使わない）。
type semver struct {
	n   [3]int
	pre string
}

func parseSemver(s string) (semver, error) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	core, pre, _ := strings.Cut(s, "-")
	v.pre = pre
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, errors.New("not X.Y.Z")
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, errors.New("not X.Y.Z")
		}
		v.n[i] = n
	}
	return v, nil
}

// compareSemver は a と b を比べる。pre-release は同じ X.Y.Z の正式版より前（pre-release どうしは文字列で比べる）。
func compareSemver(a, b semver) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] < b.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	case a.pre < b.pre:
		return -1
	}
	return 1
}
