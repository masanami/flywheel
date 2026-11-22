package core

import "context"

// supervisorHandRetry は手 retry の実行（雛形。中身は後のタスクが、このファイルだけを書き換えて足す）。
func (s *Store) supervisorHandRetry(_ context.Context, _ supervisorHandInput) (supervisorHandOutcome, error) {
	return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
}
