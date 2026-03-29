package components

import (
	"strings"

	"github.com/blakewilliams/ghq/internal/ui/styles"
)

func RenderTabs(tabs []string, activeIndex int) string {
	var rendered []string
	for i, tab := range tabs {
		if i == activeIndex {
			rendered = append(rendered, styles.TabActive.Render(" "+tab+" "))
		} else {
			rendered = append(rendered, styles.TabInactive.Render(" "+tab+" "))
		}
	}
	return strings.Join(rendered, " ")
}
