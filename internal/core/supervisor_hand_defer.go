package core

import "context"

// supervisorHandDefer は手 defer の実行（雛形。中身は後のタスクが、このファイルだけを書き換えて足す）。
func (s *Store) supervisorHandDefer(_ context.Context, _ supervisorHandInput) (supervisorHandOutcome, error) {
	return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
}
