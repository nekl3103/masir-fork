package main

import (
	"fmt"
	"strings"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

func (a *app) subscriptionDetailsView(c *ui.Context, p palette) {
	s := a.selectedSubscription()
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			a.pageHeader(c, p, tr("Subscription"), func() {
				if s != nil && iconButton(c, "edit-subscription", iconPencil, tr("Edit subscription"), p, iconSM, variantGhost, 15) {
					a.openSubscriptionEdit()
				}
			})
		})
		if s == nil {
			label(c, tr("This subscription is no longer available."), sizeBody, 18, 400, p.muted).Padding(0, pageInset)
			return
		}

		ui.Column(c).Padding(0, pageInset).Gap(10).Grow(1).Basis(0).MinHeight(0).Children(func() {
			a.subscriptionOverview(c, p, s)
			// Keep the server group framed while only its rows scroll and virtualize.
			ui.Column(c).Label(tr("Subscription server group")).Radius(cardRadius).Background(p.card).Border(1, p.borderDefault).Clip().Grow(1).Basis(0).MinHeight(0).Children(func() {
				configs := s.GetConfigs()
				a.groupHeader(c, p, s.Id, subscriptionName(s), len(configs), false, tr("Subscription server header"), nil, nil)
				cardDivider(c, p)
				a.subscriptionNodes.Key = func(i int) any {
					if len(configs) == 0 {
						return "empty-" + s.Id
					}
					if i < len(configs) {
						return serverKey{s.Id, configs[i].Id}
					}
					return nil
				}
				a.subscriptionNodes.Label = func(i int) string {
					if i < len(configs) {
						return configs[i].Name
					}
					return ""
				}
				containListScroll(ui.List(c, &a.subscriptionNodes, max(1, len(configs)), func(i int) {
					if len(configs) == 0 {
						label(c, tr("No servers yet. Refresh to try importing them again."), sizeCaption, 16, 400, p.muted).Padding(10, 6)
						return
					}
					cfg := configs[i]
					a.serverItem(c, p, server{key: serverKey{s.Id, cfg.Id}, subName: s.Name, cfg: cfg})
				})).Label(tr("Subscription servers")).Padding(4, 6, 6).ScrollbarInsets(0, 1, 0).Gap(3).Grow(1).Basis(0).MinHeight(0)
			})
		})
		a.subscriptionDetailsFooter(c, p, s)
	})
}

func (a *app) subscriptionOverview(c *ui.Context, p palette, s *pb.Subscription) {
	ui.Column(c).Gap(10).Shrink(0).Children(func() {
		ui.Row(c).AlignItems(ui.Center).PaddingX(tw(1)).Gap(8).Children(func() {
			ui.Column(c).Gap(3).Grow(1).Basis(0).MinWidth(0).Children(func() {
				label(c, subscriptionName(s), sizeTitle, 18, 600, p.text).SingleLine()
				ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
					ui.Icon(c, iconGlobe).FontSize(11).TextColor(p.muted)
					label(c, subscriptionProvider(s), sizeCaption, 15, 400, p.muted).SingleLine().Grow(1).Basis(0).MinWidth(0)
				})
			})
			ui.Column(c).AlignItems(ui.End).Gap(3).Shrink(0).Children(func() {
				active, ok := a.activeKey()
				interval := tr("Automatic")
				if n := s.UpdateIntervalMinutes; n > 0 {
					interval = fmt.Sprintf(tr("Every %d mins"), n)
					if n == 1 {
						interval = tr("Every 1 min")
					}
				}
				if ok && active.sub == s.Id {
					a.subscriptionActiveBadge(c, p, s.Id)
					interval = tr("Refresh: ") + strings.ToLower(interval)
				} else {
					label(c, tr("Auto-refresh"), sizeMicro, 14, 500, p.muted)
				}
				label(c, interval, sizeMicro, 14, 400, p.muted).SingleLine()
			})
		})
		cardBox(c, p, func() {
			usage, fraction := subscriptionUsage(s)
			subscriptionInfoRow(c, p, iconDownload, tr("Traffic"), usage)
			if s.GetUsage().GetTotalBytes() > 0 {
				ui.Column(c).Padding(0, 10, 8).Children(func() { subscriptionUsageBar(c, p, fraction) })
			}
			cardDivider(c, p)
			subscriptionInfoRow(c, p, iconCalendar, tr("Expiry"), subscriptionExpiry(s, time.Now()))
		})
		message := s.LastError
		if a.subscriptionPage.err != "" {
			message = a.subscriptionPage.err
		}
		if message != "" {
			ui.Column(c).Shrink(0).MinWidth(0).Gap(3).Children(func() {
				ui.Column(c).Gap(4).Padding(8).Radius(cardRadius).Background(p.errorSurface).Shrink(0).Children(func() {
					ui.Row(c).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconBan).FontSize(12).TextColor(p.errorC).Shrink(0)
						label(c, tr("Subscription needs attention"), sizeCaption, 15, 600, p.errorC)
					})
					label(c, message, sizeCaption, 15, 400, p.text).MaxLines(3).Selectable()
				})
				label(c, tr("Last attempt: ")+refreshTime(s.CheckedAtMs), sizeMicro, 14, 400, p.muted)
			})
		}
	})
}

func subscriptionInfoRow(c *ui.Context, p palette, icon *ui.SVG, title, value string) {
	ui.Row(c).Padding(7, 10).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon).FontSize(13).TextColor(p.muted).Shrink(0)
		ui.Column(c).Gap(1).Grow(1).Basis(0).MinWidth(0).Children(func() {
			label(c, title, sizeMicro, 12, 500, p.muted)
			label(c, value, sizeCaption, 15, 400, p.text).MaxLines(2)
		})
	})
}

func (a *app) subscriptionDetailsFooter(c *ui.Context, p palette, s *pb.Subscription) {
	st := &a.subscriptionPage
	busy := st.busy || a.subscriptionRefreshing[s.Id]
	ui.Column(c).Padding(8, pageInset, pageInset).Gap(7).Shrink(0).Children(func() {
		cardDivider(c, p)
		ui.Row(c).AlignItems(ui.Center).Gap(3).Children(func() {
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).PaddingStart(tw(1)).Gap(1).Children(func() {
				label(c, tr("Last refreshed"), sizeMicro, 12, 500, p.muted).SingleLine()
				label(c, refreshTime(s.UpdatedAtMs), sizeCaption, 15, 400, p.text).SingleLine()
			})
			b := ui.ButtonBase(c.Key("refresh-details")).Label(tr("Refresh ")+subscriptionName(s)).Height(22).Padding(0, 8).Gap(3).Radius(5).AlignItems(ui.Center).Cursor(ui.CursorPointer).Shrink(0)
			b.Background(p.cardHover).Border(1, p.borderDefault)
			if b.Hovered() && !busy {
				b.Background(p.cardActive)
			}
			if busy {
				b.Opacity(0.5)
			}
			b.Children(func() {
				if a.subscriptionRefreshing[s.Id] {
					ui.Spinner(c).Size(11, 11)
				} else {
					ui.Icon(c, iconRefresh).FontSize(11).TextColor(p.text)
				}
				label(c, tr("Refresh"), sizeMicro, 14, 500, p.text)
			})
			if b.Clicked() && !busy {
				a.refreshSubscription(s.Id)
			}
			if !st.removing {
				b := ui.ButtonBase(c.Key("remove-subscription")).Label(tr("Remove")).Height(22).Padding(0, 8).Gap(3).Radius(5).Center().Cursor(ui.CursorPointer).Shrink(0)
				if b.Hovered() {
					b.Background(p.errorSurface)
				}
				if busy {
					b.Opacity(0.5)
				}
				b.Children(func() {
					ui.Icon(c, iconTrash).FontSize(11).TextColor(p.errorC)
					label(c, tr("Remove"), sizeMicro, 14, 500, p.errorC)
				})
				if b.Clicked() && !busy {
					st.removing = true
				}
			}
		})
		if st.removing {
			label(c, tr("Remove this subscription and its servers?"), sizeCaption, 15, 500, p.errorC)
			ui.Row(c).Gap(6).Children(func() {
				removeBtn := ui.ButtonBase(c.Key("confirm-remove")).Label(tr("Remove")).Height(22).Padding(0, 8).Gap(3).Radius(5).Center().Cursor(ui.CursorPointer).Grow(1).Basis(0)
				bg := p.errorC
				if removeBtn.Hovered() && !busy {
					if p.dark {
						bg = ui.Hex("#e0382e")
					} else {
						bg = ui.Hex("#961818")
					}
				}
				removeBtn.Background(bg)
				if busy {
					removeBtn.Opacity(0.5)
				}
				removeBtn.Children(func() {
					if st.busy {
						ui.Spinner(c).Size(11, 11)
					} else {
						ui.Icon(c, iconTrash).FontSize(11).TextColor(ui.Hex("#ffffff"))
					}
					label(c, tr("Remove"), sizeMicro, 14, 500, ui.Hex("#ffffff"))
				})
				if removeBtn.Clicked() && !busy {
					a.subscriptionAction("remove", nil)
				}

				keepBtn := ui.ButtonBase(c.Key("cancel-remove")).Label(tr("Keep")).Height(22).Padding(0, 8).Gap(3).Radius(5).Center().Cursor(ui.CursorPointer).Grow(1).Basis(0)
				keepBtn.Background(p.cardHover).Border(1, p.borderDefault)
				if keepBtn.Hovered() && !busy {
					keepBtn.Background(p.cardActive)
				}
				if busy {
					keepBtn.Opacity(0.5)
				}
				keepBtn.Children(func() {
					label(c, tr("Keep"), sizeMicro, 14, 500, p.text)
				})
				if keepBtn.Clicked() && !busy {
					st.removing = false
				}
			})
		}
	})
}
