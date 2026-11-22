package main

import "github.com/egoist/mygo/ui"

// Match the full footprint of the three swipe buttons, including their gaps.
func ruleRemoveWidth() float32 { return 3*tw(7) + 2*tw(1.5) }

func ruleRemoveButton(c *ui.Context, p palette, key string, height float32, busy bool) bool {
	b := ui.ButtonBase(c.Key(key)).Label(tr("Remove")).Size(ruleRemoveWidth(), height).
		Radius(6).Gap(tw(1.5)).Center().Shrink(0).Cursor(ui.CursorPointer).Disabled(busy)
	bg := p.errorSurface
	if b.Hovered() && !busy {
		bg = p.cardActive
	}
	b.Background(bg).Border(1, p.errorBorder)
	b.Children(func() {
		ui.Icon(c, iconTrash).FontSize(13).TextColor(p.errorC)
		label(c, tr("Remove"), sizeSubhead, 14, 500, p.errorC)
	})
	return b.Clicked()
}
