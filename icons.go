package main

import "github.com/vivaui/viva/gfx"

// The app's glyphs: small Lucide-style stroke icons, hand-compiled into
// gfx.Icon path commands. ui.Icon tints them with the foreground color, so
// one glyph works on both themes.

// strokeGlyph wraps path commands in the icon style the app uses: a 24-unit
// view box, a 2-unit stroke and round caps and joins.
func strokeGlyph(commands ...gfx.PathCommand) gfx.Icon {
	return gfx.Icon{
		ViewBox:  24,
		Stroke:   2,
		Cap:      gfx.CapRound,
		Join:     gfx.JoinRound,
		Commands: commands,
	}
}

// circle is a stroked circle as four cubic Béziers.
func circle(cx, cy, r float32) []gfx.PathCommand {
	const k = 0.55228
	return []gfx.PathCommand{
		gfx.MoveTo(cx, cy-r),
		gfx.CubicTo(cx+k*r, cy-r, cx+r, cy-k*r, cx+r, cy),
		gfx.CubicTo(cx+r, cy+k*r, cx+k*r, cy+r, cx, cy+r),
		gfx.CubicTo(cx-k*r, cy+r, cx-r, cy+k*r, cx-r, cy),
		gfx.CubicTo(cx-r, cy-k*r, cx-k*r, cy-r, cx, cy-r),
	}
}

var (
	icoPlus = strokeGlyph(
		gfx.MoveTo(12, 5), gfx.LineTo(12, 19),
		gfx.MoveTo(5, 12), gfx.LineTo(19, 12),
	)
	icoTrash = strokeGlyph(
		gfx.MoveTo(4, 7), gfx.LineTo(20, 7),
		gfx.MoveTo(9, 7), gfx.LineTo(9, 4), gfx.LineTo(15, 4), gfx.LineTo(15, 7),
		gfx.MoveTo(6, 7), gfx.LineTo(7, 20), gfx.LineTo(17, 20), gfx.LineTo(18, 7),
		gfx.MoveTo(10, 11), gfx.LineTo(10, 16),
		gfx.MoveTo(14, 11), gfx.LineTo(14, 16),
	)
	icoPlay = strokeGlyph(
		gfx.MoveTo(7, 5), gfx.LineTo(19, 12), gfx.LineTo(7, 19), gfx.ClosePath(),
	)
	icoCopy = strokeGlyph(
		gfx.MoveTo(9, 9), gfx.LineTo(19, 9), gfx.LineTo(19, 19), gfx.LineTo(9, 19), gfx.ClosePath(),
		gfx.MoveTo(5, 15), gfx.LineTo(5, 5), gfx.LineTo(15, 5),
	)
	icoClock = strokeGlyph(append(
		circle(12, 12, 9),
		gfx.MoveTo(12, 7), gfx.LineTo(12, 12), gfx.LineTo(15, 14),
	)...)
	icoFolder = strokeGlyph(
		gfx.MoveTo(3, 6), gfx.LineTo(9, 6), gfx.LineTo(11, 8), gfx.LineTo(21, 8),
		gfx.LineTo(21, 18), gfx.LineTo(3, 18), gfx.ClosePath(),
	)
	icoSend = strokeGlyph(
		gfx.MoveTo(22, 2), gfx.LineTo(11, 13),
		gfx.MoveTo(22, 2), gfx.LineTo(15, 22), gfx.LineTo(11, 13), gfx.LineTo(2, 9), gfx.ClosePath(),
	)
	icoPencil = strokeGlyph(
		gfx.MoveTo(17, 3), gfx.LineTo(21, 7), gfx.LineTo(8, 20), gfx.LineTo(4, 16), gfx.ClosePath(),
		gfx.MoveTo(15, 5), gfx.LineTo(19, 9),
	)
	icoMore = func() gfx.Icon {
		cmds := circle(5, 12, 1.4)
		cmds = append(cmds, circle(12, 12, 1.4)...)
		cmds = append(cmds, circle(19, 12, 1.4)...)
		return strokeGlyph(cmds...)
	}()
	icoSearch = strokeGlyph(append(
		circle(11, 11, 7),
		gfx.MoveTo(16.5, 16.5), gfx.LineTo(21, 21),
	)...)
	icoDownload = strokeGlyph(
		gfx.MoveTo(12, 3), gfx.LineTo(12, 15),
		gfx.MoveTo(7, 10), gfx.LineTo(12, 15), gfx.LineTo(17, 10),
		gfx.MoveTo(4, 17), gfx.LineTo(4, 21), gfx.LineTo(20, 21), gfx.LineTo(20, 17),
	)
	icoUpload = strokeGlyph(
		gfx.MoveTo(12, 15), gfx.LineTo(12, 3),
		gfx.MoveTo(7, 8), gfx.LineTo(12, 3), gfx.LineTo(17, 8),
		gfx.MoveTo(4, 17), gfx.LineTo(4, 21), gfx.LineTo(20, 21), gfx.LineTo(20, 17),
	)
	icoArchive = strokeGlyph(
		gfx.MoveTo(3, 7), gfx.LineTo(21, 7), gfx.LineTo(21, 21), gfx.LineTo(3, 21), gfx.ClosePath(),
		gfx.MoveTo(3, 3), gfx.LineTo(21, 3), gfx.LineTo(21, 7), gfx.LineTo(3, 7), gfx.ClosePath(),
	)
	icoClose = strokeGlyph(
		gfx.MoveTo(6, 6), gfx.LineTo(18, 18),
		gfx.MoveTo(18, 6), gfx.LineTo(6, 18),
	)
	icoBranch = func() gfx.Icon {
		cmds := circle(6, 6, 2.4)
		cmds = append(cmds, circle(6, 18, 2.4)...)
		cmds = append(cmds, circle(18, 6, 2.4)...)
		cmds = append(cmds,
			gfx.MoveTo(6, 8.4), gfx.LineTo(6, 15.6),
			gfx.MoveTo(6, 10), gfx.CubicTo(10.5, 10, 13, 8.4, 15.6, 8.4))
		return strokeGlyph(cmds...)
	}()
	icoLock = strokeGlyph(append(
		circle(12, 8, 3),
		gfx.MoveTo(6, 11), gfx.LineTo(18, 11), gfx.LineTo(18, 21), gfx.LineTo(6, 21), gfx.ClosePath(),
	)...)
)

// icoGithub is the octocat, compiled from Lucide's github.svg by
// tools/genicon.
var icoGithub = gfx.Icon{
	ViewBox: 24.0,
	Stroke:  2.0,
	Cap:     gfx.CapRound,
	Join:    gfx.JoinRound,
	Commands: []gfx.PathCommand{
		gfx.MoveTo(15.0, 22.0),
		gfx.LineTo(15.0, 18.0),
		gfx.CubicTo(15.1391, 16.7473, 14.7799, 15.4901, 14.0, 14.5),
		gfx.CubicTo(17.0, 14.5, 20.0, 12.5, 20.0, 9.0),
		gfx.CubicTo(20.08, 7.75, 19.73, 6.52, 19.0, 5.5),
		gfx.CubicTo(19.28, 4.35, 19.28, 3.15, 19.0, 2.0),
		gfx.CubicTo(19.0, 2.0, 17.03, 1.81, 14.94, 3.31),
		gfx.CubicTo(13.96, 2.95, 12.93, 2.77, 11.9, 2.77),
		gfx.CubicTo(10.87, 2.77, 9.84, 2.95, 8.86, 3.31),
		gfx.CubicTo(7.77, 3.31, 5.8, 3.5, 5.8, 3.5),
		gfx.CubicTo(5.52, 4.65, 5.52, 5.85, 5.8, 7.0),
		gfx.CubicTo(5.07, 8.02, 4.82, 9.25, 4.8, 10.5),
		gfx.CubicTo(4.8, 14.0, 7.8, 16.0, 10.8, 16.0),
		gfx.CubicTo(10.41, 16.49, 10.12, 17.05, 9.95, 17.65),
		gfx.CubicTo(9.78, 18.25, 9.73, 18.88, 9.8, 19.5),
		gfx.LineTo(9.8, 23.5),
		gfx.MoveTo(9.0, 18.0),
		gfx.CubicTo(4.49, 20.0, 4.0, 16.0, 2.0, 16.0),
	},
}
