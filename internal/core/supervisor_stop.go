package core

import "context"

// supervisorEnabled は宣言の supervisor の値。宣言が無い（nil）ときは、宣言の既定と同じく真。
func supervisorEnabled(decl *AgentDeclaration) bool { return decl == nil || decl.Supervisor }

// stopHoldOptions は、保留を作る呼び出し元が止まり方 kind を渡すための指定を返す。止まり方は保留の理由から導かず、呼び出し元が決める。
// 宣言の supervisor が偽のときは、止まり方を残しても保留はスーパーバイザー待ちにならない。
func stopHoldOptions(kind StopKind, decl *AgentDeclaration) HoldOptionsInput {
	return HoldOptionsInput{StopKind: kind, SupervisorEnabled: supervisorEnabled(decl)}
}

// insertHoldStop は insertHold の、止まり方（と宣言の supervisor）を渡す版。
func insertHoldStop(ctx context.Context, tc *transitionCtx, question string, opts HoldOptionsInput) error {
	_, err := insertHoldWithOptions(ctx, tc, question, HoldKindHuman, tc.current.Status, tc.taskID, opts)
	return err
}
