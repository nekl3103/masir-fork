package main

import (
	"fmt"
	"strings"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo/ui"
)

type routeCheckState struct {
	domain  string
	checked string
	busy    bool
	err     string
	result  *pb.TestRoutingResponse
}

func (a *app) routeCheckView(c *ui.Context, p palette) {
	st := &a.routeCheck
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(pageInset, pageInset, 0).Shrink(0).Children(func() {
			a.pageHeader(c, p, tr("Check Domain Route"))
		})
		ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Padding(0, pageInset, pageInset).Children(func() {
			ui.Column(c).Gap(tw(2.5)).Shrink(0).Children(func() {
				cardBox(c, p, func() {
					ui.Column(c).Padding(tw(2.5), tw(3)).Children(func() {
						if field(c, p, "route-domain", tr("Domain"), &st.domain, "example.com", iconGlobe, p.muted, nil) {
							a.checkDomainRoute()
						}
					})
				})
				label(c, tr("Tests HTTPS through the current Xray route. ipify checks the public IP through the same outbound."), sizeCaption, 15, 400, p.muted)
				if button(c, "route-check-submit", tr("Check route"), true, strings.TrimSpace(st.domain) == "", st.busy, p) {
					a.checkDomainRoute()
				}
				if st.err != "" {
					label(c, st.err, sizeCaption, 15, 400, p.errorC).Selectable()
				}
				if res := st.result; res != nil {
					a.routeCheckResult(c, p, st.checked, res)
				}
			})
		})
	})
}

func (a *app) checkDomainRoute() {
	st := &a.routeCheck
	domain := strings.TrimSpace(st.domain)
	if st.busy || domain == "" {
		return
	}
	st.checked, st.busy, st.err, st.result = domain, true, "", nil
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		res, err := a.d.TestRouting(ctx, &pb.TestRoutingRequest{Domain: domain, Port: 443, ProbeEgress: true})
		a.post(func() {
			st.busy = false
			if err != nil {
				st.err = describe(err)
				return
			}
			if res.GetOutboundName() == "" {
				st.err = tr("Update the background service to run the HTTPS and IP checks.")
				return
			}
			st.result = res
		})
	})
}

func (a *app) routeCheckResult(c *ui.Context, p palette, domain string, res *pb.TestRoutingResponse) {
	rule := res.GetRuleTag()
	switch rule {
	case "", "masir-default-route":
		rule = tr("Default route")
	case "masir-lan-bypass":
		rule = tr("Local network bypass")
	}
	cardBox(c, p, func() {
		ui.Column(c).Padding(tw(2.5), tw(3)).Gap(tw(1)).Children(func() {
			label(c, domain, sizeSubhead, 18, 600, p.text).Selectable()
			label(c, tr("Rule · ")+rule, sizeCaption, 15, 400, p.text).Selectable()
			label(c, tr("Route · ")+routeOutboundName(res), sizeCaption, 15, 400, p.text).Selectable()
			if code := res.GetHttpStatus(); code != 0 {
				label(c, fmt.Sprintf("HTTPS · %d", code), sizeCaption, 15, 400, p.muted)
			} else if res.GetHttpError() != "" {
				label(c, "HTTPS · "+res.GetHttpError(), sizeCaption, 15, 400, p.errorC)
			}
			if addr := res.GetEgressIp(); addr != "" {
				label(c, tr("Public IP · ")+addr, sizeCaption, 15, 400, p.text).Selectable()
			} else if res.GetEgressError() != "" {
				label(c, tr("IP check · ")+res.GetEgressError(), sizeCaption, 15, 400, p.errorC)
			}
		})
	})
}

func routeOutboundName(res *pb.TestRoutingResponse) string {
	switch res.GetOutboundTag() {
	case "direct", "block":
		return tr(res.GetOutboundName())
	default:
		return res.GetOutboundName()
	}
}
