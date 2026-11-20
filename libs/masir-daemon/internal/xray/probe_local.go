package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	applog "github.com/xtls/xray-core/app/log"
	corenet "github.com/xtls/xray-core/common/net"
	xserial "github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
)

const DefaultProbeURL = "https://www.google.com/generate_204"

type LatencyOptions struct {
	CurrentTunName  func() string `json:"-"` // snapshot after acquiring the worker slot
	TunName         string        // active Masir TUN to exclude from the probe route
	URL             string        // probe URL; empty means DefaultProbeURL
	Attempts        int           // requests per config; the fastest wins (default 2)
	Timeout         time.Duration // limit for one request (default 12s)
	BalancedProfile bool          // use Masir’s managed routing and observatory
	Full            bool          // keep dns/routing/policy instead of stripping them
}

func (o LatencyOptions) withDefaults() LatencyOptions {
	if o.URL == "" {
		o.URL = DefaultProbeURL
	}
	if o.Attempts < 1 {
		o.Attempts = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 12 * time.Second
	}
	return o
}

// Only these app configs survive when Full is false.
var essentialApps = map[string]bool{
	"xray.app.proxyman.OutboundConfig": true,
	"xray.app.dispatcher.Config":       true,
	"xray.app.log.Config":              true,
}

// measureDelayLocal starts a temporary Xray instance from cfgJSON, sends requests
// through its first outbound, and returns the fastest round trip.
// Cancelling ctx aborts the test; the instance is always closed.
func measureDelayLocal(ctx context.Context, cfgJSON []byte, opt LatencyOptions) (time.Duration, error) {
	opt = opt.withDefaults()

	if opt.BalancedProfile {
		p, err := ParseProfile(cfgJSON)
		if err != nil {
			return 0, err
		}
		p.inbounds = nil
		p.base["log"] = json.RawMessage(`{"loglevel":"none"}`)
		engine := New("")
		defer engine.Close()
		if err := engine.Apply(ctx, p); err != nil {
			return 0, err
		}

		return measureThrough(ctx, engine.inst, "", opt)
	}

	cfg, err := loadConfig(cfgJSON)
	if err != nil {
		return 0, fmt.Errorf("load config: %w", err)
	}
	if len(cfg.Outbound) == 0 {
		return 0, errors.New("config has no outbounds")
	}

	quietProbeLogs(cfg)
	cfg.Inbound = nil // nothing listens on any port
	if !opt.Full {
		keep := cfg.App[:0] // filter in place, reusing the same backing array
		for _, app := range cfg.App {
			if essentialApps[app.Type] {
				keep = append(keep, app)
			}
		}
		cfg.App = keep
	}

	// core.New and Start can't be cancelled, so check first.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	inst, err := core.New(cfg)
	if err != nil {
		return 0, fmt.Errorf("create instance: %w", err)
	}
	// The helper process owns all of this instance's resources.
	defer inst.Close()
	if err := inst.Start(); err != nil {
		return 0, fmt.Errorf("start instance: %w", err)
	}

	return measureThrough(ctx, inst, "", opt)
}

// Probe is one outbound to latency-test; Tag only identifies it in results.
type Probe struct {
	Tag      string
	Outbound json.RawMessage
}

const maxProbeBatch = 16

// measureEachLocal tests many outbounds on temporary Xray instances in bounded batches,
// up to workers at a time.
// report gets exactly one call per probe (never concurrently), even when ctx is cancelled.
func measureEachLocal(ctx context.Context, probes []Probe, opt LatencyOptions, workers int, report func(tag string, d time.Duration, err error)) {
	opt = opt.withDefaults()
	if workers < 1 {
		workers = 1
	}
	var mu sync.Mutex
	send := func(tag string, d time.Duration, err error) {
		mu.Lock()
		defer mu.Unlock()
		report(tag, d, err)
	}

	// An outbound that doesn't build would stop the whole instance from starting.
	var valid []Probe
	for _, p := range probes {
		it, err := newItem(p.Outbound, "tag", p.Tag)
		if err == nil {
			_, err = buildOutbound(it.JSON)
		}
		if err != nil {
			send(p.Tag, 0, fmt.Errorf("invalid outbound: %w", err))
			continue
		}
		valid = append(valid, p)
	}
	if len(valid) == 0 {
		return
	}

	for i := 0; i < len(valid); i += maxProbeBatch {
		if ctx.Err() != nil {
			for _, p := range valid[i:] {
				send(p.Tag, 0, ctx.Err())
			}
			break
		}
		end := min(i+maxProbeBatch, len(valid))
		measureBatch(ctx, valid[i:end], opt, workers, send)
	}
}

func measureBatch(ctx context.Context, batch []Probe, opt LatencyOptions, workers int, send func(string, time.Duration, error)) {
	outs := make([]json.RawMessage, 0, len(batch))
	for _, p := range batch {
		it, _ := newItem(p.Outbound, "tag", p.Tag)
		outs = append(outs, it.JSON)
	}

	failBatch := func(err error) {
		for _, p := range batch {
			send(p.Tag, 0, err)
		}
	}

	cfg, err := loadConfig(marshal(map[string]any{"outbounds": outs}))
	if err != nil {
		failBatch(fmt.Errorf("load config: %w", err))
		return
	}
	quietProbeLogs(cfg)
	cfg.Inbound = nil
	keep := cfg.App[:0]
	for _, app := range cfg.App {
		if essentialApps[app.Type] {
			keep = append(keep, app)
		}
	}
	cfg.App = keep
	inst, err := core.New(cfg)
	if err != nil {
		failBatch(fmt.Errorf("load instance: %w", err))
		return
	}
	// The helper process owns all of this instance's resources.
	defer inst.Close()
	if err := inst.Start(); err != nil {
		failBatch(fmt.Errorf("start instance: %w", err))
		return
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(workers, len(batch)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tag := range jobs {
				if err := ctx.Err(); err != nil {
					send(tag, 0, err)
					continue
				}
				d, err := measureThrough(ctx, inst, tag, opt)
				send(tag, d, err)
			}
		}()
	}
	for _, p := range batch {
		jobs <- p.Tag
	}
	close(jobs)
	wg.Wait()
}

// measureThrough probes via the outbound tagged tag, or Xray's default outbound when tag is empty.
func measureThrough(ctx context.Context, inst *core.Instance, tag string, opt LatencyOptions) (time.Duration, error) {
	tr := &http.Transport{
		TLSHandshakeTimeout: 6 * time.Second,
		// Instead of opening a real socket, hand the connection to Xray.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dest, err := corenet.ParseDestination(network + ":" + addr)
			if err != nil {
				return nil, err
			}
			if tag != "" {
				ctx = session.SetForcedOutboundTagToContext(ctx, tag)
			}
			if opt.BalancedProfile {
				ctx = session.ContextWithContent(ctx, &session.Content{SniffingRequest: session.SniffingRequest{
					Enabled: true, RouteOnly: true, OverrideDestinationForProtocol: []string{"http", "tls", "quic"},
				}})
			}
			return core.Dial(ctx, inst, dest)
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}

	best := time.Duration(-1)
	var lastErr error
	for i := 0; i < opt.Attempts; i++ {
		if err := ctx.Err(); err != nil {
			if best >= 0 {
				return best, nil // cancelled, but we already have a result
			}
			return 0, err
		}
		d, err := probeAttempt(ctx, client, opt)
		if err != nil {
			lastErr = err
			continue
		}
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0, lastErr
	}
	return best, nil
}

// Give observers time to publish their first result after a fast failed
// fallback request. The retries share one attempt's existing timeout.
func probeAttempt(ctx context.Context, client *http.Client, opt LatencyOptions) (time.Duration, error) {
	if !opt.BalancedProfile {
		return probeOnce(ctx, client, opt.URL, opt.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		delay, err := probeOnce(ctx, client, opt.URL, opt.Timeout)
		if err == nil {
			return delay, nil
		}
		client.CloseIdleConnections()
		select {
		case <-ctx.Done():
			return 0, err
		case <-tick.C:
		}
	}
}

// probeOnce is its own function so defer cancel() runs after every request,
// not once at the end of the loop.
func probeOnce(ctx context.Context, client *http.Client, url string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return 0, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return time.Since(start), nil
}

// Probe logs never open caller-supplied files or share the result stream.
func quietProbeLogs(cfg *core.Config) {
	for i, app := range cfg.App {
		if app.Type == "xray.app.log.Config" {
			cfg.App[i] = xserial.ToTypedMessage(&applog.Config{
				AccessLogType: applog.LogType_None,
				ErrorLogType:  applog.LogType_None,
			})
		}
	}
}
