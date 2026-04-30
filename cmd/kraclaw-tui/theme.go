// Package main provides the kraclaw TUI client theme system.
// Palette definitions, style vars, and theme persistence live here.
package main

import (
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"

	"charm.land/lipgloss/v2"
)

// Palette holds every named color the TUI uses. Two palettes are built at
// init time and swapped via setTheme.
type Palette struct {
	Name string

	Bg        color.Color
	BgPanel   color.Color
	Fg        color.Color
	FgDim     color.Color
	Muted     color.Color
	MutedSoft color.Color

	Kraken      color.Color
	KrakenLight color.Color

	Coral    color.Color
	CoralDim color.Color

	Ok   color.Color
	Warn color.Color
	Err  color.Color

	SelBg       color.Color
	SelStrongBg color.Color
	SelStrongFg color.Color
	InverseBg   color.Color
	InverseFg   color.Color
}

var (
	paletteDark = Palette{
		Name:        "dark",
		Bg:          lipgloss.Color("#0B1220"),
		BgPanel:     lipgloss.Color("#111A2E"),
		Fg:          lipgloss.Color("#E8EDF5"),
		FgDim:       lipgloss.Color("#A8B3C7"),
		Muted:       lipgloss.Color("#6B7A99"),
		MutedSoft:   lipgloss.Color("#8B9BB8"),
		Kraken:      lipgloss.Color("#2F4A7A"),
		KrakenLight: lipgloss.Color("#89B0D9"),
		Coral:       lipgloss.Color("#E8704A"),
		CoralDim:    lipgloss.Color("#B8543A"),
		Ok:          lipgloss.Color("#7FB685"),
		Warn:        lipgloss.Color("#E8B84A"),
		Err:         lipgloss.Color("#E06C5C"),
		SelBg:       lipgloss.Color("#3A2218"),
		SelStrongBg: lipgloss.Color("#E8704A"),
		SelStrongFg: lipgloss.Color("#0B1220"),
		InverseBg:   lipgloss.Color("#E8EDF5"),
		InverseFg:   lipgloss.Color("#0B1220"),
	}

	paletteLight = Palette{
		Name:        "light",
		Bg:          lipgloss.Color("#EAF1F8"),
		BgPanel:     lipgloss.Color("#D7E3F0"),
		Fg:          lipgloss.Color("#1F3558"),
		FgDim:       lipgloss.Color("#3B527A"),
		Muted:       lipgloss.Color("#7B8DA8"),
		MutedSoft:   lipgloss.Color("#5A6F8E"),
		Kraken:      lipgloss.Color("#2F4A7A"),
		KrakenLight: lipgloss.Color("#4A6FA5"),
		Coral:       lipgloss.Color("#B8543A"),
		CoralDim:    lipgloss.Color("#B8543A"),
		Ok:          lipgloss.Color("#4E8E5A"),
		Warn:        lipgloss.Color("#B48A2E"),
		Err:         lipgloss.Color("#B94234"),
		SelBg:       lipgloss.Color("#DCE5F0"),
		SelStrongBg: lipgloss.Color("#2F4A7A"),
		SelStrongFg: lipgloss.Color("#EAF1F8"),
		InverseBg:   lipgloss.Color("#1F3558"),
		InverseFg:   lipgloss.Color("#EAF1F8"),
	}

	activePalette = &paletteDark
)

// Package-level style vars. All colors reference activePalette and are
// rebuilt on every theme swap.
var (
	activeTabStyle      lipgloss.Style
	inactiveTabStyle    lipgloss.Style
	activeTabNumStyle   lipgloss.Style
	inactiveTabNumStyle lipgloss.Style

	sectionRuleStyle   lipgloss.Style
	statusLineStyle    lipgloss.Style
	statusLineSepStyle lipgloss.Style
	keyHintKeyStyle    lipgloss.Style
	keyHintLabelStyle  lipgloss.Style

	selStrongStyle lipgloss.Style

	inputBoxStyle       lipgloss.Style
	composerPromptStyle lipgloss.Style
	composerMetaStyle   lipgloss.Style

	userLabelStyle   lipgloss.Style
	agentLabelStyle  lipgloss.Style
	systemLabelStyle lipgloss.Style

	okStyle    lipgloss.Style
	errStyle   lipgloss.Style
	warnStyle  lipgloss.Style
	dimStyle   lipgloss.Style
	fgStyle    lipgloss.Style
	krakenStyle lipgloss.Style
	coralStyle lipgloss.Style
	coralBold  lipgloss.Style

	bigMetricValueStyle lipgloss.Style
	bigMetricUnitStyle  lipgloss.Style

	spinnerStyle     lipgloss.Style
	stubMessageStyle lipgloss.Style
	groupHeaderStyle lipgloss.Style
)

func init() {
	loadInitialTheme()
	rebuildStyles()
}

// loadInitialTheme resolves the active palette from the saved config,
// then the KRACLAW_TUI_THEME env var, then falls back to dark.
func loadInitialTheme() {
	name := loadSavedThemeName()
	if name == "" {
		name = os.Getenv("KRACLAW_TUI_THEME")
	}
	applyThemeName(name)
}

func applyThemeName(name string) {
	switch name {
	case "light":
		activePalette = &paletteLight
	default:
		activePalette = &paletteDark
	}
}

// setTheme swaps the active palette and rebuilds all style vars.
func setTheme(name string) {
	applyThemeName(name)
	rebuildStyles()
}

// rebuildStyles reconstructs every package-level lipgloss.Style from the
// current activePalette. Called by setTheme and at init.
func rebuildStyles() {
	p := activePalette

	activeTabStyle = lipgloss.NewStyle().Bold(true).Foreground(p.Fg).Background(p.BgPanel).Padding(0, 2)
	inactiveTabStyle = lipgloss.NewStyle().Foreground(p.FgDim).Padding(0, 2)
	activeTabNumStyle = lipgloss.NewStyle().Bold(true).Foreground(p.Coral).Background(p.BgPanel)
	inactiveTabNumStyle = lipgloss.NewStyle().Foreground(p.Coral)

	sectionRuleStyle = lipgloss.NewStyle().Foreground(p.Muted)
	statusLineStyle = lipgloss.NewStyle().Foreground(p.InverseFg).Background(p.InverseBg)
	statusLineSepStyle = lipgloss.NewStyle().Foreground(p.MutedSoft).Background(p.InverseBg)
	keyHintKeyStyle = lipgloss.NewStyle().Foreground(p.Coral)
	keyHintLabelStyle = lipgloss.NewStyle().Foreground(p.Muted)

	selStrongStyle = lipgloss.NewStyle().Foreground(p.SelStrongFg).Background(p.SelStrongBg)

	inputBoxStyle = lipgloss.NewStyle().Padding(0, 1)
	composerPromptStyle = lipgloss.NewStyle().Foreground(p.Coral).Bold(true)
	composerMetaStyle = lipgloss.NewStyle().Foreground(p.Muted)

	userLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(p.KrakenLight)
	agentLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(p.Coral)
	systemLabelStyle = lipgloss.NewStyle().Foreground(p.Muted)

	okStyle = lipgloss.NewStyle().Foreground(p.Ok)
	errStyle = lipgloss.NewStyle().Foreground(p.Err)
	warnStyle = lipgloss.NewStyle().Foreground(p.Warn)
	dimStyle = lipgloss.NewStyle().Foreground(p.Muted)
	fgStyle = lipgloss.NewStyle().Foreground(p.Fg)
	krakenStyle = lipgloss.NewStyle().Foreground(p.KrakenLight)
	coralStyle = lipgloss.NewStyle().Foreground(p.Coral)
	coralBold = lipgloss.NewStyle().Foreground(p.Coral).Bold(true)

	bigMetricValueStyle = lipgloss.NewStyle().Bold(true).Foreground(p.Fg)
	bigMetricUnitStyle = lipgloss.NewStyle().Foreground(p.Muted)

	spinnerStyle = lipgloss.NewStyle().Foreground(p.Coral)
	stubMessageStyle = lipgloss.NewStyle().Foreground(p.Coral)
	groupHeaderStyle = lipgloss.NewStyle().Foreground(p.FgDim)
}

// configPath returns the path to the persisted TUI config file, honoring
// XDG_CONFIG_HOME when set.
func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "kraclaw", "tui.json"), nil
}

type tuiConfig struct {
	Theme string `json:"theme"`
}

// loadSavedThemeName returns the persisted theme name, or "" on any error.
func loadSavedThemeName() string {
	path, err := configPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg tuiConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return ""
	}
	return cfg.Theme
}

// persistTheme writes the chosen theme name to the user config file.
func persistTheme(name string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(tuiConfig{Theme: name})
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
