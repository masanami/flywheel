package cli

import (
	"context"
	"fmt"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runRequirementCreate は `flywheel requirement create` の実装。
func runRequirementCreate(a Args) (any, error) {
	r, err := a.Store.CreateRequirement(context.Background(), core.ChannelCLI,
		core.CreateRequirementInput{Title: a.Values["title"], Chat: a.Chat.coreMark()})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromCreatedRequirement(*r),
		text: fmt.Sprintf("要求:       %s\n題名:       %s\n出どころ:   %s\n証跡の場所: %s\n", r.ID, r.Title, r.Origin, r.EvidencePath),
	}, nil
}
