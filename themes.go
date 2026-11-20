package main

import (
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// The looks. Two stock themes plus tinted variants: every preset is a base
// theme with its surfaces and primary recolored, so spacing, radius and
// metrics stay Viva's and every panel of the app keeps working.

// tint lists the colours a preset recolors; a tinted theme keeps the base's
// success/danger, foregrounds of popovers and its metrics.
type tint struct {
	foreground        gfx.Color
	background        gfx.Color
	card              gfx.Color
	popover           gfx.Color
	muted             gfx.Color
	mutedForeground   gfx.Color
	border            gfx.Color
	hover             gfx.Color
	input             gfx.Color
	primary           gfx.Color
	primaryForeground gfx.Color
	ring              gfx.Color
	selection         gfx.Color
	secondary         gfx.Color
	accent            gfx.Color
}

func retint(base ui.Theme, t tint) ui.Theme {
	out := base
	out.Foreground, out.Background, out.Card, out.Popover = t.foreground, t.background, t.card, t.popover
	out.Muted, out.MutedForeground, out.Border, out.Hover, out.Input = t.muted, t.mutedForeground, t.border, t.hover, t.input
	out.Primary, out.PrimaryForeground, out.Ring, out.Selection = t.primary, t.primaryForeground, t.ring, t.selection
	out.Secondary, out.Accent, out.CardElevated = t.secondary, t.accent, t.card
	return out
}

// themePreset is one entry of the 主题 menu.
type themePreset struct {
	name  string
	build func() ui.Theme
}

var themePresets = []themePreset{
	{"浅色", func() ui.Theme { return ui.LightTheme() }},
	{"深色", func() ui.Theme { return ui.DarkTheme() }},
	{"暖纸", func() ui.Theme {
		// 纸墨: a warm reading light with a brown-orange primary
		return retint(ui.LightTheme(), tint{
			foreground:        gfx.RGB24(0x3d3629),
			background:        gfx.RGB24(0xf7f2e9),
			card:              gfx.RGB24(0xfffcf5),
			popover:           gfx.RGB24(0xfffcf5),
			muted:             gfx.RGB24(0xefe7d8),
			mutedForeground:   gfx.RGB24(0x8a7f6a),
			border:            gfx.RGB24(0xe0d5c0),
			hover:             gfx.RGB24(0xefe7d8),
			input:             gfx.RGB24(0xefe7d8),
			primary:           gfx.RGB24(0xb46b3d),
			primaryForeground: gfx.RGB24(0xffffff),
			ring:              gfx.RGB24(0xb46b3d),
			selection:         gfx.RGB24(0xe9d7b8),
			secondary:         gfx.RGB24(0xefe7d8),
			accent:            gfx.RGB24(0xe8dcc4),
		})
	}},
	{"深海", func() ui.Theme {
		// navy surfaces, a sky primary
		return retint(ui.DarkTheme(), tint{
			foreground:        gfx.RGB24(0xe2e8f0),
			background:        gfx.RGB24(0x0f172a),
			card:              gfx.RGB24(0x1e293b),
			popover:           gfx.RGB24(0x1e293b),
			muted:             gfx.RGB24(0x26334b),
			mutedForeground:   gfx.RGB24(0x94a3b8),
			border:            gfx.RGB24(0x2f3d55),
			hover:             gfx.RGB24(0x27334d),
			input:             gfx.RGB24(0x26334b),
			primary:           gfx.RGB24(0x38bdf8),
			primaryForeground: gfx.RGB24(0x0b1220),
			ring:              gfx.RGB24(0x38bdf8),
			selection:         gfx.RGB24(0x1d3a5f),
			secondary:         gfx.RGB24(0x26334b),
			accent:            gfx.RGB24(0x27334d),
		})
	}},
	{"墨绿", func() ui.Theme {
		// forest dark, an emerald primary
		return retint(ui.DarkTheme(), tint{
			foreground:        gfx.RGB24(0xe4efe9),
			background:        gfx.RGB24(0x0e1b16),
			card:              gfx.RGB24(0x152a22),
			popover:           gfx.RGB24(0x152a22),
			muted:             gfx.RGB24(0x1c352b),
			mutedForeground:   gfx.RGB24(0x9db8a8),
			border:            gfx.RGB24(0x25443a),
			hover:             gfx.RGB24(0x1d3a2f),
			input:             gfx.RGB24(0x1c352b),
			primary:           gfx.RGB24(0x34d399),
			primaryForeground: gfx.RGB24(0x08150f),
			ring:              gfx.RGB24(0x34d399),
			selection:         gfx.RGB24(0x1b4034),
			secondary:         gfx.RGB24(0x1c352b),
			accent:            gfx.RGB24(0x1d3a2f),
		})
	}},
	{"暗紫", func() ui.Theme {
		// violet dark
		return retint(ui.DarkTheme(), tint{
			foreground:        gfx.RGB24(0xeae6f5),
			background:        gfx.RGB24(0x151320),
			card:              gfx.RGB24(0x1f1b2e),
			popover:           gfx.RGB24(0x1f1b2e),
			muted:             gfx.RGB24(0x27223a),
			mutedForeground:   gfx.RGB24(0xa99fc4),
			border:            gfx.RGB24(0x322b47),
			hover:             gfx.RGB24(0x282240),
			input:             gfx.RGB24(0x27223a),
			primary:           gfx.RGB24(0xa78bfa),
			primaryForeground: gfx.RGB24(0x120e1f),
			ring:              gfx.RGB24(0xa78bfa),
			selection:         gfx.RGB24(0x2f2750),
			secondary:         gfx.RGB24(0x27223a),
			accent:            gfx.RGB24(0x282240),
		})
	}},
}

// themeNames are the menu's labels.
func themeNames() []string {
	names := make([]string, len(themePresets))
	for i, p := range themePresets {
		names[i] = p.name
	}
	return names
}
