package main

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) addSubscriptionView(c *ui.Context, p palette) {
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Bind(&a.importHeader).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			a.pageHeader(c, p, "Add Subscription")
		})
		ui.Scroll(c).Label("Subscription import form").Padding(0, pageInset).ScrollbarInsets(0, 2, 0).Grow(1).Basis(0).MinHeight(0).Children(func() {
			content := ui.Column(c).Bind(&a.importContent).Gap(tw(2.5)).Shrink(0)
			previousHeight := a.importContent.Bounds(c).H
			content.Draw(func(_ *ui.Painter, r ui.Rect) {
				if abs32(r.H-previousHeight) > 0.5 {
					c.Invalidate()
				}
			})
			content.Children(func() {
				cardBox(c, p, func() {
					ui.Column(c).Label("Subscription link fields").Padding(tw(2.5), tw(3)).Gap(tw(2.5)).Children(func() {
						nameSubmitted := field(c, p, "sub-name", "Label (Optional)", &a.subName, "e.g. My Server Group", iconTag, p.muted, nil)
						enter := field(c, p, "sub-url", "Subscription URL", &a.subURL, "https://example.com/sub/...", iconLink, p.muted, func() {
							if iconButton(c, "paste", iconClipboard, "Paste from clipboard", p, iconXS, variantSubtle, 14) {
								if s := readClipboard(); s != "" {
									a.subURL = s
								}
							}
						})
						if nameSubmitted || enter {
							a.addSubscription()
						}
					})
				})

				label(c, "Supports V2Ray/Xray, AmneziaWG, TUIC v5 and MASQUE (JSON).", sizeCaption, 15, 400, p.muted).Padding(0, tw(1))
				if a.subError != "" {
					ui.Row(c).Label("Subscription import error").Gap(tw(1.5)).Padding(tw(2.5), tw(3)).Radius(cardRadius).Background(p.errorSurface).Border(1, p.errorBorder).Children(func() {
						label(c, a.subError, sizeCaption, 15, 400, p.errorC).Selectable()
					})
				}
			})
		})
		// An error can grow the form; keep its action reachable at the panel's foot.
		ui.Column(c).Bind(&a.importFooter).Padding(8, pageInset, pageInset).Shrink(0).Children(func() {
			text := "Import Subscription"
			if a.subBusy {
				text = "Importing & Parsing..."
			}
			if button(c, "import", text, true, strings.TrimSpace(a.subURL) == "", a.subBusy, p, cardRadius) {
				a.addSubscription()
			}
		})
	})
	// Measure the entire form, not the clipped scroll viewport. A long error
	// grows the page up to the normal detail-page limit; beyond it the form
	// scrolls while Import stays fixed and reachable.
	header, content, footer := a.importHeader.Bounds(c), a.importContent.Bounds(c), a.importFooter.Bounds(c)
	if header.H > 0 && content.H > 0 && footer.H > 0 {
		// Include the outer surface border and round outward so neither a
		// native integer resize nor a PNG capture cuts off the last pixel.
		natural := header.H + content.H + footer.H + 2*panelBorderWidth(c)
		measured := max(float32(addSubscriptionHeight), min(pageOf(subscriptionDetailsPath).height, float32(math.Ceil(float64(natural)))))
		if abs32(a.importHeight-measured) > 0.5 {
			a.importHeight = measured
			c.AnimationFrame()
		}
		if a.path == addSubscriptionPath {
			a.toHeight = a.importHeight
		}
	} else {
		c.AnimationFrame()
	}
	if a.path == addSubscriptionPath {
		a.settleHeight()
	}
}
