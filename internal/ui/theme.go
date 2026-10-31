package ui

import "github.com/charmbracelet/lipgloss"

// An htop-inspired palette built on the 16 ANSI colors so it follows the
// user's terminal theme.
var (
	cGreen   = lipgloss.Color("2")
	cYellow  = lipgloss.Color("3")
	cRed     = lipgloss.Color("1")
	cBlue    = lipgloss.Color("4")
	cMagenta = lipgloss.Color("5")
	cCyan    = lipgloss.Color("6")
	cGray    = lipgloss.Color("8")
	cBlack   = lipgloss.Color("0")
	cWhite   = lipgloss.Color("15")

	stBase      = lipgloss.NewStyle()
	stBold      = lipgloss.NewStyle().Bold(true)
	stDim       = lipgloss.NewStyle().Foreground(cGray)
	stLabel     = lipgloss.NewStyle().Foreground(cCyan)
	stLabelBold = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	stBracket   = lipgloss.NewStyle().Bold(true)
	stReq       = lipgloss.NewStyle().Foreground(cGreen)
	stLim       = lipgloss.NewStyle().Foreground(cYellow)
	stOver      = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stMeterText = lipgloss.NewStyle().Foreground(cGray)

	stMissing   = lipgloss.NewStyle().Foreground(cYellow)
	stUnbounded = lipgloss.NewStyle().Foreground(cRed)
	stPartial   = lipgloss.NewStyle().Foreground(cYellow)

	stKind     = lipgloss.NewStyle().Foreground(cCyan)
	stWorkload = lipgloss.NewStyle().Bold(true)
	stTree     = lipgloss.NewStyle().Foreground(cGray)
	stRole     = lipgloss.NewStyle().Foreground(cMagenta)
	stPods     = lipgloss.NewStyle().Foreground(cBlue).Bold(true)

	stQoSGuaranteed = lipgloss.NewStyle().Foreground(cGreen)
	stQoSBurstable  = lipgloss.NewStyle().Foreground(cYellow)
	stQoSBestEffort = lipgloss.NewStyle().Foreground(cRed)

	stHeader       = lipgloss.NewStyle().Background(cGreen).Foreground(cBlack)
	stHeaderSorted = lipgloss.NewStyle().Background(cCyan).Foreground(cBlack).Bold(true)
	stSelected     = lipgloss.NewStyle().Background(cCyan).Foreground(cBlack)
	stTotal        = lipgloss.NewStyle().Bold(true)
	stRule         = lipgloss.NewStyle().Foreground(cGray)
	stChanged      = lipgloss.NewStyle().Foreground(cYellow).Bold(true)

	stFKey   = lipgloss.NewStyle().Foreground(cWhite).Bold(true)
	stFLabel = lipgloss.NewStyle().Background(cCyan).Foreground(cBlack)

	stErr  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stWarn = lipgloss.NewStyle().Foreground(cYellow)
	stInfo = lipgloss.NewStyle().Foreground(cBlue)

	stTitle = lipgloss.NewStyle().Bold(true).Foreground(cBlack).Background(cCyan).Padding(0, 1)
	stBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cCyan).Padding(0, 1)
)
