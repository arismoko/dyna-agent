package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dyna-agent/internal/profile"
)

type profilesModel struct {
	width, height int
	store         *profile.Store
	sel           int
	editing       bool
	form          formModel
	statusMsg     string
	confirmDel    bool
}

func newProfilesModel(store *profile.Store) profilesModel {
	return profilesModel{store: store}
}

func (m *profilesModel) setSize(w, h int) { m.width, m.height = w, h }

func (m profilesModel) update(msg tea.KeyMsg) (profilesModel, tea.Cmd) {
	if m.editing {
		done, saved, cmd := m.form.update(msg)
		if done {
			m.editing = false
			if saved {
				p := m.form.toProfile()
				if m.form.origPk != "" && m.form.origPk != p.Name {
					m.store.Remove(m.form.origPk) // renamed while editing
				}
				if err := m.store.Upsert(p); err != nil {
					m.statusMsg = "✗ " + err.Error()
				} else if err := m.store.Save(); err != nil {
					m.statusMsg = "✗ " + err.Error()
				} else {
					m.statusMsg = "✓ saved " + p.Name
				}
			}
		}
		return m, cmd
	}

	if m.confirmDel {
		switch msg.String() {
		case "y", "Y":
			if m.sel < len(m.store.Profiles) {
				m.store.Remove(m.store.Profiles[m.sel].Name)
				m.store.Save()
				m.sel = clamp(m.sel, 0, len(m.store.Profiles)-1)
				m.statusMsg = "deleted"
			}
		}
		m.confirmDel = false
		return m, nil
	}

	switch msg.String() {
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.store.Profiles)-1 {
			m.sel++
		}
	case "a":
		m.form = newForm(nil)
		m.editing = true
		m.statusMsg = ""
	case "e", "enter":
		if m.sel < len(m.store.Profiles) {
			p := m.store.Profiles[m.sel]
			m.form = newForm(&p)
			m.editing = true
			m.statusMsg = ""
		}
	case "d":
		if m.sel < len(m.store.Profiles) {
			m.confirmDel = true
		}
	case "s":
		if m.sel < len(m.store.Profiles) {
			p := m.store.Profiles[m.sel]
			p.Default = true
			m.store.Upsert(p)
			m.store.Save()
			m.statusMsg = "✓ " + p.Name + " is now default"
		}
	}
	return m, nil
}

func (m profilesModel) view() string {
	if m.editing {
		return m.form.view(m.width, m.height)
	}
	lw := clamp(m.width/3, 30, 44)
	rw := m.width - lw - 6

	var b strings.Builder
	b.WriteString(sTitle.Render("Worker profiles") + "\n")
	if len(m.store.Profiles) == 0 {
		b.WriteString(sDim.Render("\nnone yet — press ") + sHelpKey.Render("a") + sDim.Render(" to add\nor run `dyna demo`"))
	}
	for i, p := range m.store.Profiles {
		icon := sDim.Render("○")
		if p.Default {
			icon = sOK.Render("●")
		}
		name := p.Name
		row := icon + " " + name
		if i == m.sel {
			row = icon + " " + sSel.Render(name)
		}
		row += "  " + sDim.Render(p.Harness)
		b.WriteString(row + "\n")
	}
	if m.confirmDel && m.sel < len(m.store.Profiles) {
		b.WriteString("\n" + sErrS.Render("delete "+m.store.Profiles[m.sel].Name+"? (y/n)"))
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + sDim.Render(m.statusMsg))
	}
	left := sPaneL.Width(lw).Height(m.height - 2).Render(b.String())

	right := sPaneR.Width(rw).Height(m.height - 2).Render(m.viewCard(rw - 4))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (m profilesModel) viewCard(w int) string {
	if m.sel >= len(m.store.Profiles) {
		return sDim.Render("select a profile — these are the workers agents may orchestrate")
	}
	p := m.store.Profiles[m.sel]
	var b strings.Builder
	name := sTitle.Render(p.Name)
	if p.Default {
		name += "  " + sBadge.Render("default")
	}
	b.WriteString(name + "\n\n")
	b.WriteString(sDim.Render("harness ") + p.Harness)
	if p.Model != "" {
		b.WriteString(sDim.Render("   model ") + p.Model)
	}
	b.WriteString("\n\n")

	stat := func(label string, v int, c lipgloss.AdaptiveColor, note string) {
		b.WriteString(fmt.Sprintf("%-14s %s  %d/5  %s\n", label, statBar(v, c), v, sDim.Render(note)))
	}
	stat("taste", p.Taste, cTaste, "quality · design · review judgment")
	stat("intelligence", p.Intelligence, cIntel, "hard, long, complex tasks")
	stat("cost-eff.", p.Cost, cCost, "higher = cheaper to run")
	b.WriteString("\n")

	if p.Description != "" {
		b.WriteString(sTitle.Render("Description") + "\n")
		b.WriteString(lipgloss.NewStyle().Width(w).Render(p.Description) + "\n\n")
	}
	if len(p.ExtraArgs) > 0 {
		b.WriteString(sDim.Render("extra args: "+strings.Join(p.ExtraArgs, " ")) + "\n")
	}
	if len(p.Command) > 0 {
		b.WriteString(sDim.Render("command: "+strings.Join(p.Command, " ")) + "\n")
	}
	if p.TimeoutSec > 0 {
		b.WriteString(sDim.Render(fmt.Sprintf("timeout: %ds", p.TimeoutSec)) + "\n")
	}
	return b.String()
}

// ---------------- form ----------------

type fieldKind int

const (
	fText fieldKind = iota
	fCycle
	fStat
)

type field struct {
	label  string
	kind   fieldKind
	input  textinput.Model
	cycle  []string
	cycleI int
	stat   int
	note   string
}

type formModel struct {
	fields []field
	focus  int
	origPk string // original name when editing ("" = new)
	errMsg string
}

func textField(label, placeholder, value, note string) field {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.Prompt = ""
	ti.CharLimit = 500
	return field{label: label, kind: fText, input: ti, note: note}
}

func newForm(p *profile.Profile) formModel {
	var v profile.Profile
	if p != nil {
		v = *p
	} else {
		v = profile.Profile{Harness: profile.HarnessClaudeCode, Taste: 3, Intelligence: 3, Cost: 3}
	}
	harnessIdx := 0
	for i, h := range profile.Harnesses {
		if h == v.Harness {
			harnessIdx = i
		}
	}
	f := formModel{}
	if p != nil {
		f.origPk = p.Name
	}
	f.fields = []field{
		textField("name", "e.g. opus-4.8", v.Name, "unique id agents reference in scripts"),
		textField("description", "personality, strengths, weaknesses…", v.Description, "agents read this to pick workers"),
		{label: "harness", kind: fCycle, cycle: profile.Harnesses, cycleI: harnessIdx, note: "which CLI runs this worker"},
		textField("model", "e.g. opus | gpt-5.5 | zai/glm-5.2", v.Model, "model id passed to the CLI"),
		{label: "taste", kind: fStat, stat: v.Taste, note: "quality · design · review judgment"},
		{label: "intelligence", kind: fStat, stat: v.Intelligence, note: "hard, long, complex tasks"},
		{label: "cost-eff.", kind: fStat, stat: v.Cost, note: "higher = cheaper (5 = very cheap)"},
		textField("extra args", "--dangerously-skip-permissions …", strings.Join(v.ExtraArgs, " "), "extra CLI args, space-separated"),
		{label: "default", kind: fCycle, cycle: []string{"no", "yes"}, cycleI: boolIdx(v.Default), note: "used when a script omits profile"},
	}
	f.fields[0].input.Focus()
	return f
}

func boolIdx(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (f *formModel) toProfile() profile.Profile {
	p := profile.Profile{
		Name:         strings.TrimSpace(f.fields[0].input.Value()),
		Description:  strings.TrimSpace(f.fields[1].input.Value()),
		Harness:      f.fields[2].cycle[f.fields[2].cycleI],
		Model:        strings.TrimSpace(f.fields[3].input.Value()),
		Taste:        f.fields[4].stat,
		Intelligence: f.fields[5].stat,
		Cost:         f.fields[6].stat,
		Default:      f.fields[8].cycleI == 1,
	}
	if ea := strings.TrimSpace(f.fields[7].input.Value()); ea != "" {
		p.ExtraArgs = strings.Fields(ea)
	}
	return p
}

// update returns (done, saved).
func (f *formModel) update(msg tea.KeyMsg) (bool, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return true, false, nil
	case "ctrl+s":
		p := f.toProfile()
		if err := p.Validate(); err != nil {
			f.errMsg = err.Error()
			return false, false, nil
		}
		return true, true, nil
	case "up", "shift+tab":
		f.setFocus(f.focus - 1)
		return false, false, nil
	case "down", "tab", "enter":
		if msg.String() == "enter" && f.focus == len(f.fields)-1 {
			p := f.toProfile()
			if err := p.Validate(); err != nil {
				f.errMsg = err.Error()
				return false, false, nil
			}
			return true, true, nil
		}
		f.setFocus(f.focus + 1)
		return false, false, nil
	}

	cur := &f.fields[f.focus]
	switch cur.kind {
	case fCycle:
		switch msg.String() {
		case "left", "h":
			cur.cycleI = (cur.cycleI + len(cur.cycle) - 1) % len(cur.cycle)
		case "right", "l", " ":
			cur.cycleI = (cur.cycleI + 1) % len(cur.cycle)
		}
	case fStat:
		switch msg.String() {
		case "left", "h":
			cur.stat = clamp(cur.stat-1, 1, 5)
		case "right", "l":
			cur.stat = clamp(cur.stat+1, 1, 5)
		case "1", "2", "3", "4", "5":
			cur.stat = int(msg.String()[0] - '0')
		}
	case fText:
		var cmd tea.Cmd
		cur.input, cmd = cur.input.Update(msg)
		return false, false, cmd
	}
	return false, false, nil
}

func (f *formModel) setFocus(i int) {
	f.fields[f.focus].input.Blur()
	f.focus = clamp(i, 0, len(f.fields)-1)
	if f.fields[f.focus].kind == fText {
		f.fields[f.focus].input.Focus()
	}
}

func (f formModel) view(width, height int) string {
	title := "Add worker profile"
	if f.origPk != "" {
		title = "Edit " + f.origPk
	}
	var b strings.Builder
	b.WriteString(sTitle.Render(title) + "\n\n")
	for i, fd := range f.fields {
		cursor := "  "
		labelSt := sDim
		if i == f.focus {
			cursor = sHelpKey.Render("▸ ")
			labelSt = sTitle
		}
		var val string
		switch fd.kind {
		case fText:
			val = fd.input.View()
		case fCycle:
			val = "◂ " + sProfTag.Render(fd.cycle[fd.cycleI]) + " ▸"
		case fStat:
			c := cTaste
			switch fd.label {
			case "intelligence":
				c = cIntel
			case "cost-eff.":
				c = cCost
			}
			val = statBar(fd.stat, c) + fmt.Sprintf("  %d/5", fd.stat)
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, labelSt.Render(fmt.Sprintf("%-13s", fd.label)), val))
		if i == f.focus && fd.note != "" {
			b.WriteString(sDim.Render(strings.Repeat(" ", 17)+fd.note) + "\n")
		}
	}
	if f.errMsg != "" {
		b.WriteString("\n" + sErrS.Render("✗ "+f.errMsg))
	}
	w := clamp(width-4, 40, 100)
	return sPaneR.Width(w).Render(b.String())
}
