package core

import "context"

// supervisorHandReverify は手 reverify の実行（雛形。中身は後のタスクが、このファイルだけを書き換えて足す）。
func (s *Store) supervisorHandReverify(_ context.Context, _ supervisorHandInput) (supervisorHandOutcome, error) {
	return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
}
