package keepwarm

import "github.com/aiproxer/aiproxer/pkg/aipapi"

func hasFinishedOSCommand(events []aipapi.ToolEvent) bool {
	for _, event := range events {
		if event.Kind == aipapi.ToolEventFinished && event.Category == aipapi.ToolCategoryOSCommand {
			return true
		}
	}
	return false
}
