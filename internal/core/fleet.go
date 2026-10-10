package core

// このファイルは、server が束ねるワークスペースの集合（fleet）の宣言
// （fleet.json。docs/features/m4-ui-server.md §IF / API「fleet の宣言ファイル」）
// の読み込みと検証、既定の置き場所、宣言を使わないときの名前の導き方を持つ。
// 宣言はワークスペースの外（利用者の設定）にあり、ストアには触れない。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FleetWorkspace は fleet の 1 つのワークスペース。Path は絶対パス。
type FleetWorkspace struct {
	Name string
	Path string
}

var (
	fleetTopLevelKeys = map[string]bool{"version": true, "workspaces": true}
	fleetEntryKeys    = map[string]bool{"name": true, "path": true}
	fleetNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// DefaultFleetPath は既定の fleet.json のパス（os.UserConfigDir() の
// flywheel/fleet.json）を返す。利用者設定ディレクトリを決められなければエラー。
func DefaultFleetPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "flywheel", "fleet.json"), nil
}

// LoadFleetDeclaration は path の fleet.json を読み、宣言の順のワークスペースを
// 返す。ファイルが無ければ ErrConfigNotFound。JSON として解釈できない・未知の
// キー・型の違い・version が 1 でない・名前かパスの重複・相対パス・名前の規則
// （[a-z0-9][a-z0-9-]*）違反は、いずれも ErrConfigInvalid（fail-closed。
// 一部だけを返さない）。
func LoadFleetDeclaration(path string) ([]FleetWorkspace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("%w: read %s: %w", ErrConfigInvalid, path, err)
	}
	ws, err := parseFleetDeclaration(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrConfigInvalid, path, err)
	}
	return ws, nil
}

func parseFleetDeclaration(data []byte) ([]FleetWorkspace, error) {
	raw, err := decodeStrictJSONObject(data)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownKeys(raw, fleetTopLevelKeys, "top-level"); err != nil {
		return nil, err
	}
	rawVersion, ok := raw["version"]
	if !ok || isJSONNull(rawVersion) {
		return nil, fmt.Errorf(`missing required key "version"`)
	}
	var version int
	if err := json.Unmarshal(rawVersion, &version); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if version != 1 {
		return nil, fmt.Errorf("version must be 1, got %d", version)
	}
	rawList, ok := raw["workspaces"]
	if !ok || isJSONNull(rawList) {
		return nil, fmt.Errorf(`missing required key "workspaces"`)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(rawList, &entries); err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}

	out := make([]FleetWorkspace, 0, len(entries))
	names := map[string]bool{}
	paths := map[string]bool{}
	for i, e := range entries {
		if e == nil {
			return nil, fmt.Errorf("workspaces[%d]: must be an object", i)
		}
		if err := rejectUnknownKeys(e, fleetEntryKeys, fmt.Sprintf("workspaces[%d]", i)); err != nil {
			return nil, err
		}
		var ws FleetWorkspace
		for key, dst := range map[string]*string{"name": &ws.Name, "path": &ws.Path} {
			v, ok := e[key]
			if !ok || isJSONNull(v) {
				return nil, fmt.Errorf("workspaces[%d]: missing required key %q", i, key)
			}
			if err := json.Unmarshal(v, dst); err != nil {
				return nil, fmt.Errorf("workspaces[%d].%s: %w", i, key, err)
			}
		}
		if !fleetNamePattern.MatchString(ws.Name) {
			return nil, fmt.Errorf("workspaces[%d]: name %q must match [a-z0-9][a-z0-9-]*", i, ws.Name)
		}
		if !filepath.IsAbs(ws.Path) {
			return nil, fmt.Errorf("workspaces[%d]: path %q must be an absolute path", i, ws.Path)
		}
		ws.Path = filepath.Clean(ws.Path)
		if names[ws.Name] {
			return nil, fmt.Errorf("workspaces[%d]: duplicate name %q", i, ws.Name)
		}
		if paths[ws.Path] {
			return nil, fmt.Errorf("workspaces[%d]: duplicate path %q", i, ws.Path)
		}
		names[ws.Name], paths[ws.Path] = true, true
		out = append(out, ws)
	}
	return out, nil
}

// FleetNameFromPath は、fleet の宣言を使わずに束ねる 1 つのワークスペースの
// 名前を path から導く。filepath.Base（symlink は解決しない）を小文字にし、
// コードポイントごとに [a-z0-9-] 以外を - 1 つに置き換え（連続はまとめない）、
// 先頭に続く - をすべて除く。空になれば "workspace"。
func FleetNameFromPath(path string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(path)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.TrimLeft(b.String(), "-")
	if name == "" {
		return "workspace"
	}
	return name
}
