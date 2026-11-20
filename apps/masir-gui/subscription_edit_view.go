package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"
)

func (a *app) openSubscriptionEdit() {
	s := a.selectedSubscription()
	st := &a.subscriptionPage
	if s == nil || st.busy || a.subscriptionRefreshing[s.Id] {
		return
	}
	st.name, st.url, st.interval = s.Name, s.Url, strconv.FormatUint(uint64(s.CustomUpdateIntervalMinutes), 10)
	st.sources = nil
	if len(s.Sources) > 1 {
		st.sources = append(st.sources, s.Sources[1:]...)
	}
	st.removing, st.err, st.editHeight = false, "", 0
	a.openPage(editSubscriptionPath)
}

func (a *app) subscriptionEditView(c *ui.Context, p palette) {
	st := &a.subscriptionPage
	ui.Column(c).Fill().Children(func() {
		header := ui.Column(c).Bind(&st.editHeader).Padding(pageInset, pageInset, 0).Shrink(0)
		header.Children(func() { a.pageHeader(c, p, "Edit Subscription") })
		if a.selectedSubscription() == nil {
			label(c, "This subscription is no longer available.", sizeBody, 18, 400, p.muted).Padding(0, pageInset)
			return
		}
		ui.Scroll(c).Padding(0, pageInset, pageInset).ScrollbarInsets(0, 2, 0).Grow(1).Basis(0).MinHeight(0).Children(func() {
			form := ui.Column(c).Bind(&st.editContent).Label("Subscription edit content").Gap(10).Shrink(0)
			form.Children(func() {
				cardBox(c, p, func() {
					ui.Column(c).Padding(10).Gap(10).Children(func() {
						field(c, p, "edit-sub-name", "Name", &st.name, "Name", iconTag, p.muted, nil)
						field(c, p, "edit-sub-url", "URL", &st.url, "https://…", iconLink, p.muted, nil)
						sourceFields(c, p, "edit-source", &st.sources, st.busy)
						field(c, p, "edit-sub-interval", "Interval (minutes; 0 = automatic)", &st.interval, "0", iconClock, p.muted, nil)
					})
				})
				if st.err != "" {
					label(c, st.err, sizeCaption, 15, 400, p.errorC)
				}
				if button(c, "save-subscription", "Save changes", true, false, st.busy, p) {
					a.saveSubscription()
				}
				if button(c, "cancel-edit", "Cancel", false, st.busy, false, p) {
					a.goBack()
				}
			})
		})
	})
	// Handles read committed layout in this page's own engine, independent
	// of build order and the scroll container's deferred child build.
	header, content := st.editHeader.Bounds(c), st.editContent.Bounds(c)
	if header.H > 0 && content.H > 0 {
		measured := min(pageOf(subscriptionDetailsPath).height, header.H+content.H+pageInset)
		if abs32(st.editHeight-measured) > 0.5 {
			st.editHeight = measured
			c.AnimationFrame()
		}
		if a.path == editSubscriptionPath {
			a.toHeight = st.editHeight
		}
	} else if a.selectedSubscription() != nil {
		c.AnimationFrame()
	}
	if a.path == editSubscriptionPath {
		a.settleHeight()
	}
}
