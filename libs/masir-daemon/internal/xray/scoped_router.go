package xray

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common"
	xserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type scopedRouter struct {
	*router.Router
	publication     sync.Mutex
	readers         atomic.Int64
	cleanupPending  atomic.Bool
	active          atomic.Pointer[routeTable]
	onIdle          func()
	staging         bool
	observersOnIdle func()
	profiles        map[string]*profileRouter // updated under Core.mu or before the instance starts
}

type routeTable struct {
	outer    *router.Router
	profiles map[string]*profileRouter
}

// This bit prevents reader registration while retired routing state is closed.
const cleaningRoutes = 1 << 62

var registerScopedRouter = sync.OnceFunc(func() {
	common.Must(common.RegisterConfig((*wrapperspb.StringValue)(nil), func(ctx context.Context, value interface{}) (interface{}, error) {
		raw, err := base64.StdEncoding.DecodeString(value.(*wrapperspb.StringValue).Value)
		if err != nil {
			return nil, err
		}
		cfg := new(router.Config)
		if err := proto.Unmarshal(raw, cfg); err != nil {
			return nil, err
		}
		native, err := common.CreateObject(ctx, cfg)
		if err != nil {
			return nil, err
		}
		r := &scopedRouter{Router: native.(*router.Router)}
		r.publishTable()
		return r, nil
	}))
})

func installScopedRouter(cfg *core.Config) {
	registerScopedRouter()
	name := string((&router.Config{}).ProtoReflect().Descriptor().FullName())
	for i, app := range cfg.App {
		if app.Type == name {
			cfg.App[i] = xserial.ToTypedMessage(wrapperspb.String(base64.StdEncoding.EncodeToString(app.Value)))
			return
		}
	}
}

func (r *scopedRouter) PickRoute(ctx routing.Context) (routing.Route, error) {
	for r.readers.Add(1) >= cleaningRoutes {
		r.releaseRoute()
		// Cleanup holds publication and excludes new lookups.
		r.publication.Lock()
		r.publication.Unlock()
	}
	defer r.releaseRoute()
	table := r.active.Load()
	route, err := table.outer.PickRoute(ctx)
	if err != nil {
		return nil, err
	}
	if scope := table.profiles[route.GetOutboundTag()]; scope != nil {
		return &scopedRoute{Route: scope.pick(ctx), outerRuleTag: route.GetRuleTag()}, nil
	}
	return route, nil
}

type scopedRoute struct {
	routing.Route
	outerRuleTag string
}

func (r *scopedRouter) releaseRoute() {
	if r.readers.Add(-1) == 0 && r.cleanupPending.Load() {
		r.publication.Lock()
		defer r.publication.Unlock()
		r.cleanupIfIdle()
	}
}

// publishTable runs under Core.mu or before the instance is bound.
func (r *scopedRouter) publishTable() {
	r.active.Store(&routeTable{outer: r.Router, profiles: r.profiles})
}

type profileRouter struct {
	router        *router.Router
	prefix        string
	firstOutbound string
	loopbacks     map[string]string
}

type profileRouteContext struct {
	routing.Context
	inbound  string
	loopback bool
}

func (c profileRouteContext) GetInboundTag() string { return c.inbound }
func (c profileRouteContext) GetSkipDNSResolve() bool {
	return c.loopback || c.Context.GetSkipDNSResolve()
}

type localRoute struct {
	routing.Context
	tag string
}

func (r *localRoute) GetOutboundTag() string       { return r.tag }
func (*localRoute) GetOutboundGroupTags() []string { return nil }
func (*localRoute) GetRuleTag() string             { return "" }

func (p *profileRouter) pick(ctx routing.Context) routing.Route {
	request := profileRouteContext{Context: ctx, inbound: p.prefix + "client"}
	var seen map[string]bool
	for range 64 {
		route, err := p.router.PickRoute(request)
		tag := p.firstOutbound
		switch {
		case err == nil:
			tag = route.GetOutboundTag()
		case !errors.Is(err, common.ErrNoClue) || tag == "":
			return &localRoute{Context: ctx, tag: "block"}
		}
		step := p.loopbacks[tag]
		if step == "" {
			if route != nil {
				return route
			}
			return &localRoute{Context: ctx, tag: tag}
		}
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[step] {
			break
		}
		seen[step] = true
		request.inbound = p.prefix + "step/" + step
		request.loopback = true
	}
	return &localRoute{Context: ctx, tag: "block"}
}

func (r *scopedRouter) configureScopes(inst *core.Instance, p *Profile) error {
	next := make(map[string]*profileRouter, len(p.scopes))
	for tag, scope := range r.profiles {
		next[tag] = scope
	}
	for tag, raw := range p.scopes {
		if _, exists := next[tag]; exists {
			continue
		}
		var settings struct {
			conf.RouterConfig
			FirstOutbound string            `json:"firstOutbound"`
			Loopbacks     map[string]string `json:"loopbacks"`
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		cfg, err := settings.Build()
		if err != nil {
			return fmt.Errorf("build provider routing: %w", err)
		}
		if err := validateRuleAttributes(cfg); err != nil {
			return err
		}
		obj, err := core.CreateObject(inst, cfg)
		if err != nil {
			return fmt.Errorf("prepare provider routing: %w", err)
		}
		scope := obj.(*router.Router)
		if err := scope.Start(); err != nil {
			return err
		}
		next[tag] = &profileRouter{router: scope, prefix: strings.TrimPrefix(tag, "masir-profile-route/"),
			firstOutbound: settings.FirstOutbound, loopbacks: settings.Loopbacks}
	}
	r.profiles = next
	r.publishTable()
	return nil
}

// retainScopes runs under the publication lock before publishing the new table.
func (r *scopedRouter) retainScopes(p *Profile) {
	next := make(map[string]*profileRouter, len(p.scopes))
	for tag := range p.scopes {
		next[tag] = r.profiles[tag]
	}
	r.profiles = next
}

// cleanupIfIdle runs under publication and keeps route lookups independent of
// each other, including DNS lookups that re-enter routing.
func (r *scopedRouter) cleanupIfIdle() {
	if r.staging || !r.cleanupPending.Load() || !r.readers.CompareAndSwap(0, cleaningRoutes) {
		return
	}
	defer r.readers.Add(-cleaningRoutes)
	r.cleanupPending.Store(false)
	if r.onIdle != nil {
		r.onIdle()
		r.onIdle = nil
	}
	if r.observersOnIdle != nil {
		r.observersOnIdle()
		r.observersOnIdle = nil
	}
}
