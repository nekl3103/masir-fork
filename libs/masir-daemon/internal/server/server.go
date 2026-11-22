package server

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/analytics"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/compose"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/identity"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/rulecheck"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	"github.com/Gozargah/Masir/libs/masir-daemon/killswitch"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// retryEvery is how often a config that failed to apply in the background is retried.
const retryEvery = 15 * time.Second

type Server struct {
	pb.UnimplementedDaemonServiceServer
	xray      *xray.Core
	id        identity.Identity
	startedAt time.Time

	// mu serializes every change to state, the selection and the running config.
	mu       sync.Mutex
	xrayMu   sync.Mutex // serializes the shutdown check/start with the final Close
	dir      string
	state    store.State
	resolved []compose.ResolvedRule // user rules as last applied
	running  running                // what the last successful apply brought up
	lastErr  string                 // why what runs doesn't match the state, from a background apply
	config   []byte                 // the composed Xray config, as written to xray.json
	// ruleProblems are the enabled rules the geo data can't build, as of the last check
	// (see gate); suspended is the subset the running config leaves out.
	ruleProblems []rulecheck.Problem
	suspended    map[string]bool

	testMu sync.Mutex
	tests  map[string]context.CancelFunc // test ID -> its cancel function

	analytics    *analytics.Client
	connectivity connectivityMetrics

	logs    *hub
	changes *signal
	subs    *subs.Manager // set by Boot

	upd          *updates    // set by Boot
	restart      atomic.Bool // the daemon stopped to start a newly installed version
	stopTracked  sync.Once   // daemon_stopped is recorded once
	subCount     atomic.Int64
	totalConfigs atomic.Int64
	// heldPorts is true from Boot until the first apply finishes or BeginShutdown closes
	// Xray. It is what makes a port that is still taken during boot the daemon this one
	// replaced, on its way out (a service being removed, a root daemon taking over),
	// instead of a failure to report. It ends at the first apply so a later start (after
	// Power went off) reports a busy port at once.
	heldPorts atomic.Bool
	firewall  killswitch.Firewall // synchronized by mu with the applied TUN policy
	// shuttingDown is set when BeginShutdown begins. An apply that is waiting for a port
	// (or building a config) must not bring Xray back up after shutdown closed it.
	shuttingDown atomic.Bool
	// idle keeps Xray down, power and always-on proxy alike, until a client turns
	// masir on: a daemon the CLI started for other work (subscriptions, rules,
	// updates) shouldn't start proxying.
	idle atomic.Bool

	stopOnce sync.Once
	stopped  chan struct{}

	geoMu    sync.RWMutex
	geoCache map[string]geoCacheEntry

	collectMu          sync.Mutex // ordered after mu; serializes collection and attribution
	trafficMu          sync.Mutex
	lastXrayGen        uint64
	cumulativeOutbound map[string]xray.Traffic
	cumulativeServer   map[string]xray.Traffic // key: "subID/configID"
	lastXrayOutbound   map[string]xray.Traffic
}

type running struct {
	proxy     bool
	proxyAddr string
	tun       bool
	tunName   string // kept while the TUN runs: Xray can't rename one
}

func New(xc *xray.Core, id identity.Identity) *Server {
	if xc == nil {
		panic("server.New: xray core is nil")
	}
	s := &Server{
		xray:               xc,
		id:                 id,
		startedAt:          time.Now(),
		state:              store.State{Settings: store.DefaultSettings()},
		logs:               newHub(),
		changes:            newSignal(),
		tests:              map[string]context.CancelFunc{},
		stopped:            make(chan struct{}),
		geoCache:           map[string]geoCacheEntry{},
		cumulativeOutbound: map[string]xray.Traffic{},
		cumulativeServer:   map[string]xray.Traffic{},
		lastXrayOutbound:   map[string]xray.Traffic{},
	}
	xray.SetLogSink(s.Log)
	return s
}

// Boot loads state from dir, brings Xray to what the settings ask for, and until ctx is
// done keeps subscriptions fresh and retries a config that failed to start. It fails
// only if state can't be loaded, so a UI can still connect and fix a broken config.
func (s *Server) Boot(ctx context.Context, dir string) error {
	st, err := store.Load(dir)
	if err != nil {
		return err
	}
	m, err := subs.NewManager(filepath.Join(dir, "subscriptions"), filepath.Join(dir, "active.json"), s.xray.Dial)
	if err != nil {
		return err
	}
	m.Log = s.Log
	m.OnChange = s.subscriptionsChanged
	m.BuildProbeProfile = func(c subs.Config) ([]byte, error) {
		state := store.State{Settings: store.DefaultSettings()}
		state.Settings.Fallback = store.FallbackProxy
		out, err := compose.Build(compose.Input{State: state, ActiveProfile: c.Profile})
		if err != nil {
			return nil, err
		}
		if err := xray.CheckNoLocalFiles(out.Config); err != nil {
			return nil, err
		}
		return out.Config, nil
	}
	m.SetInterval(time.Duration(st.Settings.SubIntervalMinutes) * time.Minute)
	s.subs = m
	s.refreshSubscriptionCounts()

	// An idle daemon is one a short command started for its own work. It leaves Xray
	// down, but it must not change what the person chose: the saved power and tunnel
	// state come back when a daemon that isn't idle starts. status() reports power as
	// off while idle, so a UI doesn't show "Connecting..." for a daemon that may never
	// start Xray.
	s.initUpdates(dir)
	s.mu.Lock()
	s.dir, s.state = dir, st
	s.heldPorts.Store(true)
	s.analytics = analytics.New(analytics.Options{
		Dir:     dir,
		Version: Version,
		Enabled: st.Settings.Analytics,
		Proxy:   s.xray,
	})
	if err := s.reconcileLocked(ctx); err != nil {
		log.Printf("start xray: %v", err)
	}
	// Boot is the handover, and it ends with this first apply: whatever the daemon this
	// one replaced was holding, it has either let go or the apply above reported it. A
	// later start (Power off, then on again) must report a busy port at once instead of
	// waiting out the patience again for a predecessor that is long gone.
	s.heldPorts.Store(false)
	s.mu.Unlock()
	s.track("daemon_started", map[string]any{
		"idle":       s.idle.Load(),
		"privileged": s.id.Privileged,
	})

	s.analytics.VerifyUpdateRestart(Version)

	go m.Run(ctx)
	go s.retryFailed(ctx)
	go s.maintainUpdates(ctx)
	go s.analyticsLoop(ctx)
	return nil
}

// StartIdle makes Boot leave Xray down and masir off, until SetPower turns it on or
// always-on proxy is switched on. Call it before Boot.
func (s *Server) StartIdle() { s.idle.Store(true) }

// Idle reports whether the daemon is still idle.
func (s *Server) Idle() bool { return s.idle.Load() }

// Stopped is closed when a client asks the daemon to shut down.
func (s *Server) Stopped() <-chan struct{} { return s.stopped }

// Restart ends the daemon so it starts the binary on disk in its place, as after a
// deferred update.
func (s *Server) Restart(_ context.Context, _ *pb.RestartRequest) (*pb.RestartResponse, error) {
	s.Log("info", "restart requested")
	s.restartForUpdate()
	return &pb.RestartResponse{}, nil
}

func (s *Server) Shutdown(ctx context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.Log("info", "shutdown requested")
	s.TrackDaemonStopped("client")
	s.BeginShutdown()
	return &pb.ShutdownResponse{}, nil
}

// BeginShutdown tears down what the daemon serves and tells its clients to go: every
// stream ends, so a graceful stop can finish. Shutdown uses it after saying why; a
// supervisor's signal (launchd's bootout, systemd's stop) uses it directly, because
// nobody is waiting for a reply there and an open stream would hold the process — and
// the service manager waiting for it — until it's killed. It is safe to call twice.
func (s *Server) BeginShutdown() {
	// Set before anything that can wait on s.mu: an apply holding mu may be waiting for a
	// port or building a config, and it must not start Xray once this closes it. An apply
	// that already passed the check holds xrayMu, so the final Close waits for it.
	s.shuttingDown.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.collectTraffic(ctx)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	// Tear the TUN and proxy down before answering. A new daemon waits for the reply
	// before taking the socket, so returning first would let it start while the old TUN
	// still held its address.
	s.xrayMu.Lock()
	s.heldPorts.Store(false)
	_ = s.xray.Close()
	s.xrayMu.Unlock()
	// A latency test is a call with nothing to watch `stopped`: cancel it, or the
	// graceful stop waits for a probe that can take a while.
	s.testMu.Lock()
	for _, cancel := range s.tests {
		cancel()
	}
	s.testMu.Unlock()
	s.stopOnce.Do(func() { close(s.stopped) })
}

func (s *Server) retryFailed(ctx context.Context) {
	t := time.NewTicker(retryEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		if s.lastErr != "" {
			_ = s.reconcileLocked(ctx)
		}
		s.mu.Unlock()
	}
}

var Version = "dev" // overridden via -ldflags "-X .../server.Version=0.1.0"

func (s *Server) GetXrayVersion(ctx context.Context, req *pb.GetXrayVersionRequest) (*pb.GetXrayVersionResponse, error) {
	return &pb.GetXrayVersionResponse{
		Version: s.xray.GetVersion(),
	}, nil
}

func (s *Server) GetXrayConfig(ctx context.Context, _ *pb.GetXrayConfigRequest) (*pb.GetXrayConfigResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pb.GetXrayConfigResponse{Json: string(s.config)}, nil
}
