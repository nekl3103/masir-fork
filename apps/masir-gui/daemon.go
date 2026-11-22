package main

import (
	"context"
	"log"
	"sync"
	"time"

	coreanalytics "github.com/Gozargah/Masir/libs/masir-core/analytics"
	"github.com/Gozargah/Masir/libs/masir-core/client"
	"github.com/Gozargah/Masir/libs/masir-core/paths"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
	"github.com/egoist/mygo"
	"google.golang.org/grpc"
)

func (a *app) setPower(on bool) {
	if a.busy != "" || a.serviceBusy || a.busyUpdate() || a.updates.state.restarting {
		return
	}
	a.connection.checked = false
	a.connection.generation++
	a.busy = map[bool]string{true: tr("Connecting…"), false: tr("Disconnecting…")}[on]
	a.async(func() {
		err := a.power(on)
		a.post(func() {
			a.busy = ""
			if err != nil {
				a.fail(tr(map[bool]string{true: "Couldn't connect: ", false: "Couldn't disconnect: "}[on]), err)
			}
		})
	})
}

// The socket check and the permission-prompting commands, replaceable in tests so they
// never touch the machine they run on.
var (
	daemonAlive     = client.Alive
	startRootDaemon = elevateDaemon
	startServiceNow = startService
)

func (a *app) power(on bool) error {
	if on && !a.connectable() {
		if err := a.startDaemon(); err != nil {
			return err
		}
	}
	ctx, cancel := rpc()
	defer cancel()
	if on {
		// Both default routes use TUN so per-app and other routing rules apply.
		// Check TUN before turning power on, so a refusal doesn't leave power on.
		if _, err := a.d.SetTunEnabled(ctx, &pb.SetTunEnabledRequest{Enabled: true}); err != nil {
			return err
		}
	}
	if _, err := a.d.SetPower(ctx, &pb.SetPowerRequest{On: on}); err != nil {
		return err
	}
	return nil
}

// disconnectOnQuit turns masir off as `masir disconnect` does when the app is really
// going away. Hiding the window in the tray is not a quit, and an update's relaunch is
// not one either: the app comes back, so neither turns masir off.
func (a *app) disconnectOnQuit() {
	if !a.disconnectQuit {
		return
	}
	// A development build leaves masir as it is: the next `go run` or
	// `mygo dev` should find the daemon it was using, not a stopped one.
	// Packaged builds (mygo build sets production) still disconnect.
	if mygo.IsDev() {
		return
	}
	if a.updates.state.relaunching || !a.wantsOn() {
		return
	}
	// Only power, so Always-on Proxy keeps its port up. A daemon failure here must not
	// hold the quit (it may already be gone).
	ctx, cancel := context.WithTimeout(context.Background(), quitTimeout)
	defer cancel()
	if _, err := a.d.SetPower(ctx, &pb.SetPowerRequest{On: false}); err != nil {
		log.Print("disconnect on quit: ", err)
	}
}

func (a *app) privileged() bool { return a.status != nil && a.status.Privileged }

// connectable reports whether a privileged daemon answers right now. The socket check
// catches a daemon that stopped since the last status; the status itself is main-thread
// only, hence the hop.
func (a *app) connectable() bool {
	if !daemonAlive() {
		return false
	}
	var ok bool
	a.post(func() { ok = a.privileged() })
	return ok
}

// startDaemon brings up a privileged daemon to connect through. An installed service is
// the supervisor, so it is started (asking for permission) instead of a second,
// unsupervised daemon. Without a service, this session gets a root daemon that takes over
// from the one running as the user.
func (a *app) startDaemon() error {
	if readService().Installed {
		// Ask the service manager rather than trust its cached state: the service may
		// have stopped a moment ago, and a kickstart is harmless when it is up.
		if !daemonAlive() {
			if err := a.runServiceAction(); err != nil {
				return err
			}
		}
		return a.waitForRoot(tr("the background service didn't start with root permission"))
	}
	return a.becomePrivileged()
}

// runServiceAction starts the installed service, keeping the daemon watch from starting
// one of its own while the service comes up.
func (a *app) runServiceAction() error {
	a.post(func() { a.serviceBusy = true })
	err := startServiceNow(a.daemonPath())
	a.post(func() {
		a.serviceBusy = false
		a.serviceChecked = time.Time{}
		a.refreshServiceState()
	})
	return err
}

// becomePrivileged restarts the daemon as root for this session (see elevate), and waits
// for it to take over.
func (a *app) becomePrivileged() error {
	if err := startRootDaemon(a.daemonPath()); err != nil {
		return err
	}
	return a.waitForRoot(tr("the daemon didn't start with root permission"))
}

// waitForRoot waits until the daemon answers and reports that it runs as root.
func (a *app) waitForRoot(failure string) error {
	return client.WaitFor(context.Background(), func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res, err := a.d.GetStatus(ctx, &pb.GetStatusRequest{}, grpc.WaitForReady(true))
		return err == nil && res.Status.Privileged
	}, 30*time.Second, failure)
}

// ---- the daemon's lifecycle ----

// daemonStep is what the daemon watch does when nothing answers.
type daemonStep int

const (
	// daemonWait: an update or a service action is in flight. Its daemon is expected to
	// be down for a moment, and starting another would race it for the socket.
	daemonWait daemonStep = iota
	// daemonLeaveToService: a service definition exists. It is the supervisor, not this
	// app, so the daemon is left to it; connecting starts a stopped service.
	daemonLeaveToService
	// daemonStart: no service and no daemon, so the app starts one.
	daemonStart
)

// daemonStep answers the watch from another goroutine. The update and service state is
// main-thread only, so the question is asked there and the answer carried back; the
// service definition is read where it stands, so no OS query runs in a frame.
func (a *app) daemonStep() daemonStep {
	var busy bool
	a.post(func() { busy = a.busyUpdate() || a.updates.state.restarting || a.serviceBusy })
	switch {
	case busy:
		return daemonWait
	case serviceDefined():
		return daemonLeaveToService
	}
	return daemonStart
}

// watch keeps a status stream open for as long as ctx lives: it starts the daemon when
// none answers, and dials again when the daemon goes away or is replaced (an update, or
// a root daemon taking over).
func (a *app) watch(ctx context.Context) {
	var once sync.Once
	var failed int // consecutive failed starts
	warn := func(msg string) {
		once.Do(func() { a.post(func() { a.notifyError(msg) }) })
	}
	for ctx.Err() == nil {
		if !client.Alive() {
			a.post(a.setUnreachable)
			switch a.daemonStep() {
			case daemonWait:
				// An update or a service action is restarting the daemon. Starting
				// another one here would race it for the socket, and an idle start
				// would look like the person turned masir off.
				sleep(ctx, time.Second)
				continue
			case daemonLeaveToService:
				// A service exists: it is the supervisor, not this app. Starting a
				// second, unsupervised daemon would fight it for the socket.
				sleep(ctx, 3*time.Second)
				continue
			}
			bin := a.daemonPath()
			if bin == "" {
				warn(tr("Couldn't start the masir daemon: the app ships no masir executable"))
				sleep(ctx, 5*time.Second)
				continue
			}
			// Normal mode, not idle: the daemon honors the saved power and tunnel
			// settings, so an app restart doesn't silently turn masir off.
			if err := client.Start(ctx, bin, false); err != nil {
				if dir, e := paths.ConfigDir(); e == nil {
					coreanalytics.StartupFailure(dir, version, "gui", "daemon_start", err)
				}
				warn(tr("Couldn't start the masir daemon: ") + err.Error())
				// Start fails as soon as the daemon does, so back off rather than spawn
				// a broken daemon every three seconds.
				failed++
				sleep(ctx, retryDelay(failed))
				continue
			}
			failed = 0
		}
		stream, err := a.d.WatchStatus(ctx, &pb.WatchStatusRequest{}, grpc.WaitForReady(true))
		if err != nil {
			sleep(ctx, time.Second)
			continue
		}
		for {
			res, err := stream.Recv()
			if err != nil {
				break
			}
			a.post(func() {
				previous, _ := a.activeLatencyTarget()
				a.applyStatus(res.Status)
				if current, ok := a.activeLatencyTarget(); ok && current != previous {
					a.refreshActiveLatency(ctx)
				}
			})
		}
		a.post(a.setUnreachable)
		sleep(ctx, time.Second)
	}
}

// retryDelay is how long the watch waits after failed consecutive daemon starts:
// 3s, 6s, 12s, 24s, then 30s from the fifth failure on. The exponent is capped
// before the shift, so the delay can never overflow to a negative duration (which
// would turn the backoff into a zero-delay respawn loop).
func retryDelay(failed int) time.Duration {
	return min(3*time.Second<<min(failed-1, 4), 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
