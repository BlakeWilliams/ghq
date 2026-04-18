package prdetail

import "github.com/blakewilliams/ghq/internal/ui/diffviewer"

func (m Model) View() string {
	if !m.dv.VPReady {
		return ""
	}

	// Right panel content with cursor overlay.
	rightView := m.dv.VP.View()
	if m.dv.CurrentFileIdx >= 0 {
		rightView = m.dv.OverlayDiffCursor(rightView)
	}

	// Determine right panel title.
	var rightTitle string
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		rightTitle = m.dv.Files[m.dv.CurrentFileIdx].Filename
	} else {
		rightTitle = "Description"
	}

	// Compose: tree | divider | right panel.
	view := m.dv.RenderLayout(rightView, rightTitle, diffviewer.LayoutInfo{})

	// Modal overlay on top.
	if m.showSidebar {
		view = m.renderModal(view)
	}
	return view
}
