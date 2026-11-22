package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Gozargah/Masir/libs/masir-core/paths"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/compose"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/subs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/userfs"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	"github.com/Gozargah/Masir/libs/masir-daemon/ports"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// change edits a copy of the state, applies it, and saves it only once it runs.
func (s *Server) change(ctx context.Context, msg string, fn func(*store.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state.Clone()
	if err := fn(&next); err != nil {
		return toStatus(err)
	}
	if err := s.applyLocked(ctx, next, true); err != nil {
		if next.Settings.KillSwitch != s.state.Settings.KillSwitch {
			_ = s.applyLocked(context.WithoutCancel(ctx), s.state, false)
		}
		return err
	}
	if err := store.Save(s.dir, next); err != nil {
		_ = s.applyLocked(context.WithoutCancel(ctx), s.state, false) // keep what runs and what's saved in agreement
		return status.Error(codes.Internal, "save state: "+err.Error())
	}
	s.state = next
	if msg != "" {
		s.Log("info", msg)
	}
	return nil
}

// applyLocked composes st with the current selection and brings Xray to it:
// running when power or always-on proxy is on (and the daemon isn't idle), stopped
// otherwise. Expects s.mu held.
//
// attended says a caller is waiting on the result and can be asked what to do about rules
// that can't be built (see gate); background applies pass false.
func (s *Server) applyLocked(ctx context.Context, st store.State, attended bool) error {
	return s.applyLockedWithCollection(ctx, st, attended, true)
}

// Selection changes collect before switching the active config.
func (s *Server) applyLockedWithCollection(ctx context.Context, st store.State, attended, collect bool) error {
	active, err := s.subs.Active()
	if err != nil {
		return status.Error(codes.Internal, "read selection: "+err.Error())
	}
	runs := st.Settings.XrayRuns() && !s.idle.Load()
	tun := runs && st.Settings.Power && st.Settings.Tun && s.id.Privileged
	skip, err := s.gate(st, len(active.Outbound) > 0 || active.Profile != nil, runs, attended)
	if err != nil {
		return err
	}
	in := compose.Input{
		Skip:                 skip,
		State:                st,
		Proxy:                runs,
		Tun:                  tun,
		LogDir:               s.logDir(),
		Active:               active.Outbound,
		ActiveProfile:        active.Profile,
		ActiveID:             active.ConfigID,
		ActiveSubscriptionID: active.SubscriptionID,
		Resolve:              s.resolveTarget,
	}
	if tun {
		in.TunName = s.tunName()
	}
	out, err := compose.Build(in)
	if err != nil {
		return status.Error(codes.Internal, "compose: "+err.Error())
	}
	if s.id.Privileged {
		// Root runs configs it didn't write (a subscription's servers, custom inbounds):
		// none may point Xray at a file.
		if err := xray.CheckNoLocalFiles(out.Config); err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	p, err := xray.ParseProfile(out.Config)
	if err != nil {
		return status.Error(codes.Internal, "compose: "+err.Error())
	}

	if collect {
		s.collectTrafficLocked(ctx)
	}

	wasRunning := s.xray.Running()
	if runs && !wasRunning {
		// Xray reports a taken port only after its API feature has started, and a Close
		// right then races xray-core's commander. Checking ports first turns that crash
		// into the error the person would have seen.
		if err := s.handoverPatience(st, probePorts(st)); err != nil {
			mode := "proxy"
			if tun {
				mode = "tun"
			}
			s.trackErrorLocked(st, "xray_apply_failed", err, operationProps(ctx, map[string]any{"mode": mode}), true)
			return status.Error(codes.Internal, err.Error())
		}
	}
	protectedTun := ""
	if tun && st.Settings.KillSwitch {
		protectedTun = in.TunName
	}
	var nextProfile *xray.Profile
	if runs {
		nextProfile = p
	}
	if err := s.applyXray(ctx, nextProfile, protectedTun); err != nil {
		mode := "off"
		if tun {
			mode = "tun"
		} else if runs {
			mode = "proxy"
		}
		s.trackErrorLocked(st, "xray_apply_failed", err, operationProps(ctx, map[string]any{"mode": mode}), true)
		return err
	}
	s.resolved = out.Rules
	s.running = running{proxy: runs, tun: tun, tunName: in.TunName}
	if !wasRunning && runs {
		mode := "proxy"
		if tun {
			mode = "tun"
		}
		s.trackLocked(st, "xray_started", map[string]any{"mode": mode})
	} else if wasRunning && !runs {
		s.trackLocked(st, "xray_stopped", map[string]any{"reason": "state"})
	}
	if runs {
		host := "127.0.0.1"
		if st.Settings.AllowLAN {
			host = "0.0.0.0"
		}
		s.running.proxyAddr = net.JoinHostPort(host, strconv.Itoa(st.Settings.ProxyPort))
	}
	defer s.changes.notify()

	// xray.json is output: what runs (or would), for people to read. Never read back.
	if raw, err := p.Marshal(); err == nil {
		s.config = raw
		if err := userfs.Write(filepath.Join(s.dir, "xray.json"), raw); err != nil {
			s.Log("warn", "write xray.json: "+err.Error())
		}
	}
	return nil
}

// tunName keeps the interface going while it runs. Xray can't rename a TUN through its
// API, so picking a new number would mean tearing the interface and its routes down and
// back up on every apply, dropping every connection that goes through it.
func (s *Server) tunName() string {
	if s.running.tun && s.running.tunName != "" {
		return s.running.tunName
	}
	return compose.TunName()
}

// reconcileLocked applies the saved state when no caller is waiting on the result (boot,
// subscription refreshes, retries). A failure is kept in lastErr for the status and the
// retry loop, since nothing else would report it. Expects s.mu held.
func (s *Server) reconcileLocked(ctx context.Context) error {
	err := s.applyLocked(ctx, s.state, false)
	prev := s.lastErr
	s.lastErr = ""
	if err != nil {
		s.lastErr = status.Convert(err).Message()
		if s.lastErr != prev {
			s.Log("error", "apply config: "+s.lastErr)
		}
	} else if prev != "" {
		s.Log("info", "config applied after earlier failure")
	}
	if s.lastErr != prev {
		s.changes.notify()
	}
	return err
}

// subscriptionsChanged rebuilds after a refresh or removal: pinned configs may have
// moved, fallen back to proxy, or the selection may have followed a rotated config.
func (s *Server) subscriptionsChanged(ctx context.Context) {
	s.mu.Lock()
	_ = s.reconcileLocked(ctx)
	s.mu.Unlock()
	s.refreshSubscriptionCounts()
}

func (s *Server) resolveTarget(t store.Target) (subs.Config, bool) {
	return s.subs.Find(t.SubscriptionID, t.ConfigID, t.Name)
}

func toStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err // already a gRPC status
	}
	switch {
	case errors.Is(err, store.ErrInvalid), errors.Is(err, subs.ErrInvalid), errors.Is(err, xray.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrNotFound), errors.Is(err, subs.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrExists), errors.Is(err, subs.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, store.ErrInUse), errors.Is(err, xray.ErrNotRunning):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, subs.ErrFetch):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// logDir is where Xray writes its error log. A root daemon keeps it where only root can
// write: Xray opens the file by path, and in the user's directory a link could point it
// at any file. Without a safe place the log stays on stdout.
func (s *Server) logDir() string {
	if !s.id.Privileged {
		return s.dir
	}
	dir := paths.PrivilegedLogDir(s.id.UID)
	if dir == "" { // Windows keeps it with the daemon's other files
		return s.dir
	}
	// The run directory is shared with the socket, which the user must reach: 0755.
	if paths.EnsureRootDir(paths.PrivilegedRunDir, 0, 0o755) != nil || paths.EnsureRootDir(dir, 0, 0o700) != nil {
		log.Printf("xray's error log is off: %s isn't a directory only root controls", dir)
		return ""
	}
	return dir
}

// handoverWait bounds how long a boot gives the daemon it replaced to close its
// listeners. Xray is closed in BeginShutdown before that daemon answers, and a process
// that has been signalled needs a moment more to be reaped; past this the port belongs to
// somebody else and the failure is real.
//
// It is a variable so the tests that must see a real timeout can shorten it.
var handoverWait = 5 * time.Second

// handoverPatience waits out a port the daemon this one replaced is still letting go of.
//
// A supervisor's stop answers before the old daemon has closed Xray's listeners: launchd
// returns once the job is gone, systemd once the unit is dead, and the app starts the
// bundle daemon the moment the removal returns. So the new daemon can probe the proxy port
// a beat too early and report "address already in use" about masir's own proxy — the
// failure a person saw while removing the service, which no other program had caused.
//
// It only waits when this daemon holds the proxy, i.e. it starts it: a config change made
// later (a new port typed into Settings, an inbound added) reports a busy port at once,
// because then there is no previous daemon to blame and a wait would only hide it.
func (s *Server) handoverPatience(st store.State, err error) error {
	if err == nil || !s.heldPorts.Load() {
		return err
	}
	// One wait per daemon. A port that stays taken is another program's, and every later
	// apply (a retry, a settings change) must report it instead of sitting through five
	// more seconds of it.
	s.heldPorts.Store(false)
	if waitErr := ports.WaitFree(st, handoverWait); waitErr != nil {
		return errors.Join(err, fmt.Errorf("still taken after %s: %w", handoverWait, waitErr))
	}
	s.Log("info", "the daemon this one replaced let go of its ports")
	return nil
}

// probePorts fails when a port Xray will bind is already taken, before it starts. A bind
// failure after Xray's API feature has started makes xray-core's commander race its own
// Close (it reads its gRPC server from a goroutine while Close nils it), which is a
// nil-pointer crash in a goroutine the daemon can't recover. See the call in
// applyLockedWithCollection.
//
// The addresses live in ports.Busy, which the CLI also asks while a service hands over: a
// port this daemon will bind is a port the previous one has to have let go of.
func probePorts(st store.State) error { return ports.Busy(st) }

// applyXray keeps the shutdown check and start atomic with shutdown's final Close.
func (s *Server) applyXray(ctx context.Context, p *xray.Profile, protectedTun string) error {
	s.xrayMu.Lock()
	defer s.xrayMu.Unlock()
	if s.shuttingDown.Load() {
		return status.Error(codes.Unavailable, "the daemon is shutting down")
	}
	if protectedTun != "" {
		if err := s.firewall.Enable(context.WithoutCancel(ctx), protectedTun); err != nil {
			return status.Error(codes.FailedPrecondition, "enable Kill Switch: "+err.Error())
		}
	}
	var err error
	if p == nil {
		err = s.xray.Close()
	} else {
		err = s.xray.Apply(ctx, p)
	}
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if protectedTun != "" {
		if err := s.firewall.AllowTunnel(context.WithoutCancel(ctx), protectedTun); err != nil {
			return status.Error(codes.FailedPrecondition, "allow Kill Switch tunnel: "+err.Error())
		}
	}
	if s.id.Privileged && !s.idle.Load() && protectedTun == "" {
		if err := s.firewall.Disable(context.WithoutCancel(ctx)); err != nil {
			return status.Error(codes.Internal, "disable Kill Switch: "+err.Error())
		}
	}
	return nil
}
