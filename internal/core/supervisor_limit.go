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
