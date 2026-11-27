package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// このファイルは回数の上限（区切り S4b-2）の判定を持つ。同じタスク（タスクの無い保留は同じ課題）の同じ止まり方への対処は
// supervisorActionLimit 回まで。数えるのは、最後のオーナーの回答より後の、手が見送り・上げるでなく、結果が executed の対処の記録
// （SupervisorHandledCountQuery）。判定は、保留を作る入口（insertHoldWithOptions）が、行を作る前に同じ書き込みの中で呼ぶ。

// supervisorActionLimit は、同じ止まり方への対処の上限の回数。
const supervisorActionLimit = 2

// supervisorLimitHit は、上限に当たったときの対処の手（実行の順）を返す。当たらなければ nil。
func supervisorLimitHit(ctx context.Context, tx *sql.Tx, challengeID int64, taskID *int64, kind StopKind) ([]SupervisorHand, error) {
	if kind == StopKindChildQuestion {
		return nil, nil // 子の問いは、この上限の対象にしない（答えによる再開の上限だけで止める）
	}
	task := ""
	if taskID != nil {
		task = formatTaskID(*taskID)
	}
	hands, err := supervisorActionHands(ctx, tx, SupervisorHandledCountQuery(formatChallengeID(challengeID), task, kind))
	if err != nil {
		return nil, err
	}
	if len(hands) < supervisorActionLimit {
		return nil, nil
	}
	return hands[:supervisorActionLimit], nil
}

// supervisorLimitNotice は、上限に当たった保留の問いの先頭に付ける固定の 1 行（手は実行した順）。
func supervisorLimitNotice(hands []SupervisorHand) string {
	names := make([]string, len(hands))
	for i, h := range hands {
		names[i] = string(h)
		if h == "" {
			names[i] = "判断できなかった"
		}
	}
	return fmt.Sprintf("スーパーバイザーが %d 回対処したが、進まなかった（手: %s）。", supervisorActionLimit, strings.Join(names, "、"))
}

// このファイルの後半は、答えによる再開（手 answer）の上限（区切り S4b-3）。1 つの課題で、最後のオーナーの回答より後に 3 回まで。
// 数えるのは、その課題の、手が answer で結果が executed の対処の記録（タスクは問わない。止まり方は、下書きの問い
// spec_question を除いて問わない。下書きの側の歯止めは実行の回数の上限。過去の J4 の実行の記録は数えない）。

// supervisorAnswerLimit は、1 つの課題で、スーパーバイザーが答えて再開できる回数の上限。
const supervisorAnswerLimit = 3

// supervisorAnswerLimitNotice は、再開の上限で上げる保留の問いの先頭に付ける固定の 1 行。
const supervisorAnswerLimitNotice = "スーパーバイザーが答えて再開できる回数の上限（3 回）に達した。"

// supervisorAnswerLimitReached は、課題 challengeID の答えによる再開が上限に達しているかを返す。読み取りだけ。
// 達しているときの子の問いは、判断点を呼ばず、問いの先頭に supervisorAnswerLimitNotice を付けた保留（対処の記録は作らない）にして、オーナーへ上げる。
func supervisorAnswerLimitReached(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	n, err := countSupervisorActions(ctx, tx, SupervisorActionCountQuery{
		ChallengeID: formatChallengeID(challengeID), AnyTask: true, Hands: []SupervisorHand{HandAnswer},
		ExecutedOnly: true, AfterLastOwnerAnswer: true, ExcludeStopKinds: []StopKind{StopKindSpecQuestion},
	})
	return n >= supervisorAnswerLimit, err
}

// prefixSupervisorAnswerLimitQuestion は問いの先頭へ再開の上限の固定の 1 行を付ける（すでに付いていれば付けない）。
func prefixSupervisorAnswerLimitQuestion(question string) string {
	if strings.HasPrefix(question, supervisorAnswerLimitNotice+"\n") {
		return question
	}
	return supervisorAnswerLimitNotice + "\n" + question
}

// answerLimitReached は supervisorAnswerLimitReached を、読み取りの 1 つのトランザクションで呼ぶ。
func (s *Store) answerLimitReached(ctx context.Context, challengeID int64) (bool, error) {
	var reached bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		reached, err = supervisorAnswerLimitReached(ctx, tx, challengeID)
		return err
	})
	return reached, classifyReadWriteErr(err)
}
