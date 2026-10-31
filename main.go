// Command rvis is a Helm plugin that visualizes the resource requests and
// limits of a chart.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"

	"github.com/mohammed-ehsan/rvis/cmd"
)

func main() {
	if err := cmd.New().Execute(); err != nil {
		if errors.Is(err, cmd.ErrFindings) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true).Render("Error:"), err)
		os.Exit(1)
	}
}
