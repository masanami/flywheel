package core

import (
	"fmt"
	"strings"
)

// flywheelRepoRemote は flywheel 自身のリポジトリの `repos[].remote`
// （<owner>/<name> の形。大文字小文字は区別しない）。
const flywheelRepoRemote = "masanami/flywheel"

// improvementRouteIssue は経路が決まらないときの 1 行。起票先は J2 に任せる。
const improvementRouteIssue = "成果物の経路: Issue"

// improvementRoute は改修課題の説明に書く「成果物の経路」の 1 行を決定的に返す。
// decl が nil のときはどの層も「Issue」とだけ書く。
// originURL はワークスペースの origin の URL（読めなければ空）。git は起動しない。
func improvementRoute(layer OccurrenceLayer, decl *ConnectorsDeclaration, originURL string) string {
	if decl == nil {
		return improvementRouteIssue
	}
	switch layer {
	case OccurrenceLayerPrompt, OccurrenceLayerEntrySkill:
		for _, r := range decl.Repos {
			if strings.EqualFold(r.Remote, flywheelRepoRemote) {
				return fmt.Sprintf("成果物の経路: PR（対象のリポジトリ: %s）", r.Remote)
			}
		}
	case OccurrenceLayerKnowledge:
		for _, r := range decl.Repos {
			if remoteMatches(originURL, r.Remote) {
				return fmt.Sprintf("成果物の経路: ワークスペースの変更（対象のリポジトリ: %s）", r.Remote)
			}
		}
	}
	return improvementRouteIssue
}
