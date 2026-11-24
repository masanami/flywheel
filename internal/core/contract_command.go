package core

import (
	"fmt"
)

// contractCmd は契約のコマンドの種類。
type contractCmd string

const (
	contractCmdStart   contractCmd = "start"
	contractCmdStatus  contractCmd = "status"
	contractCmdResume  contractCmd = "resume"
	contractCmdCancel  contractCmd = "cancel"
	contractCmdReflect contractCmd = "reflect"
)

// delegatedRunPlaceholder は form: flow のコマンドの `--delegated-run` の値の仮の印。run の行を作る前に引数を組み立てる呼び出し元が
// 入れ、run の行を作った後の起動の直前に、その run の ID へ差し替える（withDelegatedRun）。
const delegatedRunPlaceholder = "{delegated_run}"

// SetFlowExecutable は form: flow の委譲先のコマンドの先頭に置く、自分の実行ファイルの絶対パスを渡す
// （internal/cli が os.Executable の結果を渡す。core はパスを決めない）。渡さないストアは、flow の形の契約のコマンドを組み立てられない。
func (s *Store) SetFlowExecutable(path string) { s.flowExecutable = path }

// cliCommandArgs は form: cli の宣言の commands から、コマンド cmd の引数の配列（差し込み前）を返す。
func cliCommandArgs(c *CLICommands, cmd contractCmd) []string {
	switch cmd {
	case contractCmdStart:
		return c.Start
	case contractCmdStatus:
		return c.Status
	case contractCmdResume:
		return c.Resume
	case contractCmdCancel:
		return c.Cancel
	case contractCmdReflect:
		return c.Reflect
	}
	return nil
}

// contractCommandArgs は契約のコマンド cmd の引数の配列を組み立てる、唯一の所。form: cli は宣言の commands の差し込み
// （引数と順は宣言のまま）、form: flow は自分の実行ファイルの flow のコマンドの表から作る。
// flow の形の start・resume・reflect の `--delegated-run` の値は仮の印（delegatedRunPlaceholder）で、finishContractCall が
// run の行を作った後に run の ID へ差し替える。
func (s *Store) contractCommandArgs(c *Connector, cmd contractCmd, f contractFill) ([]string, error) {
	if c == nil || !c.UsesContractCommands() {
		return nil, fmt.Errorf("%w: connector is not a contract connector", ErrValidation)
	}
	if c.Form == ConnectorFormFlow {
		return s.flowCommandArgs(c, cmd, f)
	}
	if c.Commands == nil {
		return nil, fmt.Errorf("%w: connector %q has no commands", ErrValidation, c.ID)
	}
	return fillContractArgsWith(cliCommandArgs(c.Commands, cmd), f)
}

// flowCommandArgs は form: flow の契約のコマンドを、自分の実行ファイルの flow のコマンドとして組み立てる。
func (s *Store) flowCommandArgs(c *Connector, cmd contractCmd, f contractFill) ([]string, error) {
	if s.flowExecutable == "" {
		return nil, fmt.Errorf("%w: the executable path for form flow connector %q is not set", ErrValidation, c.ID)
	}
	base := []string{s.flowExecutable, "flow"}
	ws := []string{"--workspace", s.workspace}
	perm := []string{"--permission-mode", string(c.PermissionMode), "--delegated-run", delegatedRunPlaceholder}
	challengeID := c.ChallengeIDPrefix + f.ChallengeID

	switch cmd {
	case contractCmdStart:
		issue := ""
		if f.Source != nil {
			_, number, ok := parseExternalKey(f.Source.ExternalKey)
			if !ok {
				return nil, fmt.Errorf("%w: malformed external_key %q", ErrValidation, f.Source.ExternalKey)
			}
			issue = fmt.Sprintf("%d", number)
		}
		usesIntegration := "false"
		if f.UsesIntegration {
			usesIntegration = "true"
		}
		if f.Flow == "" {
			return nil, fmt.Errorf("%w: the operation of connector %q has no flow", ErrValidation, c.ID)
		}
		args := append(append(append(base, "start"), ws...), perm...)
		args = append(args,
			"--input", "issue="+issue,
			"--input", "task_file="+f.TaskFile,
			"--input", "task_id="+f.TaskID,
			"--input", "uses_integration="+usesIntegration,
			"--input", "challenge_id="+challengeID,
			f.Flow)
		return args, nil
	case contractCmdReflect:
		args := append(append(append(base, "start"), ws...), perm...)
		args = append(args,
			"--input", "artifact_ref="+f.ArtifactRef,
			"--input", "challenge_id="+challengeID,
			"reflect")
		return args, nil
	case contractCmdStatus:
		return append(append(append(base, "status"), ws...), f.RunID), nil
	case contractCmdCancel:
		return append(append(append(base, "cancel"), ws...), f.RunID), nil
	case contractCmdResume:
		args := append(append(append(base, "resume"), ws...), perm...)
		if f.Answer.Choice != "" {
			args = append(args, "--input", f.Answer.Choice)
		}
		if f.Answer.Note != "" {
			args = append(args, "--note", f.Answer.Note)
		}
		if f.ApprovedRef != "" {
			args = append(args, "--approved-ref", f.ApprovedRef)
		}
		return append(args, f.RunID), nil
	}
	return nil, fmt.Errorf("%w: unknown contract command %q", ErrValidation, cmd)
}

// withDelegatedRun は command の `--delegated-run` の値の仮の印を runID へ差し替えた複製を返す（印が無ければ command のまま）。
// 引数の値（添え書きなど）が印と同じ文字列でも差し替えないよう、フラグの直後の要素だけを見る。
func withDelegatedRun(command []string, runID string) []string {
	for i := 0; i+1 < len(command); i++ {
		if command[i] == "--delegated-run" && command[i+1] == delegatedRunPlaceholder {
			out := append([]string(nil), command...)
			out[i+1] = runID
			return out
		}
		if command[i] == "--note" || command[i] == "--input" || command[i] == "--approved-ref" {
			i++ // 値は読まない
		}
	}
	return command
}
