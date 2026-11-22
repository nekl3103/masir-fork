package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// active is the selected server, if the lists have it.
func (a *app) active() (server, bool) {
	st := a.status
	k, ok := a.activeKey()
	if !ok {
		return server{}, false
	}
	for _, s := range a.subscriptions.servers {
		if s.key == k {
			return s, true
		}
	}
	// The selection is gone from its subscription: the daemon keeps running a copy.
	return server{key: k, subName: "", cfg: &pb.SubscriptionConfig{Id: k.cfg, Name: st.GetActiveConfigName()}}, true
}

// serverSearchText is everything the picker's search matches: the provider's own name,
// the country its flag stands for, the subscription, protocol, address and country code.
// It is built when the lists load, not on every frame of typing.
func serverSearchText(name, sub string, cfg *pb.SubscriptionConfig) string {
	flag, title, detail := serverTitle(name)
	parts := []string{name, title, detail, sub, cfg.GetProtocol(), cfg.GetAddress()}
	if code := emojiCode(flag); code != "" {
		parts = append(parts, code, countryNameIn(code, "en"), countryNameIn(code, "ru"))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// filteredServers are the servers matching the search, in the order of their subscriptions.
func (a *app) filteredServers() []server {
	return a.serversMatching(strings.ToLower(strings.TrimSpace(a.query)))
}

func (a *app) serversMatching(q string) []server {
	if q == "" {
		return a.subscriptions.servers
	}
	var out []server
	for _, s := range a.subscriptions.servers {
		hay := s.search
		if hay == "" { // a server made without the list, as in a test
			hay = serverSearchText(s.cfg.GetName(), s.subName, s.cfg)
		}
		if strings.Contains(hay, q) {
			out = append(out, s)
		}
	}
	return out
}

// latencyOf is a result as one number: milliseconds, or -1 when the probe failed.
func latencyOf(r *pb.LatencyResult) int64 {
	if r.GetError() != "" {
		return -1
	}
	return r.GetLatencyMs()
}

func (a *app) recordLatency(key serverKey, result *pb.LatencyResult) {
	a.latency[key] = latencyOf(result)
	a.latencyAt[key] = result.GetTestedAtMs()
}

const activeLatencyInterval = 30 * time.Second

// watchActiveLatency runs for the lifetime of the tray app, including while hidden.
func (a *app) watchActiveLatency(ctx context.Context) {
	ticker := time.NewTicker(activeLatencyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.post(func() { a.refreshActiveLatency(ctx) })
		}
	}
}

// activeLatencyTarget follows the confirmed selection. The subscription probe needs
// a config that still exists in its subscription.
func (a *app) activeLatencyTarget() (serverKey, bool) {
	st := a.status
	if !a.reachable || st.GetActiveStale() || st.GetActiveSubscriptionId() == "" || st.GetActiveConfigId() == "" {
		return serverKey{}, false
	}
	return serverKey{st.GetActiveSubscriptionId(), st.GetActiveConfigId()}, true
}

func (a *app) refreshActiveLatency(ctx context.Context) {
	if key, ok := a.activeLatencyTarget(); ok && ctx.Err() == nil {
		a.probeServers(ctx, true, []server{{key: key}})
	}
}

// testServers measures the given servers, or all of them when none are given; results
// arrive as they finish.
func (a *app) testServers(only ...server) {
	targets := only
	if len(targets) == 0 {
		targets = a.subscriptions.servers
	}
	a.probeServers(context.Background(), false, targets)
}

func (a *app) probeServers(parent context.Context, quiet bool, targets []server) {
	bySub := map[string][]string{}
	for _, s := range targets {
		if a.testing[s.key] {
			continue
		}
		a.testing[s.key] = true
		bySub[s.key.sub] = append(bySub[s.key.sub], s.key.cfg)
	}
	subIDs := make([]string, 0, len(bySub))
	for id := range bySub {
		subIDs = append(subIDs, id)
	}
	sort.Strings(subIDs)
	for _, id := range subIDs {
		cfgs := bySub[id]
		a.async(func() {
			ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
			defer cancel()
			stream, err := a.d.TestSubscriptionLatency(ctx, &pb.TestSubscriptionLatencyRequest{SubscriptionId: id, ConfigIds: cfgs})
			done := map[string]bool{}
			var saved map[string]*pb.LatencyResult
			if err == nil {
				for {
					res, recvErr := stream.Recv()
					if recvErr != nil {
						err = recvErr
						break
					}
					k := serverKey{id, res.ConfigId}
					done[res.ConfigId] = true
					a.post(func() {
						a.recordLatency(k, res.Result)
						delete(a.testing, k)
					})
				}
				if len(done) < len(cfgs) {
					saved = a.savedLatencies(parent, id)
				}
			}
			a.post(func() { // whatever didn't answer is no longer being measured
				for _, cfg := range cfgs {
					if !done[cfg] {
						key := serverKey{id, cfg}
						if result := saved[cfg]; result.GetTestedAtMs() > a.latencyAt[key] {
							a.recordLatency(key, result)
						}
						delete(a.testing, key)
					}
				}
				if !quiet && err != nil && !errors.Is(err, io.EOF) {
					a.fail(tr("Couldn't complete the latency test: "), err)
				}
			})
		})
	}
}

// A stream can end after the daemon saves a result but before it delivers it.
func (a *app) savedLatencies(parent context.Context, id string) map[string]*pb.LatencyResult {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	sub, err := a.d.GetSubscription(ctx, &pb.GetSubscriptionRequest{Id: id})
	if err != nil {
		return nil
	}
	results := make(map[string]*pb.LatencyResult, len(sub.GetConfigs()))
	for _, cfg := range sub.GetConfigs() {
		results[cfg.GetId()] = cfg.GetLatency()
	}
	return results
}

func (a *app) removeSubscription(id string) {
	var subName string
	for _, s := range a.subscriptions.subscriptions {
		if s.Id == id {
			subName = subscriptionName(s)
			break
		}
	}
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		_, err := a.d.RemoveSubscription(ctx, &pb.RemoveSubscriptionRequest{Id: id})
		a.post(func() {
			a.refreshLists()
			if err != nil {
				a.fail(tr("Couldn't remove the subscription: "), err)
				return
			}
			for a.path != mainPath && a.router.CanGoBack() {
				a.goBack()
			}
			if a.scrollToSubID == id {
				a.scrollToSubID = ""
			}
			if a.collapsedSubs != nil {
				delete(a.collapsedSubs, id)
			}
			msg := tr("Subscription deleted")
			if subName != "" {
				msg = fmt.Sprintf(tr("Subscription %q deleted"), subName)
			}
			a.notify(msg)
		})
	})
}

// Refresh state is per subscription, so cards and Details share progress without
// coupling background completions to whichever page is currently open.
func (a *app) refreshSubscription(id string) {
	if a.subscriptionRefreshing[id] || (a.subscriptionPage.id == id && a.subscriptionPage.busy) {
		return
	}
	if a.subscriptionRefreshing == nil {
		a.subscriptionRefreshing = make(map[string]bool)
	}
	a.subscriptionRefreshing[id] = true
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		_, err := a.d.UpdateSubscription(ctx, &pb.UpdateSubscriptionRequest{Id: id})
		a.post(func() {
			delete(a.subscriptionRefreshing, id)
			a.refreshLists() // failed fetches also change last_error and checked_at
			if a.subscriptionPage.id == id {
				a.subscriptionPage.err = ""
				if err != nil {
					a.subscriptionPage.err = describe(err)
				}
			}
			if err != nil {
				a.fail(tr("Couldn't refresh the subscription: "), err)
			}
		})
	})
}

func (a *app) addSubscription() {
	url, name, interval := strings.TrimSpace(a.subURL), strings.TrimSpace(a.subName), a.subCustomInterval
	links := sourceURLs(url, a.subSources)
	if len(links) == 0 || a.subBusy {
		return
	}
	a.subBusy, a.subError = true, ""
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		req := &pb.AddSubscriptionRequest{
			Name:                        name,
			CustomUpdateIntervalMinutes: interval,
		}
		if len(links) == 1 {
			req.Url = links[0]
		} else {
			req.Sources = links
		}
		res, err := a.d.AddSubscription(ctx, req)
		a.post(func() {
			a.subBusy = false
			if err != nil {
				a.subError = describe(err)
				return
			}
			msg := tr("Subscription added")
			var newSubID string
			if res != nil && res.Subscription != nil {
				newSubID = res.Subscription.Id
				if res.Subscription.Name != "" {
					msg = fmt.Sprintf(tr("Subscription %q added"), res.Subscription.Name)
				}
			}
			a.notifySuccess(msg)
			a.subURL, a.subName, a.subCustomInterval = "", "", 0
			a.subSources = nil
			a.query = ""
			a.open = sectionServers
			if newSubID != "" {
				a.scrollToSubID = newSubID
				if a.collapsedSubs != nil {
					delete(a.collapsedSubs, newSubID)
				}
			}
			a.goBack() // the form opened over the main page
			a.refreshLists()
		})
	})
}

func (a *app) isGroupCollapsed(subID string) bool {
	if a.collapsedSubs == nil {
		return false
	}
	return a.collapsedSubs[subID]
}

func (a *app) toggleGroupCollapse(subID string) {
	if a.collapsedSubs == nil {
		a.collapsedSubs = map[string]bool{}
	}
	a.collapsedSubs[subID] = !a.collapsedSubs[subID]
	a.saveGUISettings()
}
