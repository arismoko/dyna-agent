package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dyna-agent/internal/detect"
	"dyna-agent/internal/profile"
)

// The wizard is a sequence of multiple-choice slides:
//
//	1 harness → 2 model → 3 reasoning effort → 4 stats → 5 description →
//	6 everything else (name, limits, enabled, default) + save.
type wizStep int

const (
	stepHarness wizStep = iota
	stepModel
	stepEffort
	stepStats
	stepDesc
	stepFinal
	wizSteps = 6
)

var wizTitles = []string{"Harness", "Model", "Reasoning effort", "Stats", "Description", "Finish"}

// wizModelsMsg delivers the async model probe for the chosen harness.
type wizModelsMsg struct {
	harness string
	models  []detect.Model
}

type wizModel struct {
	step wizStep
	sel  int // option cursor on choice slides

	harness string

	models      []detect.Model
	loading     bool
	customInput textinput.Model // model id / custom argv
	typing      bool            // customInput active
	model       string
	command     []string // custom harness argv

	efforts   []string
	effort    string
	hadEffort bool // whether the effort slide was shown (for back-nav)

	stats   [3]int // taste, intelligence, cost
	statSel int

	desc textinput.Model

	// final slide
	finSel   int
	name     textinput.Model
	limConc  textinput.Model
	limCalls textinput.Model
	enabled  bool
	def      bool
	errMsg   string
}

func newWizard() *wizModel {
	w := &wizModel{stats: [3]int{5, 5, 5}, enabled: true}
	w.customInput = wizInput("model id, e.g. zai/glm-5.2", 300)
	w.desc = wizInput("personality, strengths, weaknesses — agents read this to pick workers", 500)
	w.name = wizInput("unique name", 80)
	w.limConc = wizInput("0 = unlimited", 6)
	w.limCalls = wizInput("0 = unlimited", 6)
	return w
}

func wizInput(placeholder string, limit int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = limit
	return ti
}

func fetchModelsCmd(harness string) tea.Cmd {
	return func() tea.Msg { return wizModelsMsg{harness: harness, models: detect.Models(harness)} }
}

func (w *wizModel) setModels(msg wizModelsMsg) {
	if msg.harness != w.harness {
		return
	}
	w.loading = false
	w.models = msg.models
	if len(w.models) == 0 {
		w.startTyping()
	}
}

func (w *wizModel) startTyping() {
	w.typing = true
	w.customInput.SetValue("")
	if w.harness == profile.HarnessCustom {
		w.customInput.Placeholder = "argv, e.g. mycli --model {{model}} run  (prompt on stdin, or use {{prompt}})"
	} else {
		w.customInput.Placeholder = "model id, e.g. zai/glm-5.2 (empty = harness default)"
	}
	w.customInput.Focus()
}

// update returns (closed, saved). saved is non-nil when the user finished.
func (w *wizModel) update(msg tea.KeyMsg, store *profile.Store) (bool, *profile.Profile, tea.Cmd) {
	key := msg.String()

	// Global back navigation.
	if key == "esc" {
		if w.typing && len(w.models) > 0 {
			w.typing = false
			return false, nil, nil
		}
		if w.step == stepHarness {
			return true, nil, nil
		}
		w.back()
		return false, nil, nil
	}

	switch w.step {
	case stepHarness:
		opts := profile.Harnesses
		switch key {
		case "up", "k":
			w.sel = clamp(w.sel-1, 0, len(opts)-1)
		case "down", "j":
			w.sel = clamp(w.sel+1, 0, len(opts)-1)
		case "enter":
			w.harness = opts[w.sel]
			w.step = stepModel
			w.sel = 0
			w.typing = false
			if w.harness == profile.HarnessMock {
				w.model = ""
				w.advanceFromModel(nil)
				return false, nil, nil
			}
			if w.harness == profile.HarnessCustom {
				w.models = nil
				w.startTyping()
				return false, nil, nil
			}
			w.loading = true
			return false, nil, fetchModelsCmd(w.harness)
		}

	case stepModel:
		if w.typing {
			if key == "enter" {
				v := strings.TrimSpace(w.customInput.Value())
				if w.harness == profile.HarnessCustom {
					if v == "" {
						w.errMsg = "custom harness needs a command"
						return false, nil, nil
					}
					w.command = strings.Fields(v)
					w.model = ""
				} else {
					w.model = v
				}
				w.errMsg = ""
				// Hand-typed model: fall back to the harness-generic efforts.
				w.advanceFromModel(detect.Efforts(w.harness))
				return false, nil, nil
			}
			var cmd tea.Cmd
			w.customInput, cmd = w.customInput.Update(msg)
			return false, nil, cmd
		}
		if w.loading {
			return false, nil, nil
		}
		last := len(w.models) // "type it yourself" row
		switch key {
		case "up", "k":
			w.sel = clamp(w.sel-1, 0, last)
		case "down", "j":
			w.sel = clamp(w.sel+1, 0, last)
		case "enter":
			if w.sel == last {
				w.startTyping()
				return false, nil, nil
			}
			w.model = w.models[w.sel].ID
			// Efforts come from the harness's own catalog entry.
			w.advanceFromModel(w.models[w.sel].Efforts)
		}

	case stepEffort:
		switch key {
		case "up", "k":
			w.sel = clamp(w.sel-1, 0, len(w.efforts)-1)
		case "down", "j":
			w.sel = clamp(w.sel+1, 0, len(w.efforts)-1)
		case "enter":
			w.effort = w.efforts[w.sel]
			w.step = stepStats
		}

	case stepStats:
		switch key {
		case "up", "k":
			w.statSel = clamp(w.statSel-1, 0, 2)
		case "down", "j":
			w.statSel = clamp(w.statSel+1, 0, 2)
		case "left", "h":
			w.stats[w.statSel] = clamp(w.stats[w.statSel]-1, 1, 10)
		case "right", "l":
			w.stats[w.statSel] = clamp(w.stats[w.statSel]+1, 1, 10)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			w.stats[w.statSel] = int(key[0] - '0')
		case "0":
			w.stats[w.statSel] = 10
		case "enter":
			w.step = stepDesc
			w.desc.Focus()
		}

	case stepDesc:
		if key == "enter" {
			w.desc.Blur()
			w.step = stepFinal
			w.finSel = 0
			if strings.TrimSpace(w.name.Value()) == "" {
				w.name.SetValue(detect.SuggestName(w.harness, w.model, w.effort))
			}
			w.focusFinal()
			return false, nil, nil
		}
		var cmd tea.Cmd
		w.desc, cmd = w.desc.Update(msg)
		return false, nil, cmd

	case stepFinal:
		const rows = 6 // name, limConc, limCalls, enabled, default, save
		switch key {
		case "up", "shift+tab":
			w.finSel = clamp(w.finSel-1, 0, rows-1)
			w.focusFinal()
			return false, nil, nil
		case "down", "tab":
			w.finSel = clamp(w.finSel+1, 0, rows-1)
			w.focusFinal()
			return false, nil, nil
		case "ctrl+s":
			return w.save(store)
		case "enter":
			if w.finSel == rows-1 {
				return w.save(store)
			}
			w.finSel = clamp(w.finSel+1, 0, rows-1)
			w.focusFinal()
			return false, nil, nil
		case "left", "right", " ":
			switch w.finSel {
			case 3:
				w.enabled = !w.enabled
				return false, nil, nil
			case 4:
				w.def = !w.def
				return false, nil, nil
			}
		}
		var cmd tea.Cmd
		switch w.finSel {
		case 0:
			w.name, cmd = w.name.Update(msg)
		case 1:
			w.limConc, cmd = w.limConc.Update(msg)
		case 2:
			w.limCalls, cmd = w.limCalls.Update(msg)
		}
		return false, nil, cmd
	}
	return false, nil, nil
}

func (w *wizModel) advanceFromModel(efforts []string) {
	w.efforts = append([]string{"default"}, efforts...)
	w.hadEffort = len(efforts) > 0
	if !w.hadEffort {
		w.effort = "default"
		w.step = stepStats
		return
	}
	w.step = stepEffort
	w.sel = 0
}

func (w *wizModel) back() {
	switch w.step {
	case stepModel:
		w.step = stepHarness
		w.sel = 0
	case stepEffort:
		w.step = stepModel
		w.sel = 0
	case stepStats:
		if w.hadEffort {
			w.step = stepEffort
		} else {
			w.step = stepModel
		}
		w.sel = 0
	case stepDesc:
		w.desc.Blur()
		w.step = stepStats
	case stepFinal:
		w.step = stepDesc
		w.desc.Focus()
	}
	w.errMsg = ""
}

func (w *wizModel) focusFinal() {
	w.name.Blur()
	w.limConc.Blur()
	w.limCalls.Blur()
	switch w.finSel {
	case 0:
		w.name.Focus()
	case 1:
		w.limConc.Focus()
	case 2:
		w.limCalls.Focus()
	}
}

func (w *wizModel) save(store *profile.Store) (bool, *profile.Profile, tea.Cmd) {
	extraArgs, env := detect.ApplyEffort(w.harness, w.effort)
	p := profile.Profile{
		Name:           strings.TrimSpace(w.name.Value()),
		Description:    strings.TrimSpace(w.desc.Value()),
		Harness:        w.harness,
		Model:          w.model,
		Command:        w.command,
		ExtraArgs:      extraArgs,
		Env:            env,
		Taste:          w.stats[0],
		Intelligence:   w.stats[1],
		Cost:           w.stats[2],
		MaxConcurrent:  atoiOr0(w.limConc.Value()),
		MaxCallsPerRun: atoiOr0(w.limCalls.Value()),
		Disabled:       !w.enabled,
		Default:        w.def,
	}
	if err := store.Upsert(p); err != nil {
		w.errMsg = err.Error()
		return false, nil, nil
	}
	if err := store.Save(); err != nil {
		w.errMsg = err.Error()
		return false, nil, nil
	}
	return true, &p, nil
}

// ---- rendering ----

func (w *wizModel) view(width, height int) string {
	var b strings.Builder
	dots := ""
	for i := 0; i < wizSteps; i++ {
		if wizStep(i) == w.step {
			dots += sHelpKey.Render("●") + " "
		} else if wizStep(i) < w.step {
			dots += sOK.Render("●") + " "
		} else {
			dots += sDim.Render("○") + " "
		}
	}
	b.WriteString(sTitle.Render("Profile wizard") + sDim.Render(fmt.Sprintf(" · step %d/%d — ", int(w.step)+1, wizSteps)) + sPhase.Render(wizTitles[w.step]) + "\n")
	b.WriteString(dots + "\n\n")

	choice := func(i, sel int, label, detail string) {
		cursor := "  "
		name := label
		if i == sel {
			cursor = sHelpKey.Render("▸ ")
			name = sSel.Render(label)
		}
		line := cursor + name
		if detail != "" {
			line += "  " + sDim.Render(detail)
		}
		b.WriteString(line + "\n")
	}

	switch w.step {
	case stepHarness:
		b.WriteString("Which application runs this worker?\n\n")
		for i, h := range profile.Harnesses {
			detail := ""
			if !detect.Installed(h) {
				detail = "not found on PATH"
			}
			choice(i, w.sel, h, detail)
		}

	case stepModel:
		if w.loading {
			b.WriteString(sDim.Render("probing " + w.harness + " for models…"))
			break
		}
		if w.typing {
			label := "Type the model id:"
			if w.harness == profile.HarnessCustom {
				label = "Type the command that runs one worker turn:"
			}
			b.WriteString(label + "\n\n  " + w.customInput.View() + "\n")
			break
		}
		b.WriteString("Which model? (reported by the " + w.harness + " CLI)\n\n")
		for i, m := range w.models {
			detail := m.Description
			if len(detail) > 60 {
				detail = detail[:60] + "…"
			}
			choice(i, w.sel, m.ID, detail)
		}
		choice(len(w.models), w.sel, "type it yourself…", "custom / niche model id")

	case stepEffort:
		b.WriteString("Reasoning effort for " + orDefault(w.model, w.harness) + "?\n\n")
		for i, e := range w.efforts {
			detail := ""
			if e == "default" {
				detail = "leave the harness setting alone"
			}
			choice(i, w.sel, e, detail)
		}

	case stepStats:
		b.WriteString("Rate this worker (1–10, higher is better; ←/→ or 1-9/0):\n\n")
		labels := []string{"taste", "intelligence", "cost-eff."}
		notes := []string{"quality · design · review judgment", "hard, long, complex tasks", "higher = cheaper to run"}
		cs := []lipgloss.AdaptiveColor{cTaste, cIntel, cCost}
		for i := range labels {
			cursor := "  "
			lab := sDim.Render(fmt.Sprintf("%-13s", labels[i]))
			if i == w.statSel {
				cursor = sHelpKey.Render("▸ ")
				lab = sTitle.Render(fmt.Sprintf("%-13s", labels[i]))
			}
			b.WriteString(fmt.Sprintf("%s%s %s  %2d/10  %s\n", cursor, lab, statBar(w.stats[i], cs[i]), w.stats[i], sDim.Render(notes[i])))
		}

	case stepDesc:
		b.WriteString("Describe this worker — agents read this to decide when to use it:\n\n  " + w.desc.View() + "\n")

	case stepFinal:
		b.WriteString("Last details:\n\n")
		row := func(i int, label, val string) {
			cursor := "  "
			lab := sDim.Render(fmt.Sprintf("%-13s", label))
			if i == w.finSel {
				cursor = sHelpKey.Render("▸ ")
				lab = sTitle.Render(fmt.Sprintf("%-13s", label))
			}
			b.WriteString(cursor + lab + " " + val + "\n")
		}
		row(0, "name", w.name.View())
		row(1, "limit conc.", w.limConc.View())
		row(2, "limit calls", w.limCalls.View())
		row(3, "enabled", "◂ "+sProfTag.Render(yesNo(w.enabled))+" ▸")
		row(4, "default", "◂ "+sProfTag.Render(yesNo(w.def))+" ▸")
		save := "  Save profile"
		if w.finSel == 5 {
			save = sHelpKey.Render("▸ ") + sBadge.Render(" Save profile ")
		}
		b.WriteString("\n" + save + "\n")
		b.WriteString("\n" + sDim.Render(w.summary()))
	}

	if w.errMsg != "" {
		b.WriteString("\n" + sErrS.Render("✗ "+w.errMsg))
	}
	return sPaneR.Width(clamp(width-4, 40, 100)).Render(b.String())
}

// summary shows what the wizard will save.
func (w *wizModel) summary() string {
	parts := []string{w.harness}
	if w.model != "" {
		parts = append(parts, w.model)
	}
	if len(w.command) > 0 {
		parts = append(parts, strings.Join(w.command, " "))
	}
	if w.effort != "" && w.effort != "default" {
		parts = append(parts, "effort:"+w.effort)
	}
	parts = append(parts, fmt.Sprintf("T%d/I%d/C%d", w.stats[0], w.stats[1], w.stats[2]))
	return strings.Join(parts, " · ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
