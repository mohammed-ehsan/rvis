package ui

import (
	"os"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/model"
)

func demoState(t *testing.T) *State {
	t.Helper()
	data, err := os.ReadFile("../../testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := model.Parse(data, model.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &State{Report: rep, Source: "testdata/demo-chart"}
}

func press(m *tuiModel, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "f5":
			msg = tea.KeyMsg{Type: tea.KeyF5}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

// Every frame must exactly fill the terminal: no line wider than the
// window and exactly height lines, at a range of sizes and in every mode.
func TestFrameFitsTerminal(t *testing.T) {
	sizes := [][2]int{{40, 12}, {80, 24}, {100, 30}, {132, 43}, {200, 60}}
	scenarios := map[string][]string{
		"table":    nil,
		"flat":     {"f5"},
		"perpod":   {"r"},
		"detail":   {"down", "enter"},
		"help":     {"?"},
		"warnings": {"w"},
		"filter":   {"/", "a", "p"},
		"nomatch":  {"/", "z", "z", "z", "enter"},
	}
	for _, sz := range sizes {
		for name, keys := range scenarios {
			m := &tuiModel{st: demoState(t)}
			m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			press(m, keys...)
			frame := m.View()
			lines := strings.Split(frame, "\n")
			if len(lines) != sz[1] {
				t.Errorf("%s %dx%d: %d lines, want %d", name, sz[0], sz[1], len(lines), sz[1])
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > sz[0] {
					t.Errorf("%s %dx%d: line %d is %d wide", name, sz[0], sz[1], i, w)
				}
			}
			if os.Getenv("RVIS_DUMP") == name+"-"+strconv.Itoa(sz[0]) {
				os.WriteFile(os.Getenv("RVIS_DUMP_FILE"), []byte(frame), 0o644)
			}
		}
	}
}
