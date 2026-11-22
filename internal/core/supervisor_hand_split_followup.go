package core

import "context"

// supervisorHandSplitFollowup は手 split_followup の実行（雛形。中身は後のタスクが、このファイルだけを書き換えて足す）。
func (s *Store) supervisorHandSplitFollowup(_ context.Context, _ supervisorHandInput) (supervisorHandOutcome, error) {
	return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
}
