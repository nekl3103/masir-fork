package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *app) notify(message string)        { a.queueNotice(message, "info") }
func (a *app) notifyError(message string)   { a.queueNotice(message, "error") }
func (a *app) notifyWarning(message string) { a.queueNotice(message, "warning") }
func (a *app) notifySuccess(message string) { a.queueNotice(message, "success") }

// Keep message identity stable: repeated failures update the existing toast.
func (a *app) queueNotice(message, kind string) {
	if a.noticeTypes == nil {
		a.noticeTypes = make(map[string]string)
	}
	a.noticeTypes[message] = kind
	for _, notice := range a.notices {
		if notice == message {
			return
		}
	}
	a.notices = append(a.notices, message)
}

func noticeTimeout(kind string) time.Duration {
	switch kind {
	case "error":
		return 8 * time.Second
	case "warning":
		return 6 * time.Second
	default:
		return 4 * time.Second
	}
}

func (a *app) toastViewport(c *ui.Context, p palette) {
	ui.ToastViewportBase(c, func(viewport ui.Element, notices []ui.Toast) {
		viewport.Padding(12).AlignItems(ui.Stretch).Gap(8)
		// The panel is only 300 DIPs wide. Keep the newest notice readable;
		// closing it reveals a previous notice if its timeout has not elapsed.
		if len(notices) == 0 {
			return
		}
		notice := notices[len(notices)-1]
		title, icon, color := tr("Notice"), iconInfo, p.accent
		switch notice.Type {
		case "error":
			title, icon, color = tr("Error"), iconBan, p.errorC
		case "warning":
			title, icon, color = tr("Warning"), iconWarning, p.degraded
		case "success":
			title, icon, color = tr("Done"), iconCheck, p.healthy
		}
		tint := float32(0.06)
		if p.dark {
			tint = 0.12
		}
		surface := p.popup.Mix(color, tint)
		toast := ui.ToastBase(c, notice)
		toast.Root.Column().FillWidth().Shrink(0).Padding(8, 10).Gap(4).Radius(cardRadius).
			Background(surface).Border(1, color.Alpha(0.3)).Shadow(0, 3, 12, 0, ui.RGBA(0, 0, 0, 0.16)).
			Label(tr("Notification: ") + title)
		if !a.motion.instant {
			toast.Root.Transition(ui.ElementTransition{Duration: 180 * time.Millisecond, Ease: easeApp, Position: true,
				Enter: &ui.Motion{Y: 6}, Exit: &ui.Motion{Y: 6}})
		}
		toast.Root.Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Start).Shrink(0).Children(func() {
				// Center the message group beside the dismiss target when it is
				// one line high. Inside the group, keep the icon on the first line.
				ui.Row(c).Grow(1).Basis(0).MinWidth(0).AlignSelf(ui.Center).AlignItems(ui.Start).Gap(4).Children(func() {
					ui.Box(c).Width(12).Shrink(0).Children(func() {
						// A typographic strut uses this engine's actual first-line
						// metrics, including fonts taller than the requested leading.
						label(c, "\u00a0", sizeSubhead, 12, 400, p.text).Opacity(0).Role(ui.RoleNone)
						ui.Box(c).Absolute().Fill().Center().Children(func() {
							ui.Icon(c, icon).Label(tr("Notification status icon")).FontSize(12).TextColor(color)
						})
					})
					_, height := c.Size()
					messageLimit := height - 64
					if notice.Action != "" {
						messageLimit = height - 96
					}
					ui.Scroll(c).Label(tr("Notification message")).Grow(1).Basis(0).MinWidth(0).
						MaxHeight(max(32, messageLimit)).ScrollbarInsets(0, 0, 0).Children(func() {
						ui.Column(c).Gap(2).Shrink(0).Children(func() {
							label(c, notice.Title, sizeSubhead, 12, 400, p.text).Selectable()
							if notice.Description != "" {
								label(c, notice.Description, sizeCaption, 12, 400, p.muted).Selectable()
							}
						})
					})
				})
				close := toast.CloseButton().Label(tr("Dismiss notification")).Size(24, 24).Radius(5).Shrink(0).Cursor(ui.CursorPointer).Center()
				if close.Hovered() {
					close.Background(color.Alpha(0.12))
				}
				close.Children(func() { ui.Icon(c, iconX).FontSize(12).TextColor(p.muted) })
			})
			if notice.Action != "" {
				toast.ActionButton().Label(notice.Action).Padding(5, 8).Radius(5).Background(color.Alpha(0.12)).Children(func() { label(c, notice.Action, sizeCaption, 16, 500, color) })
			}
		})
	})
}
