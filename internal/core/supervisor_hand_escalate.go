package core

import "context"

// supervisorHandEscalate は手 escalate の実行（雛形。中身は後のタスクが、このファイルだけを書き換えて足す）。
func (s *Store) supervisorHandEscalate(_ context.Context, _ supervisorHandInput) (supervisorHandOutcome, error) {
	return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
}
