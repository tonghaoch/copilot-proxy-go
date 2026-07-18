package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const selectHeight = 15

type selectPalette struct {
	border lipgloss.Color
	accent lipgloss.Color
}

var (
	palettePrimary = selectPalette{border: lipgloss.Color("63"), accent: lipgloss.Color("12")}
	paletteSmall   = selectPalette{border: lipgloss.Color("36"), accent: lipgloss.Color("42")}
)

type selectModel struct {
	title   string
	items   []string
	cursor  int
	offset  int
	result  string
	done    bool
	aborted bool
	palette selectPalette
}

func runSelect(title string, items []string, palette selectPalette) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("no items available")
	}
	m := selectModel{title: title, items: items, palette: palette}
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	fm := final.(selectModel)
	if fm.aborted {
		return "", fmt.Errorf("aborted")
	}
	return fm.result, nil
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.aborted = true
			return m, tea.Quit
		case "enter":
			m.result = m.items[m.cursor]
			m.done = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				if m.cursor >= m.offset+selectHeight {
					m.offset = m.cursor - selectHeight + 1
				}
			}
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	if m.done {
		return ""
	}
	borderStyle := lipgloss.NewStyle().Foreground(m.palette.border)
	thumbStyle := lipgloss.NewStyle().Foreground(m.palette.accent)
	cursorStyle := lipgloss.NewStyle().Foreground(m.palette.accent)
	selectedStyle := lipgloss.NewStyle().Foreground(m.palette.accent)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	visible := min(selectHeight, len(m.items))
	maxW := 0
	for _, item := range m.items {
		maxW = max(maxW, len(item))
	}
	contentW := 2 + maxW
	needScroll := len(m.items) > visible
	var thumbStart, thumbEnd int
	if needScroll {
		thumbH := max(1, visible*visible/len(m.items))
		thumbPos := (m.offset * (visible - thumbH)) / max(1, len(m.items)-visible)
		thumbStart, thumbEnd = thumbPos, thumbPos+thumbH
	}

	title := " " + m.title + " "
	titleRendered := lipgloss.NewStyle().Bold(true).Foreground(m.palette.accent).Render(title)
	remainW := max(0, contentW+1-lipgloss.Width(title))
	topBorder := borderStyle.Render("╭─") + titleRendered + borderStyle.Render(strings.Repeat("─", remainW)+"╮")

	lines := make([]string, 0, visible)
	for i := 0; i < visible; i++ {
		idx := m.offset + i
		label := m.items[idx]
		padded := label + strings.Repeat(" ", maxW-len(label))
		inner := "  " + normalStyle.Render(padded)
		if idx == m.cursor {
			inner = cursorStyle.Render("> ") + selectedStyle.Render(padded)
		}
		rightBorder := borderStyle.Render("│")
		if needScroll && i >= thumbStart && i < thumbEnd {
			rightBorder = thumbStyle.Render("█")
		}
		lines = append(lines, borderStyle.Render("│")+" "+inner+" "+rightBorder)
	}
	botBorder := borderStyle.Render("╰" + strings.Repeat("─", contentW+2) + "╯")
	return topBorder + "\n" + strings.Join(lines, "\n") + "\n" + botBorder
}
