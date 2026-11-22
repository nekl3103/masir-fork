package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// ---- building blocks ----

// cardBox is the surface settings rows sit on.
func cardBox(c *ui.Context, p palette, fn func()) {
	card := ui.Column(c).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Clip().Shrink(0)
	if p.dark {
		card.Shadow(0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.2))
	} else {
		card.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.04))
	}
	card.Children(fn)
}

// cardRow is a row of a card: 10 and 12 of padding, the two ends apart.
func cardRow(c *ui.Context, key string, fn func()) {
	ui.Row(c.Key(key)).Padding(tw(2.5), tw(3)).Gap(tw(3)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0).Children(fn)
}

func cardDivider(c *ui.Context, p palette) {
	ui.Box(c).Height(0.5).Background(p.borderSubtle).Shrink(0)
}

// titled is a titled group, with an action at the end of the title.
func titled(c *ui.Context, p palette, title string, action func(), fn func()) {
	ui.Column(c.Key("section-" + title)).Shrink(0).Children(func() {
		ui.Row(c).Padding(0, tw(1)).Margin(0, 0, tw(1.5)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			label(c, title, sizeCaption, 14, 600, p.muted).LetterSpacing(0.5)
			if action != nil {
				action()
			}
		})
		fn()
	})
}

// pageInset is shared by each page's top inset and the header's bottom inset.
// Keep content containers flush with the header so these spaces remain equal.
const pageInset float32 = 12

// pageHeader is the back arrow and the centered title of a page.
func (a *app) pageHeader(c *ui.Context, p palette, title string, actions ...func()) {
	actionGap := tw(2)
	sideWidth := max(tw(7), float32(len(actions))*(tw(6)+actionGap)-actionGap)
	ui.Row(c).Padding(0, 0, pageInset).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Row(c).Width(sideWidth).Shrink(0).Children(func() {
			if iconButton(c, "back", iconBack, tr("Back"), p, iconSM, variantGhost, 16) {
				a.goBack()
			}
		})
		label(c, title, sizeTitle, 18, 600, p.text).TextAlign(ui.Center).Grow(1).SingleLine()
		ui.Row(c).Width(sideWidth).Shrink(0).Gap(actionGap).Children(func() {
			for _, action := range actions {
				action()
			}
		})
	})
}

// button is the button of the design: primary is the blue one, full width.
func button(c *ui.Context, key, text string, primary, disabled, loading bool, p palette, cornerRadius ...float32) bool {
	radius := tw(2)
	if len(cornerRadius) > 0 {
		radius = cornerRadius[0]
	}
	b := ui.ButtonBase(c.Key(key)).Height(36).Padding(0, tw(3.5)).Radius(radius).Gap(tw(1.5)).Center().Shrink(0).
		Cursor(ui.CursorPointer)
	bg, fg := p.cardHover, p.text
	if primary {
		bg, fg = p.accent, ui.Hex("#ffffff")
		if b.Hovered() && !disabled {
			bg = p.accentHover
		}
		b.Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.05))
	} else if b.Hovered() {
		bg = p.cardActive
	}
	tint(b, bg)
	if disabled || loading {
		b.Opacity(0.5)
	}
	b.Children(func() {
		if loading {
			ui.Spinner(c).Size(14, 14)
		}
		label(c, text, sizeBody, 18, 500, fg)
	})
	return b.Clicked() && !disabled && !loading
}

// thinButton is a card's action across its width, as tall as a field or a select.
func thinButton(c *ui.Context, key, text string, primary, disabled, loading bool, p palette) bool {
	b := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, tw(2.5)).Radius(tw(1.5)).Gap(tw(1.5)).
		Center().Shrink(0).Cursor(ui.CursorPointer)
	bg, fg := p.cardHover, p.text
	if primary {
		bg, fg = p.accent, ui.Hex("#ffffff")
		if b.Hovered() && !disabled {
			bg = p.accentHover
		}
	} else if b.Hovered() {
		bg = p.cardActive
	}
	tint(b, bg)
	if disabled || loading {
		b.Opacity(0.5)
	}
	b.Children(func() {
		if loading {
			ui.Spinner(c).Size(12, 12)
		}
		label(c, text, sizeCaption, 16, 500, fg)
	})
	return b.Clicked() && !disabled && !loading
}

// inputColors keeps focused dark inputs readable over bright native material.
// Focus changes the border without washing out the field behind its placeholder.
func inputColors(p palette, focused bool) (background, border, placeholder ui.Color) {
	background, border, placeholder = p.subdued, p.borderDefault, p.muted
	if focused {
		background, border = p.card, p.borderFocus
		if p.dark {
			background = ui.RGBA(0, 0, 0, 0.35)
			placeholder = p.text.Alpha(0.8)
		}
	}
	return
}

const (
	inputHeight   float32 = 32
	inputRadius   float32 = 8
	inputLabelGap float32 = 4
)

type textEntryParts struct{ Control, Input ui.Element }

func inputLabel(c *ui.Context, p palette, title string) {
	label(c, strings.ToUpper(title), sizeCaption, 14, 600, p.muted).LetterSpacing(0.4).Padding(0, 2)
}

// All text entry uses MyGo's native input behavior inside the same control surface.
// Only multiline editors grow taller; typography, icons and field chrome stay equal.
func textEntry(c *ui.Context, p palette, key, title string, value *string, placeholder string, icon *ui.SVG, iconColor ui.Color, height float32, trailing func()) textEntryParts {
	if icon == nil {
		icon = iconTag
	}
	multiline := height > inputHeight
	box := ui.Row(c.Key(key+"-control")).Label(title+tr(" input")).Height(max(inputHeight, height)).Gap(6).Padding(0, 10).Radius(inputRadius).AlignItems(ui.Center).Shrink(0).MinWidth(0)
	if multiline {
		box.Padding(8, 10).AlignItems(ui.Start)
	}
	var in ui.Element
	box.Children(func() {
		glyph := ui.Icon(c, icon).Label(title + tr(" icon")).FontSize(14).TextColor(iconColor).Shrink(0)
		if multiline {
			glyph.Margin(1, 0, 0, 0)
		}
		if multiline {
			in = ui.TextAreaBase(c.Key(key+"-input"), value).AlignSelf(ui.Stretch)
		} else {
			in = ui.TextInputBase(c.Key(key+"-input"), value)
		}
		in.Label(title).Placeholder(placeholder).Grow(1).Basis(0).MinWidth(0).MinHeight(0).AlignSelf(ui.Stretch).FontSize(sizeSubhead).TextColor(p.text).Padding(0)
		if !multiline {
			in.Padding(6, 0)
		}
		if trailing != nil {
			trailing()
		}
	})
	bg, border, placeholderColor := inputColors(p, in.Focused())
	in.PlaceholderColor(placeholderColor)
	box.Background(bg).Border(1, border).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.02))
	return textEntryParts{Control: box, Input: in}
}

func labeledInput(c *ui.Context, p palette, key, title string, value *string, placeholder string, icon *ui.SVG, iconColor ui.Color, height float32, trailing func()) ui.Element {
	var in ui.Element
	ui.Column(c.Key(key)).Gap(inputLabelGap).Shrink(0).Children(func() {
		inputLabel(c, p, title)
		in = textEntry(c, p, key, title, value, placeholder, icon, iconColor, height, trailing).Input
		if key == "sub-name" || key == "edit-sub-name" {
			in.AutoFocus()
		}
	})
	return in
}

func field(c *ui.Context, p palette, key, title string, value *string, placeholder string, icon *ui.SVG, iconColor ui.Color, trailing func()) bool {
	return labeledInput(c, p, key, title, value, placeholder, icon, iconColor, inputHeight, trailing).Submitted()
}

// iconTile is the rounded square that holds an app's icon, a flag or a rule's icon. Its
// drawing fills a box of the tile's own content, so it sits in the middle whatever kind of
// drawing it is, and the border and corners stay clear of it.
func iconTile(c *ui.Context, p palette, draw func()) {
	ui.Box(c).Size(tw(7), tw(7)).Radius(7).Background(p.cardHover).Border(1, p.borderDefault).Shrink(0).Clip().Children(func() {
		ui.Box(c).Fill().Center().Children(draw)
	})
}

// accordion shows what fn builds below its header, as far open as p says: the height grows
// and the content fades in with it. It records how tall the content is when fully open.
func (a *app) accordion(c *ui.Context, key string, p float32, open bool, fn func()) {
	pal := a.palette
	if !open && p == 0 {
		return
	}
	host := ui.Column(c.Key("acc-"+key)).Shrink(0).ClipY().Background(pal.subdued).Radius(0, 0, tw(2), tw(2)) // bg-surface-subdued rounded-b-lg
	var inner ui.Element
	host.Children(func() {
		inner = ui.Column(c).Shrink(0)
		inner.Children(fn)
	})
	if h := inner.Bounds().H; h > 0 {
		a.natural[key] = h
	}
	if p < 1 {
		host.Height(p * a.naturalHeight(key)).Opacity(p)
	}
}
