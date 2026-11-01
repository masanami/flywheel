package core

import "errors"

// CheckDeclarations は base の 3 つの宣言（agent.json・sources.json・connectors.json）を
// cycle と同じ読み込み・検査で確かめ、agent.json を返す。不備なら ErrConfigInvalid を返す。
// 宣言ファイルが無い（ErrConfigNotFound）ことはエラーにしない（その宣言は nil）。
func CheckDeclarations(base string) (*AgentDeclaration, error) {
	var agent *AgentDeclaration
	switch d, err := LoadAgentDeclaration(base); {
	case err == nil:
		agent = d
	case errors.Is(err, ErrConfigNotFound):
	default:
		return nil, err
	}
	switch sources, err := LoadSourcesDeclaration(base); {
	case err == nil:
		if _, selErr := SelectSources(sources, nil); selErr != nil {
			return nil, selErr
		}
	case errors.Is(err, ErrConfigNotFound):
	default:
		return nil, err
	}
	if _, err := LoadConnectorsDeclaration(base); err != nil && !errors.Is(err, ErrConfigNotFound) {
		return nil, err
	}
	return agent, nil
}
