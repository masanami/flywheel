package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// --- .flywheel/connectors.json（宣言）§IF / API ---
//
// docs/features/m3-invoker-delegation.md §IF / API「.flywheel/connectors.json」
// に定める形。読み込みと検証（LoadConnectorsDeclaration）は本チケット（#79）の
// 範囲。`cli` 形態の委譲そのもの（S4）・S2 以降のキー（bundle・slot_owner・
// parallel_safe 等）は範囲外（S1 では未知キーとして拒否する。詳細は返却の
// 「置いた仮定」）。

// connectorsFileName はワークスペース直下の宣言ファイル名。
const connectorsFileName = "connectors.json"

// connectorsFilePath は base の下の宣言ファイルの絶対パスを返す。
func connectorsFilePath(base string) string {
	return filepath.Join(base, flywheelDirName, connectorsFileName)
}

// ConnectorForm は接続ツールの形態の閉集合。
type ConnectorForm string

// ConnectorForm の4値。
const (
	ConnectorFormPlugin ConnectorForm = "plugin"
	ConnectorFormBrief  ConnectorForm = "brief"
	ConnectorFormCLI    ConnectorForm = "cli"
	ConnectorFormFlow   ConnectorForm = "flow"
)

var connectorFormValues = []ConnectorForm{ConnectorFormPlugin, ConnectorFormBrief, ConnectorFormCLI, ConnectorFormFlow}

func isKnownConnectorForm(s string) bool {
	for _, v := range connectorFormValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PermissionMode は接続ツールの権限モードの閉集合
// （docs/features/m3-invoker-delegation.md §クリティカル設計決定 2。
// `bypassPermissions` を含む閉集合外の値は拒否する【決定 2026-09-28 オーナー
// M3H4】）。
type PermissionMode string

// PermissionMode の3値。
const (
	PermissionModeDefault     PermissionMode = "default"
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	PermissionModeAuto        PermissionMode = "auto"
)

var permissionModeValues = []PermissionMode{PermissionModeDefault, PermissionModeAcceptEdits, PermissionModeAuto}

func isKnownPermissionMode(s string) bool {
	for _, v := range permissionModeValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// Counterpart は操作の対話相手の閉集合。
type Counterpart string

// Counterpart の2値。
const (
	CounterpartHuman  Counterpart = "human"
	CounterpartParent Counterpart = "parent"
)

var counterpartValues = []Counterpart{CounterpartHuman, CounterpartParent}

func isKnownCounterpart(s string) bool {
	for _, v := range counterpartValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// connectorArtifactsValues は操作の成果物の種類の閉集合
// （docs/features/m3-invoker-delegation.md §機能全体の設計「成果物の種類
// (pr|branch|none)」）。値が明示されている場合だけ検査する（artifacts の
// 検証はこのチケットの受入基準に明示は無いが、仕様の閉集合に沿って検証する
// 仮定。返却の「置いた仮定」に明記する）。
var connectorArtifactsValues = map[string]bool{"pr": true, "branch": true, "none": true}

// invocationPlaceholderPattern は invocation 中の `{...}` の差し込みを抜き出す。
var invocationPlaceholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

// allowedInvocationPlaceholders は invocation に書ける差し込みの閉集合
// （docs/features/m3-invoker-delegation.md §宣言）。
var allowedInvocationPlaceholders = map[string]bool{
	"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true,
}

func validateInvocationPlaceholders(invocation string) error {
	for _, m := range invocationPlaceholderPattern.FindAllString(invocation, -1) {
		if !allowedInvocationPlaceholders[m] {
			return fmt.Errorf("%w: invocation %q has a disallowed placeholder %q", ErrConfigInvalid, invocation, m)
		}
	}
	return nil
}

// HumanQuestionKind は human_question_kinds の1要素。
type HumanQuestionKind struct {
	ID    string
	Label string
}

// CLICommands は `form: cli` の接続ツールの commands（start・status・resume・
// cancel それぞれの引数の配列）。キーが省略された場合、対応するフィールドは
// nil のまま（空配列 `[]` の明示との違いを保つ。validate はこの nil/非nil を
// 「無い」の判定に使う）。
type CLICommands struct {
	Start  []string
	Status []string
	Resume []string
	Cancel []string
	// Reflect は反映の依頼を始めるコマンド。任意（nil は「無い」。空配列は読み込みが拒否する）。
	// 状態の確認・答え・取り消しは Status・Resume・Cancel を共有する。
	Reflect []string
}

// ConnectorOperation は接続ツールの操作の1要素。
type ConnectorOperation struct {
	ID             string
	Invocation     string // "" は「無し」（brief の操作、または省略）
	Interactive    bool
	Counterpart    Counterpart
	ChildMayDecide bool
	Artifacts      string // "" は省略
	// Flow は form: flow の委譲先の操作が動かすフローの名前（flow の形だけが持つ。"" は省略）。
	Flow string
}

// Connector は接続ツールの1要素。
type Connector struct {
	ID              string
	Form            ConnectorForm
	PermissionMode  PermissionMode
	Operations      []ConnectorOperation
	Commands        *CLICommands // form: cli 以外は通常 nil
	ContractVersion *int         // form: cli 以外は通常 nil
	// RequiredFacts は form: cli の委譲先の任意のキー。レビュー待ちを受け入れる事実の名前から、
	// 受け入れる値の配列への対応（nil は「無い」）。form: cli 以外では書けない。
	RequiredFacts map[string][]string
	// ChallengeIDPrefix は form: flow の委譲先の任意のキー（"" は省略）。flow の形以外では書けない。
	ChallengeIDPrefix string
	// Models は form: flow の委譲先の任意のキー。フローの名前から、変数の名前→モデルの値への対応（nil は「無い」）。
	// flow の形以外では書けない。変数がフローに定義されているかは読み込みでは確かめない。
	Models map[string]map[string]string
}

// UsesContractCommands は c が契約のコマンドを使って起動・確認・再開・取り消しをする委譲先（form: cli か form: flow）か。
// 「契約の形か」で分かれる箇所はこの関数を通す。form: flow の commands は読み込んだ後も空で、実行ファイルのパスは起動の時に決まる。
func (c *Connector) UsesContractCommands() bool {
	return c != nil && (c.Form == ConnectorFormCLI || c.Form == ConnectorFormFlow)
}

// flowOperationNames は form: flow の操作の flow に書けるフロー名（S1 は ticket だけ）。
var flowOperationNames = map[string]bool{"ticket": true}

// flowModelsNames は form: flow の models のキーに書けるフロー名。
var flowModelsNames = map[string]bool{"ticket": true, "reflect": true}

// ConnectorRepoSlots は repos[].slots。Paths は provider が clone のとき、
// Base・Count は worktree のときだけ使う（合わないキーは未知のキーとして拒否）。
type ConnectorRepoSlots struct {
	Provider string
	Paths    []string
	Base     string
	Count    int
}

// ConnectorRepo は repos の1要素。
type ConnectorRepo struct {
	Name          string
	Remote        string
	DefaultBranch string
	Connector     string
	Slots         ConnectorRepoSlots
	// ReflectConnector は反映の依頼の口にする委譲先の ID（任意。"" は「無い」）。
	// 指せるのは、form: cli で commands.reflect を持つ委譲先だけ。
	ReflectConnector string
}

// ConnectorsDeclaration は .flywheel/connectors.json の中身
// （LoadConnectorsDeclaration の戻り値）。
type ConnectorsDeclaration struct {
	Version            int
	HumanQuestionKinds []HumanQuestionKind
	Connectors         []Connector
	Repos              []ConnectorRepo

	// DefaultsUsed は、いずれかの操作で interactive・counterpart・
	// child_may_decide が省略され既定値を適用したことを表す
	// （§宣言「interactive の省略は true、counterpart の省略は parent、
	// child_may_decide の省略は false として読む」）。
	DefaultsUsed bool
}

// LoadConnectorsDeclaration は base の下の .flywheel/connectors.json を読み、
// 検証する。ファイルが無ければ ErrConfigNotFound（`plan --auto` はこれで
// config_not_found になる。§宣言）。JSON として解釈できない・未知のキー
// （最上位・接続ツール・操作・リポジトリの各要素）・閉集合外の値・重複・
// 宣言に無い接続ツールを指すリポジトリはすべて ErrConfigInvalid
// （fail-closed。何も起動・変更しない）。
func LoadConnectorsDeclaration(base string) (*ConnectorsDeclaration, error) {
	path := connectorsFilePath(base)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// self-review 指摘: ErrConfigNotFound をそのまま返すと、CLI の
			// 表示（err.Error()）が「core: declaration file not found」に
			// 固定され、どのファイル（connectors.json）が無いのか伝わらない。
			// パスを付けてラップする（errors.Is での判定には影響しない。
			// errors.go のコメントに同じ注記がある）。
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("%w: read %s: %w", ErrConfigInvalid, path, err)
	}

	decl, err := parseConnectorsDeclaration(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s: %w", ErrConfigInvalid, path, err)
	}
	if err := decl.validate(); err != nil {
		return nil, err
	}
	return decl, nil
}

var connectorsTopLevelKeys = map[string]bool{
	"version": true, "human_question_kinds": true, "connectors": true, "repos": true,
}
var humanQuestionKindKeys = map[string]bool{"id": true, "label": true}
var connectorKeys = map[string]bool{
	"id": true, "form": true, "permission_mode": true, "operations": true,
	"commands": true, "contract_version": true, "required_facts": true,
	"challenge_id_prefix": true, "models": true,
}
var operationKeys = map[string]bool{
	"id": true, "invocation": true, "interactive": true, "counterpart": true,
	"child_may_decide": true, "artifacts": true, "flow": true,
}
var cliCommandsKeys = map[string]bool{"start": true, "status": true, "resume": true, "cancel": true, "reflect": true}
var connectorRepoKeys = map[string]bool{
	"name": true, "remote": true, "default_branch": true, "connector": true, "slots": true,
	"reflect_connector": true,
}
var connectorRepoSlotsKeys = map[string]bool{"provider": true, "paths": true, "base": true, "count": true}

// slotsKeysByProvider は provider ごとに使えるキー。合わないキーは未知のキー扱い。
var slotsKeysByProvider = map[string]map[string]bool{
	"clone":    {"provider": true, "paths": true},
	"worktree": {"provider": true, "base": true, "count": true},
}

func parseConnectorsDeclaration(data []byte) (*ConnectorsDeclaration, error) {
	raw, err := decodeStrictJSONObject(data)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownKeys(raw, connectorsTopLevelKeys, "top-level"); err != nil {
		return nil, err
	}

	decl := &ConnectorsDeclaration{}

	// version は省略時 0（sources.go の version と同じ規則。「1 でなければ
	// 拒否」に沿って、省略も拒否する。仮定として返却に明記する）。
	if v, ok := raw["version"]; ok {
		i, err := decodeIntLiteral(v)
		if err != nil {
			return nil, fmt.Errorf("version: %w", err)
		}
		decl.Version = i
	}

	if v, ok := raw["human_question_kinds"]; ok {
		// self-review 指摘（round 2）: "connectors"・"repos" と同じ理由で、
		// キーが存在しても値が null なら型違いとして拒否する（省略と同じ
		// 「0件」に静かにフォールバックさせない）。
		if isJSONNull(v) {
			return nil, fmt.Errorf("human_question_kinds: must not be null")
		}
		kinds, err := parseHumanQuestionKinds(v)
		if err != nil {
			return nil, fmt.Errorf("human_question_kinds: %w", err)
		}
		decl.HumanQuestionKinds = kinds
	}

	// "connectors"・"repos" は必須のキー。省略・null はいずれも「宣言に
	// 不備がある」として fail-closed に拒否する（sources.go の "sources" と
	// 同じ規則。self-review 指摘: null を見ずに ok だけで判定すると、
	// json.Unmarshal(null, &[]...) がエラーにならないため "0件" として
	// 静かに受理してしまう）。明示的な空配列 `[]` は「0件」として引き続き
	// 許容する。
	connsRaw, ok := raw["connectors"]
	if !ok || isJSONNull(connsRaw) {
		return nil, fmt.Errorf(`missing required key "connectors"`)
	}
	conns, defaultsUsed, err := parseConnectors(connsRaw)
	if err != nil {
		return nil, fmt.Errorf("connectors: %w", err)
	}
	decl.Connectors = conns
	decl.DefaultsUsed = defaultsUsed

	reposRaw, ok := raw["repos"]
	if !ok || isJSONNull(reposRaw) {
		return nil, fmt.Errorf(`missing required key "repos"`)
	}
	repos, err := parseConnectorRepos(reposRaw)
	if err != nil {
		return nil, fmt.Errorf("repos: %w", err)
	}
	decl.Repos = repos

	return decl, nil
}

func parseHumanQuestionKinds(raw json.RawMessage) ([]HumanQuestionKind, error) {
	var rawList []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawList); err != nil {
		return nil, err
	}
	kinds := make([]HumanQuestionKind, 0, len(rawList))
	for i, rk := range rawList {
		if err := rejectUnknownKeys(rk, humanQuestionKindKeys, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, err
		}
		var hk HumanQuestionKind
		if v, ok := rk["id"]; ok {
			if err := json.Unmarshal(v, &hk.ID); err != nil {
				return nil, fmt.Errorf("[%d].id: %w", i, err)
			}
		}
		if v, ok := rk["label"]; ok {
			if err := json.Unmarshal(v, &hk.Label); err != nil {
				return nil, fmt.Errorf("[%d].label: %w", i, err)
			}
		}
		kinds = append(kinds, hk)
	}
	return kinds, nil
}

func parseConnectors(raw json.RawMessage) ([]Connector, bool, error) {
	var rawList []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawList); err != nil {
		return nil, false, err
	}
	conns := make([]Connector, 0, len(rawList))
	defaultsUsed := false
	for i, rc := range rawList {
		if err := rejectRemovedKeys(rc, []string{"conflict_prediction"}, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, false, err
		}
		if err := rejectUnknownKeys(rc, connectorKeys, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, false, err
		}
		var c Connector
		if v, ok := rc["id"]; ok {
			if err := json.Unmarshal(v, &c.ID); err != nil {
				return nil, false, fmt.Errorf("[%d].id: %w", i, err)
			}
		}
		if v, ok := rc["form"]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, false, fmt.Errorf("[%d].form: %w", i, err)
			}
			c.Form = ConnectorForm(s)
		}
		if v, ok := rc["permission_mode"]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, false, fmt.Errorf("[%d].permission_mode: %w", i, err)
			}
			c.PermissionMode = PermissionMode(s)
		}
		if v, ok := rc["operations"]; ok {
			ops, used, err := parseOperations(v)
			if err != nil {
				return nil, false, fmt.Errorf("[%d].operations: %w", i, err)
			}
			c.Operations = ops
			if used {
				defaultsUsed = true
			}
		}
		if v, ok := rc["commands"]; ok {
			cmds, err := parseCLICommands(v)
			if err != nil {
				return nil, false, fmt.Errorf("[%d].commands: %w", i, err)
			}
			c.Commands = cmds
		}
		if v, ok := rc["required_facts"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].required_facts: must not be null", i)
			}
			var rawRF map[string][]*string
			if err := json.Unmarshal(v, &rawRF); err != nil {
				return nil, false, fmt.Errorf("[%d].required_facts: %w", i, err)
			}
			rf := make(map[string][]string, len(rawRF))
			for name, vals := range rawRF {
				ss, err := derefStrings(vals)
				if err != nil {
					return nil, false, fmt.Errorf("[%d].required_facts[%q]: %w", i, name, err)
				}
				rf[name] = ss
			}
			c.RequiredFacts = rf
		}
		if v, ok := rc["challenge_id_prefix"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].challenge_id_prefix: must not be null", i)
			}
			if err := json.Unmarshal(v, &c.ChallengeIDPrefix); err != nil {
				return nil, false, fmt.Errorf("[%d].challenge_id_prefix: %w", i, err)
			}
		}
		if v, ok := rc["models"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].models: must not be null", i)
			}
			var m map[string]map[string]string
			if err := json.Unmarshal(v, &m); err != nil {
				return nil, false, fmt.Errorf("[%d].models: %w", i, err)
			}
			c.Models = m
		}
		if v, ok := rc["contract_version"]; ok {
			cv, err := decodeIntLiteral(v)
			if err != nil {
				return nil, false, fmt.Errorf("[%d].contract_version: %w", i, err)
			}
			c.ContractVersion = &cv
		}
		conns = append(conns, c)
	}
	return conns, defaultsUsed, nil
}

func parseOperations(raw json.RawMessage) ([]ConnectorOperation, bool, error) {
	// self-review 指摘（round 2）: "operations": null は Unmarshal がエラーに
	// ならず rawList=nil（0件）になるため、明示的に拒否する（省略との混同を
	// 避ける。parseConnectors 側の "connectors"・"repos" と同じ規則）。
	if isJSONNull(raw) {
		return nil, false, fmt.Errorf("must not be null")
	}
	var rawList []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawList); err != nil {
		return nil, false, err
	}
	ops := make([]ConnectorOperation, 0, len(rawList))
	defaultsUsed := false
	for i, ro := range rawList {
		if err := rejectUnknownKeys(ro, operationKeys, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, false, err
		}
		// §宣言「interactive の省略は true、counterpart の省略は parent、
		// child_may_decide の省略は false として読む」。
		op := ConnectorOperation{Interactive: true, Counterpart: CounterpartParent, ChildMayDecide: false}
		if v, ok := ro["id"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].id: must not be null", i)
			}
			if err := json.Unmarshal(v, &op.ID); err != nil {
				return nil, false, fmt.Errorf("[%d].id: %w", i, err)
			}
		}
		if v, ok := ro["invocation"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].invocation: must not be null", i)
			}
			if err := json.Unmarshal(v, &op.Invocation); err != nil {
				return nil, false, fmt.Errorf("[%d].invocation: %w", i, err)
			}
		}
		if v, ok := ro["interactive"]; ok {
			// self-review 指摘: json.Unmarshal は非ポインタの bool を null で
			// 変えない（初期値のまま）ため、null を拒否せずに通すと defaultsUsed
			// が立たないまま既定値と同じ値が入り、型違いも素通りしてしまう。
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].interactive: must not be null", i)
			}
			if err := json.Unmarshal(v, &op.Interactive); err != nil {
				return nil, false, fmt.Errorf("[%d].interactive: %w", i, err)
			}
		} else {
			defaultsUsed = true
		}
		if v, ok := ro["counterpart"]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, false, fmt.Errorf("[%d].counterpart: %w", i, err)
			}
			op.Counterpart = Counterpart(s)
		} else {
			defaultsUsed = true
		}
		if v, ok := ro["child_may_decide"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].child_may_decide: must not be null", i)
			}
			if err := json.Unmarshal(v, &op.ChildMayDecide); err != nil {
				return nil, false, fmt.Errorf("[%d].child_may_decide: %w", i, err)
			}
		} else {
			defaultsUsed = true
		}
		if v, ok := ro["flow"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].flow: must not be null", i)
			}
			if err := json.Unmarshal(v, &op.Flow); err != nil {
				return nil, false, fmt.Errorf("[%d].flow: %w", i, err)
			}
		}
		if v, ok := ro["artifacts"]; ok {
			if isJSONNull(v) {
				return nil, false, fmt.Errorf("[%d].artifacts: must not be null", i)
			}
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, false, fmt.Errorf("[%d].artifacts: %w", i, err)
			}
			op.Artifacts = s
		}
		ops = append(ops, op)
	}
	return ops, defaultsUsed, nil
}

func parseCLICommands(raw json.RawMessage) (*CLICommands, error) {
	if isJSONNull(raw) {
		return nil, fmt.Errorf("must not be null")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if err := rejectUnknownKeys(m, cliCommandsKeys, "commands"); err != nil {
		return nil, err
	}

	cmds := &CLICommands{}
	fields := []struct {
		key    string
		target *[]string
	}{
		{"start", &cmds.Start}, {"status", &cmds.Status}, {"resume", &cmds.Resume}, {"cancel", &cmds.Cancel},
	}
	if v, ok := m["reflect"]; ok {
		if isJSONNull(v) {
			return nil, fmt.Errorf("reflect: must not be null")
		}
		var rawSS []*string
		if err := json.Unmarshal(v, &rawSS); err != nil {
			return nil, fmt.Errorf("reflect: %w", err)
		}
		ss, err := derefStrings(rawSS)
		if err != nil {
			return nil, fmt.Errorf("reflect: %w", err)
		}
		cmds.Reflect = ss
	}
	for _, f := range fields {
		v, ok := m[f.key]
		if !ok {
			continue
		}
		var ss []string
		if err := json.Unmarshal(v, &ss); err != nil {
			return nil, fmt.Errorf("%s: %w", f.key, err)
		}
		*f.target = ss
	}
	return cmds, nil
}

// derefStrings は JSON の配列を文字列の配列へ直す。null の要素は文字列でないので拒否する
// （encoding/json は null を空文字として読むため、[]string のまま読むと素通りする）。空の配列は非 nil の空で返す。
func derefStrings(in []*string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p == nil {
			return nil, fmt.Errorf("element must be a string, got null")
		}
		out = append(out, *p)
	}
	return out, nil
}

func parseConnectorRepos(raw json.RawMessage) ([]ConnectorRepo, error) {
	var rawList []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawList); err != nil {
		return nil, err
	}
	repos := make([]ConnectorRepo, 0, len(rawList))
	for i, rr := range rawList {
		if err := rejectRemovedKeys(rr, []string{"serial_judgment", "conflict_exclude_paths", "merge_method"}, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, err
		}
		if err := rejectUnknownKeys(rr, connectorRepoKeys, fmt.Sprintf("[%d]", i)); err != nil {
			return nil, err
		}
		var r ConnectorRepo
		if v, ok := rr["name"]; ok {
			if err := json.Unmarshal(v, &r.Name); err != nil {
				return nil, fmt.Errorf("[%d].name: %w", i, err)
			}
		}
		if v, ok := rr["remote"]; ok {
			if err := json.Unmarshal(v, &r.Remote); err != nil {
				return nil, fmt.Errorf("[%d].remote: %w", i, err)
			}
		}
		if v, ok := rr["default_branch"]; ok {
			if err := json.Unmarshal(v, &r.DefaultBranch); err != nil {
				return nil, fmt.Errorf("[%d].default_branch: %w", i, err)
			}
		}
		if v, ok := rr["connector"]; ok {
			if err := json.Unmarshal(v, &r.Connector); err != nil {
				return nil, fmt.Errorf("[%d].connector: %w", i, err)
			}
		}
		if v, ok := rr["reflect_connector"]; ok {
			if isJSONNull(v) {
				return nil, fmt.Errorf("[%d].reflect_connector: must not be null", i)
			}
			if err := json.Unmarshal(v, &r.ReflectConnector); err != nil {
				return nil, fmt.Errorf("[%d].reflect_connector: %w", i, err)
			}
			if r.ReflectConnector == "" {
				return nil, fmt.Errorf("[%d].reflect_connector: must not be empty", i)
			}
		}
		if v, ok := rr["slots"]; ok {
			// self-review 指摘（round 2）: "slots": null は「省略」と区別され
			// ず、Slots がゼロ値のまま静かに通ってしまう。
			if isJSONNull(v) {
				return nil, fmt.Errorf("[%d].slots: must not be null", i)
			}
			slots, err := parseConnectorRepoSlots(v)
			if err != nil {
				return nil, fmt.Errorf("[%d].slots: %w", i, err)
			}
			r.Slots = slots
		}
		repos = append(repos, r)
	}
	return repos, nil
}

func parseConnectorRepoSlots(raw json.RawMessage) (ConnectorRepoSlots, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ConnectorRepoSlots{}, err
	}
	if err := rejectUnknownKeys(m, connectorRepoSlotsKeys, "slots"); err != nil {
		return ConnectorRepoSlots{}, err
	}
	// provider に合わないキーを未知のキーとして拒否する。provider の省略は
	// paths だけの形（S1 から引き継いだ省略可の仮定）に限り、base・count は
	// provider が worktree のときだけ受け付ける。閉集合外の provider は
	// validate が拒否する。
	allowed := slotsKeysByProvider["clone"]
	if pv, ok := m["provider"]; ok {
		var ps string
		if json.Unmarshal(pv, &ps) == nil {
			if a, known := slotsKeysByProvider[ps]; known {
				allowed = a
			} else {
				allowed = connectorRepoSlotsKeys
			}
		}
	}
	if err := rejectUnknownKeys(m, allowed, "slots"); err != nil {
		return ConnectorRepoSlots{}, err
	}
	var s ConnectorRepoSlots
	if v, ok := m["provider"]; ok {
		// self-review 指摘（round 2）: null は string 型への Unmarshal で
		// 変わらず "" のままになり、validateConnectorRepo の
		// 「Provider が空なら閉集合を検査しない」規則（省略を許す仮定）と
		// 衝突して、明示的な null が素通りしてしまう。
		if isJSONNull(v) {
			return ConnectorRepoSlots{}, fmt.Errorf("provider: must not be null")
		}
		if err := json.Unmarshal(v, &s.Provider); err != nil {
			return ConnectorRepoSlots{}, fmt.Errorf("provider: %w", err)
		}
	}
	if v, ok := m["paths"]; ok {
		if err := json.Unmarshal(v, &s.Paths); err != nil {
			return ConnectorRepoSlots{}, fmt.Errorf("paths: %w", err)
		}
		if s.Paths == nil {
			// null は省略と区別できず、clone の必須検査をすり抜けるため拒否する。
			return ConnectorRepoSlots{}, fmt.Errorf("paths: must not be null")
		}
	}
	if v, ok := m["base"]; ok {
		if isJSONNull(v) {
			return ConnectorRepoSlots{}, fmt.Errorf("base: must not be null")
		}
		if err := json.Unmarshal(v, &s.Base); err != nil {
			return ConnectorRepoSlots{}, fmt.Errorf("base: %w", err)
		}
	}
	if v, ok := m["count"]; ok {
		n, err := decodeIntLiteral(v)
		if err != nil {
			return ConnectorRepoSlots{}, fmt.Errorf("count: %w", err)
		}
		s.Count = n
	}
	return s, nil
}

// validate は d が §IF / API・§受入基準「宣言」の規則を満たすかを検査する。
// 最初に見つけた違反を ErrConfigInvalid として返す（fail-closed）。
func (d *ConnectorsDeclaration) validate() error {
	if d.Version != 1 {
		return fmt.Errorf("%w: version must be 1, got %d", ErrConfigInvalid, d.Version)
	}

	seenQuestionIDs := make(map[string]bool, len(d.HumanQuestionKinds))
	for _, hk := range d.HumanQuestionKinds {
		if hk.ID == "" {
			return fmt.Errorf("%w: human_question_kinds has an entry with an empty id", ErrConfigInvalid)
		}
		if seenQuestionIDs[hk.ID] {
			return fmt.Errorf("%w: duplicate human_question_kinds id %q", ErrConfigInvalid, hk.ID)
		}
		seenQuestionIDs[hk.ID] = true
	}

	connectorIDs := make(map[string]bool, len(d.Connectors))
	for _, c := range d.Connectors {
		if err := validateConnector(c, connectorIDs); err != nil {
			return err
		}
	}

	repoNames := make(map[string]bool, len(d.Repos))
	for _, r := range d.Repos {
		if err := validateConnectorRepo(r, repoNames, connectorIDs); err != nil {
			return err
		}
		if err := d.validateReflectConnector(r); err != nil {
			return err
		}
	}

	return nil
}

// slotsProviderValues は repos[].slots.provider の閉集合（clone | worktree。
// container などは拒否する）。slots・slots.provider の省略は許す仮定なので、
// 値が明示されている場合だけ検査する。
var slotsProviderValues = map[string]bool{"clone": true, "worktree": true}

// validateSlots は provider ごとの必須キーを検査する。パスはワークスペース
// からの相対パス。
func validateSlots(r ConnectorRepo) error {
	s := r.Slots
	switch s.Provider {
	case "clone":
		if len(s.Paths) == 0 {
			return fmt.Errorf("%w: repo %q slots.paths is required (non-empty) for provider clone", ErrConfigInvalid, r.Name)
		}
		for _, p := range s.Paths {
			if !filepath.IsLocal(p) {
				return fmt.Errorf("%w: repo %q slots.paths entry %q must be a non-empty relative path inside the workspace", ErrConfigInvalid, r.Name, p)
			}
		}
	case "worktree":
		if !filepath.IsLocal(s.Base) {
			return fmt.Errorf("%w: repo %q slots.base must be a non-empty relative path inside the workspace for provider worktree", ErrConfigInvalid, r.Name)
		}
		if s.Count <= 0 {
			return fmt.Errorf("%w: repo %q slots.count must be a positive integer for provider worktree, got %d", ErrConfigInvalid, r.Name, s.Count)
		}
	}
	return nil
}

// validateConnectorRepo は1つのリポジトリを検査し、その name を repoNames へ
// 登録する（重複していれば拒否）。ConnectorsDeclaration.validate から抜き出し
// （validateConnector・validateOperation と同じ理由）。
func validateConnectorRepo(r ConnectorRepo, repoNames map[string]bool, connectorIDs map[string]bool) error {
	if r.Name == "" {
		return fmt.Errorf("%w: repos has an entry with an empty name", ErrConfigInvalid)
	}
	if repoNames[r.Name] {
		return fmt.Errorf("%w: duplicate repo name %q", ErrConfigInvalid, r.Name)
	}
	repoNames[r.Name] = true

	if !connectorIDs[r.Connector] {
		return fmt.Errorf("%w: repo %q refers to unknown connector %q", ErrConfigInvalid, r.Name, r.Connector)
	}

	// self-review 指摘: remote・default_branch・slots.provider が検証されて
	// いなかった。sources.go の repos は repoPattern（<owner>/<name>）で remote
	// と同じ形の値を検証しており、それと揃える。
	if !repoPattern.MatchString(r.Remote) {
		return fmt.Errorf("%w: repo %q has invalid remote %q (want <owner>/<name>)", ErrConfigInvalid, r.Name, r.Remote)
	}
	if r.DefaultBranch == "" {
		return fmt.Errorf("%w: repo %q has no default_branch", ErrConfigInvalid, r.Name)
	}
	if r.Slots.Provider != "" && !slotsProviderValues[r.Slots.Provider] {
		return fmt.Errorf("%w: repo %q has invalid slots.provider %q", ErrConfigInvalid, r.Name, r.Slots.Provider)
	}
	if err := validateSlots(r); err != nil {
		return err
	}

	return nil
}

// validateConnector は1つの接続ツールを検査し、その id を connectorIDs へ
// 登録する（重複していれば拒否）。ConnectorsDeclaration.validate から抜き出し
// （revive: 関数が長くなり過ぎないようにする）。
func validateConnector(c Connector, connectorIDs map[string]bool) error {
	if c.ID == "" {
		return fmt.Errorf("%w: connectors has an entry with an empty id", ErrConfigInvalid)
	}
	if connectorIDs[c.ID] {
		return fmt.Errorf("%w: duplicate connector id %q", ErrConfigInvalid, c.ID)
	}
	connectorIDs[c.ID] = true

	if !isKnownConnectorForm(string(c.Form)) {
		return fmt.Errorf("%w: connector %q has invalid form %q", ErrConfigInvalid, c.ID, c.Form)
	}
	if !isKnownPermissionMode(string(c.PermissionMode)) {
		return fmt.Errorf("%w: connector %q has invalid permission_mode %q", ErrConfigInvalid, c.ID, c.PermissionMode)
	}

	opIDs := make(map[string]bool, len(c.Operations))
	for _, op := range c.Operations {
		if err := validateOperation(c, op, opIDs); err != nil {
			return err
		}
	}

	if c.Form == ConnectorFormCLI {
		if err := validateCLICommands(c); err != nil {
			return err
		}
	}
	if c.Form == ConnectorFormFlow {
		if err := validateFlowConnector(c); err != nil {
			return err
		}
	} else if err := validateNotFlowKeys(c); err != nil {
		return err
	}
	return validateRequiredFacts(c)
}

// validateFlowConnector は form: flow の委譲先の固有の検査をする。commands・contract_version は書けず（実行ファイルは
// 起動の時に決まる）、models は ticket・reflect のフロー名ごとに、空でない名前と空でない値だけを持つ。
// 操作の flow は validateOperation が検査する。permission_mode の 3 値の検査は全ての形で済んでいる。
func validateFlowConnector(c Connector) error {
	if c.Commands != nil {
		return fmt.Errorf("%w: connector %q (form flow) must not have commands", ErrConfigInvalid, c.ID)
	}
	if c.ContractVersion != nil {
		return fmt.Errorf("%w: connector %q (form flow) must not have contract_version", ErrConfigInvalid, c.ID)
	}
	flows := make([]string, 0, len(c.Models))
	for name := range c.Models {
		flows = append(flows, name)
	}
	sort.Strings(flows)
	for _, flow := range flows {
		if !flowModelsNames[flow] {
			return fmt.Errorf("%w: connector %q models has invalid flow name %q (want ticket or reflect)", ErrConfigInvalid, c.ID, flow)
		}
		vars := make([]string, 0, len(c.Models[flow]))
		for name := range c.Models[flow] {
			vars = append(vars, name)
		}
		sort.Strings(vars)
		for _, name := range vars {
			if name == "" {
				return fmt.Errorf("%w: connector %q models[%q] has an empty variable name", ErrConfigInvalid, c.ID, flow)
			}
			if c.Models[flow][name] == "" {
				return fmt.Errorf("%w: connector %q models[%q][%q] must not be empty", ErrConfigInvalid, c.ID, flow, name)
			}
		}
	}
	return nil
}

// validateNotFlowKeys は form: flow でない委譲先が、flow の形だけのキー（challenge_id_prefix・models）を書いていないか検査する。
func validateNotFlowKeys(c Connector) error {
	if c.ChallengeIDPrefix != "" {
		return fmt.Errorf("%w: connector %q (form %s) must not have challenge_id_prefix (only form flow)", ErrConfigInvalid, c.ID, c.Form)
	}
	if c.Models != nil {
		return fmt.Errorf("%w: connector %q (form %s) must not have models (only form flow)", ErrConfigInvalid, c.ID, c.Form)
	}
	return nil
}

// validateRequiredFacts は required_facts を検査する。form: cli の委譲先だけが持て、対応は空でなく、
// 値は 1 つ以上の要素を持つ（名前・値の型は読み込みの型の検査で済んでいる）。
func validateRequiredFacts(c Connector) error {
	if c.RequiredFacts == nil {
		return nil
	}
	if !c.UsesContractCommands() {
		return fmt.Errorf("%w: connector %q (form %s) must not have required_facts (only form cli or flow)", ErrConfigInvalid, c.ID, c.Form)
	}
	if len(c.RequiredFacts) == 0 {
		return fmt.Errorf("%w: connector %q required_facts must not be empty", ErrConfigInvalid, c.ID)
	}
	names := make([]string, 0, len(c.RequiredFacts))
	for name := range c.RequiredFacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values := c.RequiredFacts[name]
		if name == "" {
			return fmt.Errorf("%w: connector %q required_facts has an empty name", ErrConfigInvalid, c.ID)
		}
		if len(values) == 0 {
			return fmt.Errorf("%w: connector %q required_facts[%q] must not be an empty array", ErrConfigInvalid, c.ID, name)
		}
	}
	return nil
}

// hasReflectCommand は c が反映の依頼を始められる（form: flow か、form: cli で commands.reflect を持つ）か。
// form: flow の委譲先は、宣言に書かなくても反映の依頼の口を持つ。
func hasReflectCommand(c *Connector) bool {
	if !c.UsesContractCommands() {
		return false
	}
	return c.Form == ConnectorFormFlow || (c.Commands != nil && c.Commands.Reflect != nil)
}

func (d *ConnectorsDeclaration) connectorByID(id string) *Connector {
	for i := range d.Connectors {
		if d.Connectors[i].ID == id {
			return &d.Connectors[i]
		}
	}
	return nil
}

// validateReflectConnector は repos[].reflect_connector が、宣言にある form: cli で commands.reflect を持つ
// 委譲先を指すか検査する。口の無いリポジトリ（reflect_connector が無く、connector も commands.reflect を持たない）は拒否しない。
func (d *ConnectorsDeclaration) validateReflectConnector(r ConnectorRepo) error {
	if r.ReflectConnector == "" {
		return nil
	}
	c := d.connectorByID(r.ReflectConnector)
	switch {
	case c == nil:
		return fmt.Errorf("%w: repo %q reflect_connector refers to unknown connector %q", ErrConfigInvalid, r.Name, r.ReflectConnector)
	case !c.UsesContractCommands():
		return fmt.Errorf("%w: repo %q reflect_connector %q must be a form cli or flow connector", ErrConfigInvalid, r.Name, r.ReflectConnector)
	case !hasReflectCommand(c):
		return fmt.Errorf("%w: repo %q reflect_connector %q has no commands.reflect", ErrConfigInvalid, r.Name, r.ReflectConnector)
	}
	return nil
}

// ReflectConnectorFor は repo の反映の依頼の口になる委譲先を返す。順は
// ① reflect_connector があればその委譲先、② 無くて repo の connector が form: cli で commands.reflect を持てばその委譲先、
// ③ どちらでもなければ nil（口は無い。flywheel の外での反映を待つ扱い）。
func (d *ConnectorsDeclaration) ReflectConnectorFor(repo ConnectorRepo) *Connector {
	if repo.ReflectConnector != "" {
		return d.connectorByID(repo.ReflectConnector)
	}
	if c := d.connectorByID(repo.Connector); hasReflectCommand(c) {
		return c
	}
	return nil
}

func validateOperation(c Connector, op ConnectorOperation, opIDs map[string]bool) error {
	if op.ID == "" {
		return fmt.Errorf("%w: connector %q has an operation with an empty id", ErrConfigInvalid, c.ID)
	}
	if opIDs[op.ID] {
		return fmt.Errorf("%w: connector %q has duplicate operation id %q", ErrConfigInvalid, c.ID, op.ID)
	}
	opIDs[op.ID] = true

	if !isKnownCounterpart(string(op.Counterpart)) {
		return fmt.Errorf("%w: connector %q operation %q has invalid counterpart %q", ErrConfigInvalid, c.ID, op.ID, op.Counterpart)
	}
	if op.Artifacts != "" && !connectorArtifactsValues[op.Artifacts] {
		return fmt.Errorf("%w: connector %q operation %q has invalid artifacts %q", ErrConfigInvalid, c.ID, op.ID, op.Artifacts)
	}
	if err := validateInvocationPlaceholders(op.Invocation); err != nil {
		return err
	}

	if c.Form == ConnectorFormFlow {
		if op.Flow == "" {
			return fmt.Errorf("%w: connector %q (form flow) operation %q has no flow", ErrConfigInvalid, c.ID, op.ID)
		}
		if !flowOperationNames[op.Flow] {
			return fmt.Errorf("%w: connector %q operation %q has invalid flow %q (want ticket)", ErrConfigInvalid, c.ID, op.ID, op.Flow)
		}
	} else if op.Flow != "" {
		return fmt.Errorf("%w: connector %q (form %s) operation %q must not have flow (only form flow)", ErrConfigInvalid, c.ID, c.Form, op.ID)
	}

	switch c.Form {
	case ConnectorFormPlugin:
		if op.Invocation == "" {
			return fmt.Errorf("%w: connector %q (form plugin) operation %q has no invocation", ErrConfigInvalid, c.ID, op.ID)
		}
	case ConnectorFormBrief:
		if op.Invocation != "" {
			return fmt.Errorf("%w: connector %q (form brief) operation %q must not have invocation", ErrConfigInvalid, c.ID, op.ID)
		}
	}
	return nil
}

// validateCLICommands は c.Commands を検査する。start・status・resume・cancel はキーが無い（nil）ことも空配列も拒否し、
// 任意の reflect は空配列だけを拒否する。引数の差し込みは checkCLICommandPlaceholders の表（コマンドごとの閉集合）で検査する。
func validateCLICommands(c Connector) error {
	if c.Commands == nil {
		return fmt.Errorf("%w: connector %q (form cli) has no commands", ErrConfigInvalid, c.ID)
	}
	cmdGroups := []struct {
		name string
		args []string
	}{
		{"start", c.Commands.Start}, {"status", c.Commands.Status},
		{"resume", c.Commands.Resume}, {"cancel", c.Commands.Cancel},
	}
	for _, g := range cmdGroups {
		if g.args == nil {
			return fmt.Errorf("%w: connector %q (form cli) commands must include start, status, resume and cancel", ErrConfigInvalid, c.ID)
		}
		if len(g.args) == 0 {
			return fmt.Errorf("%w: connector %q (form cli) commands.%s must not be empty", ErrConfigInvalid, c.ID, g.name)
		}
	}
	if c.Commands.Reflect != nil && len(c.Commands.Reflect) == 0 {
		return fmt.Errorf("%w: connector %q (form cli) commands.reflect must not be empty", ErrConfigInvalid, c.ID)
	}
	if err := checkCLICommandPlaceholders(&c); err != nil {
		return fmt.Errorf("%w: %w", ErrConfigInvalid, err)
	}
	if c.ContractVersion == nil {
		return fmt.Errorf("%w: connector %q (form cli) has no contract_version", ErrConfigInvalid, c.ID)
	}
	if *c.ContractVersion != 1 {
		return fmt.Errorf("%w: connector %q contract_version must be 1, got %d", ErrConfigInvalid, c.ID, *c.ContractVersion)
	}
	return nil
}
