package core

import (
	"context"
	"fmt"
)

// このファイルはスーパーバイザーの段の「結果を確かめる」部分。判断点の出力を検査し、手を実行できる形かを返す（検査は supervisor_output.go）。

// verifySupervisorResult は判断点の呼び出しの結果を確かめる。手を実行してよい出力なら out を返し、
// 不備があれば理由（bad）を返す（手を実行しない）。検査: 実行が成功で終わったこと・出力の形と必須の項目・
// 返ってきた手が入力に渡した選べる手の一覧にあること・選択肢の決まり。出力の不備の run は、失敗に数える。
func (s *Store) verifySupervisorResult(ctx context.Context, call svCall) (out *SVOutput, bad string) {
	out, flaw := inspectSupervisorOutput(call.Run, call.Offered)
	if flaw == nil {
		return out, ""
	}
	if flaw.BadOutput {
		// 出力の不備の run は、連続の失敗に数える（成功のままにしない）。記録の失敗は、不備の理由を優先して握りつぶさない。
		if invErr := s.invalidateRunOutput(ctx, call.Run.RunID); invErr != nil {
			return nil, fmt.Sprintf("%s。失敗の記録にも失敗した（%v）", flaw.Reason, invErr)
		}
	}
	return nil, flaw.Reason
}
