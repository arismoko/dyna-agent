package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

type guideModel struct {
	raw      string
	rendered bool
	vp       viewport.Model
}

func newGuideModel(md string) guideModel {
	return guideModel{raw: md, vp: viewport.New(0, 0)}
}

func (m *guideModel) setSize(w, h int) {
	m.vp.Width = w
	m.vp.Height = h
	m.rendered = false
}

func (m *guideModel) render() {
	width := clamp(m.vp.Width-2, 40, 110)
	out := m.raw
	if r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width)); err == nil {
		if rendered, err := r.Render(m.raw); err == nil {
			out = rendered
		}
	}
	m.vp.SetContent(out)
	m.rendered = true
}

func (m guideModel) update(msg tea.Msg) (guideModel, tea.Cmd) {
	if !m.rendered {
		m.render()
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m guideModel) view() string {
	if !m.rendered {
		m.render()
	}
	return m.vp.View()
}
