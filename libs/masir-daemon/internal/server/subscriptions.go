package server

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

func (s *Server) ListSubscriptions(ctx context.Context, req *pb.ListSubscriptionsRequest) (*pb.ListSubscriptionsResponse, error) {
	s.collectTraffic(ctx)
	all, err := s.subs.List()
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*pb.Subscription, len(all))
	for i := range all {
		out[i] = s.toPBSubscription(&all[i], req.IncludeConfigs)
	}
	return &pb.ListSubscriptionsResponse{Subscriptions: out}, nil
}

func (s *Server) GetSubscription(ctx context.Context, req *pb.GetSubscriptionRequest) (*pb.GetSubscriptionResponse, error) {
	s.collectTraffic(ctx)
	sub, err := s.subs.Get(req.Id)
	if err != nil {
		return nil, toStatus(err)
	}
	configs := make([]*pb.SubscriptionConfig, len(sub.Configs))
	for i := range sub.Configs {
		configs[i] = s.toPBConfig(req.Id, &sub.Configs[i])
	}
	return &pb.GetSubscriptionResponse{Subscription: s.toPBSubscription(sub), Configs: configs}, nil
}

// validCustomInterval checks a subscription's own refresh interval; 0 means "use the
// setting".
func validCustomInterval(minutes uint32) error {
	if minutes == 0 {
		return nil
	}
	if err := store.ValidSubInterval(int(minutes)); err != nil {
		return toStatus(err)
	}
	return nil
}

func (s *Server) AddSubscription(ctx context.Context, req *pb.AddSubscriptionRequest) (*pb.AddSubscriptionResponse, error) {
	if err := validCustomInterval(req.CustomUpdateIntervalMinutes); err != nil {
		return nil, err
	}
	var sub *subs.Subscription
	var err error
	if len(req.Sources) != 0 {
		sub, err = s.subs.AddSources(ctx, req.Sources, req.Name, req.CustomUpdateIntervalMinutes)
	} else {
		sub, err = s.subs.Add(ctx, req.Url, req.Name, req.CustomUpdateIntervalMinutes)
	}
	if err != nil {
		return nil, toStatus(err)
	}
	s.track("subscription_added", map[string]any{
		"config_count": len(sub.Configs),
	})
	return &pb.AddSubscriptionResponse{Subscription: s.toPBSubscription(sub)}, nil
}

func (s *Server) AlterSubscription(ctx context.Context, req *pb.AlterSubscriptionRequest) (*pb.AlterSubscriptionResponse, error) {
	if req.CustomUpdateIntervalMinutes != nil {
		if err := validCustomInterval(*req.CustomUpdateIntervalMinutes); err != nil {
			return nil, err
		}
	}
	var sub *subs.Subscription
	var err error
	if len(req.Sources) != 0 {
		sub, err = s.subs.AlterSources(ctx, req.Id, req.Sources, req.Name, req.CustomUpdateIntervalMinutes)
	} else {
		sub, err = s.subs.Alter(ctx, req.Id, req.Name, req.Url, req.CustomUpdateIntervalMinutes)
	}
	if err != nil {
		return nil, toStatus(err)
	}
	s.Log("info", "subscription altered: "+sub.Name)
	s.refreshSubscriptionCounts()
	s.track("subscription_altered", map[string]any{
		"name_changed":     req.Name != nil,
		"url_changed":      req.Url != nil || len(req.Sources) != 0,
		"interval_changed": req.CustomUpdateIntervalMinutes != nil,
		"config_count":     len(sub.Configs),
	})
	return &pb.AlterSubscriptionResponse{Subscription: s.toPBSubscription(sub)}, nil
}

func (s *Server) RemoveSubscription(ctx context.Context, req *pb.RemoveSubscriptionRequest) (*pb.RemoveSubscriptionResponse, error) {
	if err := s.subs.Remove(ctx, req.Id); err != nil {
		return nil, toStatus(err)
	}
	s.Log("info", "subscription removed: "+req.Id)
	s.refreshSubscriptionCounts()
	s.track("subscription_removed", map[string]any{})
	return &pb.RemoveSubscriptionResponse{}, nil
}

func (s *Server) UpdateSubscription(ctx context.Context, req *pb.UpdateSubscriptionRequest) (*pb.UpdateSubscriptionResponse, error) {
	sub, err := s.subs.Update(ctx, req.Id)
	if err != nil {
		return nil, toStatus(err)
	}
	s.refreshSubscriptionCounts()
	return &pb.UpdateSubscriptionResponse{Subscription: s.toPBSubscription(sub)}, nil
}

func (s *Server) TestSubscriptionLatency(req *pb.TestSubscriptionLatencyRequest, stream pb.DaemonService_TestSubscriptionLatencyServer) error {
	ctx := stream.Context()
	err := s.subs.TestLatency(ctx, req.SubscriptionId, req.ConfigIds, req.ProbeUrl, s.probeTunName, func(id string, l subs.Latency) {
		// A send fails only once the client is gone, which also cancels ctx and stops the test.
		_ = stream.Send(&pb.TestSubscriptionLatencyResponse{ConfigId: id, Result: toPBLatency(&l)})
	})
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return toStatus(err)
	}
	return nil
}

func (srv *Server) toPBSubscription(s *subs.Subscription, includeConfig ...bool) *pb.Subscription {
	out := &pb.Subscription{
		Id:                          s.ID,
		Name:                        s.Name,
		Url:                         s.URL,
		Sources:                     s.SourceURLs(),
		CreatedAtMs:                 s.CreatedAt.UnixMilli(),
		UpdatedAtMs:                 unixMilli(s.UpdatedAt),
		CheckedAtMs:                 unixMilli(s.CheckedAt),
		LastError:                   s.LastError,
		UpdateIntervalMinutes:       uint32(srv.subs.Interval(s).Minutes()),
		CustomUpdateIntervalMinutes: s.CustomIntervalMinutes,
		ConfigCount:                 uint32(len(s.Configs)),
	}
	if len(includeConfig) > 0 && includeConfig[0] && len(s.Configs) > 0 {
		out.Configs = make([]*pb.SubscriptionConfig, len(s.Configs))
		for i := range s.Configs {
			out.Configs[i] = srv.toPBConfig(s.ID, &s.Configs[i])
		}
	}
	if u := s.Usage; u != nil {
		out.Usage = &pb.Usage{
			UploadBytes:   u.Upload,
			DownloadBytes: u.Download,
			TotalBytes:    u.Total,
			ExpireAtMs:    unixMilli(u.Expire),
		}
	}
	return out
}

func (s *Server) toPBConfig(subID string, c *subs.Config) *pb.SubscriptionConfig {
	var up, down uint64
	if s != nil {
		tr := s.serverTraffic(subID, c.ID)
		up, down = tr.Up, tr.Down
	}
	strategy, members := c.Profile.BalancerSummary()
	return &pb.SubscriptionConfig{
		Id:               c.ID,
		Name:             c.Name,
		Protocol:         c.Protocol,
		Address:          c.Address,
		Port:             uint32(c.Port),
		Network:          c.Network,
		Security:         c.Security,
		OutboundJson:     string(c.Outbound),
		Latency:          toPBLatency(c.Latency),
		UploadBytes:      up,
		DownloadBytes:    down,
		BalancerStrategy: strategy,
		BalancerMembers:  members,
	}
}

func toPBLatency(l *subs.Latency) *pb.LatencyResult {
	if l == nil {
		return nil
	}
	return &pb.LatencyResult{LatencyMs: l.Ms, Error: l.Error, TestedAtMs: unixMilli(l.TestedAt)}
}

func (s *Server) GetSubscriptionConfigJSON(ctx context.Context, req *pb.GetSubscriptionConfigJSONRequest) (*pb.GetSubscriptionConfigJSONResponse, error) {
	cfg, err := s.subs.Config(req.SubscriptionId, req.ConfigId)
	if err != nil {
		return nil, toStatus(err)
	}
	if len(cfg.SourceJSON) == 0 {
		return &pb.GetSubscriptionConfigJSONResponse{}, nil
	}
	// Remove store indentation without decoding numbers or changing source values.
	// The inspection client allows the worst-case sixfold JSON escape expansion.
	var compact bytes.Buffer
	if err := json.Compact(&compact, cfg.SourceJSON); err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetSubscriptionConfigJSONResponse{Json: compact.String()}, nil
}
