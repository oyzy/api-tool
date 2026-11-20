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
	icoLock = strokeGlyph(append(
		circle(12, 8, 3),
		gfx.MoveTo(6, 11), gfx.LineTo(18, 11), gfx.LineTo(18, 21), gfx.LineTo(6, 21), gfx.ClosePath(),
	)...)
)
