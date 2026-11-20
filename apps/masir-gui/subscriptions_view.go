package main

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

type subscriptionPageState struct {
	id, name, url, interval string
	sources                 []string
	removing, busy          bool
	editHeight              float32
	editHeader, editContent ui.Handle
	err                     string
}

func (a *app) openSubscription(id string) {
	a.subscriptionPage = subscriptionPageState{id: id}
	a.subscriptionNodes = ui.ListState{}
	a.openPage(subscriptionDetailsPath)
}

func (a *app) selectedSubscription() *pb.Subscription {
	for _, s := range a.subscriptions.subscriptions {
		if s.Id == a.subscriptionPage.id {
			return s
		}
	}
	return nil
}

func subscriptionProvider(s *pb.Subscription) string {
	u, err := url.Parse(s.GetUrl())
	if err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "Provider not specified"
}

func subscriptionUsage(s *pb.Subscription) (string, float32) {
	u := s.GetUsage()
	if u == nil {
		return "Usage not reported", 0
	}
	used := float64(u.GetUploadBytes()) + float64(u.GetDownloadBytes())
	if u.TotalBytes <= 0 {
		return fmt.Sprintf("%.1f GB used · Unlimited", used/1e9), 0
	}
	return fmt.Sprintf("%.1f / %.1f GB", used/1e9, float64(u.TotalBytes)/1e9), float32(min(1, max(0, used/float64(u.TotalBytes))))
}

func subscriptionExpiry(s *pb.Subscription, now time.Time) string {
	ms := s.GetUsage().GetExpireAtMs()
	if ms == 0 {
		return "No expiry reported"
	}
	if ms <= now.UnixMilli() {
		return "Expired · " + time.UnixMilli(ms).Format("Jan 2, 2006")
	}
	return "Expires " + time.UnixMilli(ms).Format("Jan 2, 2006")
}

func refreshTime(ms int64) string {
	if ms == 0 {
		return "Never"
	}
	return time.UnixMilli(ms).Local().Format("Jan 2, 15:04")
}

func (a *app) subscriptionsView(c *ui.Context, p palette) {
	subs := a.subscriptions.subscriptions
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			add := func() {
				if iconButton(c, "subscriptions-add", iconPlus, "Add Subscription", p, iconSM, variantGhost, 18) {
					a.openPage(addSubscriptionPath)
				}
			}
			if len(subs) == 0 {
				a.pageHeader(c, p, "Subscriptions", add)
				return
			}
			a.pageHeader(c, p, "Subscriptions", func() {
				if iconButton(c, "subscriptions-refresh-all", iconRefresh, "Refresh all subscriptions", p, iconSM, variantGhost, 16) {
					for _, sub := range subs {
						a.refreshSubscription(sub.Id)
					}
				}
			}, add)
		})
		if len(subs) == 0 {
			label(c, "No subscriptions yet. Add a subscription to import servers.", sizeBody, 18, 400, p.muted).Padding(0, pageInset, pageInset)
			return
		}
		containListScroll(ui.List(c, &a.subscriptionList, len(subs), func(i int) {
			s := a.subscriptions.subscriptions[i]
			a.subscriptionCard(c, p, s)

		})).Label("Subscription list").Padding(0, pageInset, pageInset).ScrollbarInsets(0, 2, 0).Gap(8).Grow(1).Basis(0).MinHeight(0)
	})
}

func (a *app) saveSubscription() {
	st := &a.subscriptionPage
	interval, err := strconv.ParseUint(strings.TrimSpace(st.interval), 10, 32)
	if err != nil {
		st.err = "Enter an interval in whole minutes, or 0 for automatic."
		return
	}
	links := sourceURLs(st.url, st.sources)
	if len(links) == 0 {
		st.err = "Enter at least one subscription URL or TUIC link."
		return
	}
	for _, link := range links {
		u, err := url.Parse(link)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "tuic") {
			st.err = "Enter an HTTP(S) subscription URL or a TUIC link."
			return
		}
	}
	name, minutes := strings.TrimSpace(st.name), uint32(interval)
	req := &pb.AlterSubscriptionRequest{Id: st.id, Name: &name, CustomUpdateIntervalMinutes: &minutes}
	if len(links) == 1 && len(a.selectedSubscription().GetSources()) <= 1 {
		req.Url = &links[0]
	} else {
		req.Sources = links
	}
	a.subscriptionAction("edit", req)
}

func (a *app) subscriptionAction(action string, req *pb.AlterSubscriptionRequest) {
	if action == "refresh" {
		a.refreshSubscription(a.subscriptionPage.id)
		return
	}
	st := &a.subscriptionPage
	if st.busy || a.subscriptionRefreshing[st.id] {
		return
	}
	id := st.id
	var subName string
	if s := a.selectedSubscription(); s != nil {
		subName = subscriptionName(s)
	}
	st.busy, st.err = true, ""
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		var err error
		switch action {
		case "edit":
			_, err = a.d.AlterSubscription(ctx, req)
		case "remove":
			_, err = a.d.RemoveSubscription(ctx, &pb.RemoveSubscriptionRequest{Id: id})
		}
		a.post(func() {
			// Refresh even on failure: a failed fetch changes checked_at and last_error.
			a.refreshLists()
			if a.subscriptionPage.id != id {
				return
			}
			a.subscriptionPage.busy = false
			if err != nil {
				a.subscriptionPage.err = describe(err)
				return
			}
			if action == "edit" && a.path == editSubscriptionPath {
				a.goBack()
			} else if action == "remove" {
				for a.path != mainPath && a.router.CanGoBack() {
					a.goBack()
				}
				if a.scrollToSubID == id {
					a.scrollToSubID = ""
				}
				if a.collapsedSubs != nil {
					delete(a.collapsedSubs, id)
				}
				msg := "Subscription deleted"
				if subName != "" {
					msg = fmt.Sprintf("Subscription %q deleted", subName)
				}
				a.notify(msg)
			}
		})
	})
}

// The card opens Details; its refresh action stays on the list.
func (a *app) subscriptionCard(c *ui.Context, p palette, s *pb.Subscription) {
	cardBox(c, p, func() {
		handled := false
		b := ui.ButtonBase(c.Key(s.Id)).Label("Open "+subscriptionName(s)).FillWidth().MinWidth(0).Padding(8, 10, 6).Cursor(ui.CursorPointer)
		if b.Hovered() {
			b.Background(p.cardHover)
		}
		b.Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(tw(2)).Children(func() {
				ui.Column(c).FillWidth().MinWidth(0).Gap(tw(1)).Children(func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
							label(c, subscriptionName(s), sizeBody, 18, 600, p.text).SingleLine().MinWidth(0).Shrink(1)
							a.subscriptionActiveBadge(c, p, s.Id)
						})
						ui.Icon(c, iconChevronRight).FontSize(13).TextColor(p.tertiary)
					})

					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						label(c, subscriptionProvider(s), sizeCaption, 14, 400, p.muted).SingleLine()
						n := fmt.Sprintf("%d nodes", s.ConfigCount)
						if s.ConfigCount == 1 {
							n = "1 node"
						}
						label(c, n, sizeCaption, 14, 400, p.muted)
					})
				})

				u := s.GetUsage()
				if u == nil {
					label(c, "Usage not shared by provider", sizeCaption, 15, 400, p.tertiary)
				} else {
					used := max(0, float64(u.UploadBytes)+float64(u.DownloadBytes))
					ui.Column(c).Gap(5).Children(func() {
						ui.Row(c).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							label(c, subscriptionBytes(used)+" used", sizeCaption, 15, 500, p.text).SingleLine()
							remaining := "Unlimited"
							color := p.muted
							if u.TotalBytes > 0 {
								remaining = subscriptionBytes(max(0, float64(u.TotalBytes)-used)) + " left"
								if used >= float64(u.TotalBytes) {
									color = p.errorC
								}
							}
							label(c, remaining, sizeCaption, 15, 400, color).SingleLine().Shrink(0)
						})
						if u.TotalBytes > 0 {
							subscriptionUsageBar(c, p, float32(min(1, used/float64(u.TotalBytes))))
						}
					})
				}
				if ms := u.GetExpireAtMs(); ms > 0 {
					now := time.Now()
					color := p.muted
					if ms <= now.UnixMilli() {
						color = p.errorC
					} else if time.UnixMilli(ms).Sub(now) < 7*24*time.Hour {
						color = p.degraded
					}
					label(c, subscriptionCardExpiry(s, now), sizeCaption, 15, 400, color)
				}
				ui.Column(c).Gap(4).Children(func() {
					ui.Box(c).Height(0.5).Background(p.borderSubtle)
					ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).Children(func() {
						label(c, subscriptionUpdated(s.UpdatedAtMs, time.Now()), sizeMicro, 14, 400, p.tertiary).SingleLine().Grow(1).Basis(0).MinWidth(0)
						label(c, subscriptionRefreshInterval(s), sizeMicro, 14, 400, p.tertiary).SingleLine().Shrink(0)
						handled = a.subscriptionRefreshButton(c, p, s)
					})
				})
				if s.LastError != "" {
					ui.Row(c).Padding(5, 7).Radius(5).Background(p.errorSurface).Children(func() {
						message := "Refresh failed · Existing nodes kept"
						if s.ConfigCount == 0 {
							message = "Refresh failed · No servers available"
						}
						label(c, message, sizeCaption, 15, 500, p.errorC)
					})
				}
			})
		})
		if b.Clicked() && !handled {
			a.openSubscription(s.Id)
		}
	})
}

func subscriptionName(s *pb.Subscription) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return subscriptionProvider(s)
}

func subscriptionBytes(n float64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1f TB", n/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", n/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", n/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f KB", n/1e3)
	default:
		return fmt.Sprintf("%.0f B", n)
	}
}

func subscriptionUsageBar(c *ui.Context, p palette, fraction float32) {
	color := p.accent
	if fraction >= 1 {
		color = p.errorC
	} else if fraction >= 0.9 {
		color = p.degraded
	}
	ui.Row(c).Height(4).Radius(2).Background(p.subdued).Clip().Children(func() {
		if fraction > 0 {
			ui.Box(c).Height(4).Grow(fraction).Basis(0).Background(color)
		}
		if fraction < 1 {
			ui.Box(c).Height(4).Grow(1 - fraction).Basis(0)
		}
	})
}

func subscriptionUpdated(ms int64, now time.Time) string {
	if ms == 0 {
		return "Not refreshed yet"
	}
	elapsed := now.Sub(time.UnixMilli(ms))
	switch {
	case elapsed < time.Minute:
		return "Updated just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("Updated %dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("Updated %dh ago", int(elapsed.Hours()))
	default:
		return "Updated " + time.UnixMilli(ms).Local().Format("Jan 2")
	}
}

func subscriptionRefreshInterval(s *pb.Subscription) string {
	n := s.UpdateIntervalMinutes
	if n == 0 {
		return "Automatic"
	}
	if n%1440 == 0 {
		return fmt.Sprintf("Every %dd", n/1440)
	}
	if n%60 == 0 {
		return fmt.Sprintf("Every %dh", n/60)
	}
	return fmt.Sprintf("Every %dm", n)
}

func subscriptionCardExpiry(s *pb.Subscription, now time.Time) string {
	remaining := time.UnixMilli(s.GetUsage().GetExpireAtMs()).Sub(now)
	switch {
	case remaining <= 0:
		return "Expired"
	case remaining <= 24*time.Hour:
		return "Expires within 24 hours"
	case remaining < 7*24*time.Hour:
		return fmt.Sprintf("Expires in %d days", int(math.Ceil(remaining.Hours()/24)))
	default:
		return subscriptionExpiry(s, now)
	}
}

func (a *app) subscriptionActiveBadge(c *ui.Context, p palette, id string) {
	if key, ok := a.activeKey(); ok && key.sub == id {
		badge(c, "Active", nil, p.healthy, p.healthySurface, 16, 0)
	}
}

func (a *app) subscriptionRefreshButton(c *ui.Context, p palette, s *pb.Subscription) bool {
	busy := a.subscriptionRefreshing[s.Id]
	b := ui.ButtonBase(c.Key("refresh-"+s.Id)).Label("Refresh "+subscriptionName(s)).Tooltip("Refresh subscription").Size(16, 16).Radius(4).Center().Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(p.cardHover)
	}
	b.Children(func() {
		if busy {
			ui.Spinner(c).Size(10, 10)
		} else {
			ui.Icon(c, iconRefresh).FontSize(12).TextColor(p.muted)
		}
	})
	if b.Clicked() && !busy {
		a.refreshSubscription(s.Id)
	}
	return b.Clicked()
}
