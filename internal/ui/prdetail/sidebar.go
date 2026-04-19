package prdetail

import (
	"strings"

	"github.com/blakewilliams/gg/internal/github"
	"github.com/blakewilliams/gg/internal/ui/components"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// --- Comments ---

var authorBadge = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Black).
	Background(lipgloss.Yellow)

func (m Model) roundedBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.ctx.DiffColors.BorderColor).
		Padding(0, 1)
}

// coloredAuthor is a convenience alias for the shared component.
var coloredAuthor = components.ColoredAuthor

// --- Reviews / Comments ---

func (m Model) hasReviewContent() bool {
	return len(m.reviews) > 0 || len(m.pr.RequestedReviewers) > 0
}

var (
	reviewApproved  = lipgloss.NewStyle().Foreground(lipgloss.Green).Bold(true)
	reviewChanges   = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
	reviewCommented = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	reviewPending   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
)

func reviewStateIcon(state string) string {
	switch state {
	case "APPROVED":
		return reviewApproved.Render(iconCheckCircle + " approved")
	case "CHANGES_REQUESTED":
		return reviewChanges.Render(iconXCircle + " changes requested")
	case "COMMENTED":
		return reviewCommented.Render(iconComment + " commented")
	case "DISMISSED":
		return reviewCommented.Render(iconSlash + " dismissed")
	default:
		return reviewPending.Render(iconClock + " pending")
	}
}

// rebuildSidebar rebuilds the right sidebar viewport content.
func (m *Model) rebuildSidebar() {
	if !m.showSidebar {
		return
	}
	const pad = 4
	modalW := m.dv.Width - pad*2
	modalH := m.dv.Height - pad*2
	if modalW < 20 {
		modalW = 20
	}
	if modalH < 5 {
		modalH = 5
	}
	contentPad := 2
	innerW := modalW - 2 - contentPad*2 // inside borders + padding
	contentH := modalH - 2              // inside top/bottom borders

	var lines []string
	switch m.sidebarType {
	case sidebarComments:
		lines = m.buildCommentLines(innerW)
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No comments yet.")}
		}
	case sidebarReviews:
		lines = m.buildReviewLines(innerW)
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No reviews yet.")}
		}
	case sidebarChecks:
		lines = m.buildCheckLines()
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No checks yet.")}
		}
	}

	sep := m.dv.BorderStyle().Render(strings.Repeat("─", innerW))
	content := strings.Join(lines, "\n"+sep+"\n")

	m.sidebarVP = viewport.New()
	m.sidebarVP.SetWidth(innerW)
	m.sidebarVP.SetHeight(contentH)
	m.sidebarVP.SetContent(content)
}

// buildReviewLines builds the content lines for the reviews section.
func (m Model) buildReviewLines(innerW int) []string {
	// Deduplicate reviews — keep only the latest per user.
	latestByUser := make(map[string]github.Review)
	for _, r := range m.reviews {
		if r.State == "PENDING" {
			continue
		}
		existing, ok := latestByUser[r.User.Login]
		if !ok || r.SubmittedAt.After(existing.SubmittedAt) {
			latestByUser[r.User.Login] = r
		}
	}

	var lines []string
	for _, r := range m.reviews {
		latest, ok := latestByUser[r.User.Login]
		if !ok || latest.ID != r.ID {
			continue
		}
		delete(latestByUser, r.User.Login)

		author := coloredAuthor(r.User.Login)
		line := author + " " + reviewStateIcon(r.State)
		if r.Body != "" {
			body := renderMarkdown(r.Body, innerW)
			line += "\n" + body
		}
		lines = append(lines, line)
	}

	// Requested reviewers (haven't reviewed yet).
	for _, u := range m.pr.RequestedReviewers {
		if _, reviewed := latestByUser[u.Login]; reviewed {
			continue
		}
		alreadyRendered := false
		for _, r := range m.reviews {
			if r.User.Login == u.Login {
				alreadyRendered = true
				break
			}
		}
		if alreadyRendered {
			continue
		}
		author := coloredAuthor(u.Login)
		lines = append(lines, author+" "+reviewPending.Render(iconClock+" awaiting review"))
	}

	return lines
}

// buildCommentLines builds the content lines for the comments section.
func (m Model) buildCommentLines(innerW int) []string {
	var lines []string
	for _, c := range m.comments {
		author := coloredAuthor(c.User.Login)
		if c.User.Login == m.pr.User.Login {
			author += " " + authorBadge.Render(" "+iconAuthor+" Author ")
		}
		age := dimStyle.Render(relativeTime(c.CreatedAt))

		line := author + " " + age
		if c.Body != "" {
			body := renderMarkdown(c.Body, innerW)
			line += "\n" + body
		}
		lines = append(lines, line)
	}
	return lines
}

// buildCheckLines builds the content lines for the checks section.
func (m Model) buildCheckLines() []string {
	var lines []string
	for _, c := range m.checkRuns {
		var icon string
		switch {
		case c.Status != "completed":
			icon = reviewPending.Render(iconClock + " in progress")
		case c.Conclusion != nil:
			switch *c.Conclusion {
			case "success":
				icon = reviewApproved.Render(iconCheckCircle + " passed")
			case "failure":
				icon = reviewChanges.Render(iconXCircle + " failed")
			case "cancelled":
				icon = dimStyle.Render(iconSlash + " cancelled")
			case "skipped":
				icon = dimStyle.Render(iconSlash + " skipped")
			case "neutral":
				icon = dimStyle.Render(iconCheckCircle + " neutral")
			default:
				icon = reviewPending.Render(iconClock + " " + *c.Conclusion)
			}
		default:
			icon = reviewPending.Render(iconClock + " pending")
		}

		name := lipgloss.NewStyle().Bold(true).Render(c.Name)
		lines = append(lines, name+" "+icon)
	}
	return lines
}
