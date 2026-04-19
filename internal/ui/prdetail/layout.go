package prdetail

import "github.com/blakewilliams/gg/internal/ui/diffviewer"

func (m Model) View() string {
	if !m.dv.VPReady {
		return ""
	}

	// Right panel content — highlights are baked into rendered lines.
	rightView := m.dv.VP.View()

	// Determine right panel title.
	var rightTitle string
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		rightTitle = m.dv.Files[m.dv.CurrentFileIdx].Filename
	} else {
		rightTitle = "Description"
	}

	// Compose: tree | divider | right panel.
	view := m.dv.RenderLayout(rightView, rightTitle, diffviewer.LayoutInfo{
		HelpMode: m.ctx.Config.HelpMode,
		HelpLine: m.helpLine(),
	})

	// Search popup overlay.
	if m.dv.Searching {
		view = m.dv.RenderSearchPopup(view, m.dv.Height)
	}

	// Modal overlay on top.
	if m.showSidebar {
		view = m.renderModal(view)
	}
	return view
}
