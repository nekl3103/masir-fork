package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gozargah/Masir/libs/masir-core/paths"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/identity"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/store"
	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// A daemon that boots while the one it replaces is still closing Xray must wait for the
// port instead of reporting a failure. A supervisor's stop answers first, and the app
// starts this daemon the moment the removal returns, so the busy port is masir's own
// proxy on its way out — the "address already in use" a person saw after removing the
// service.
func TestBootWaitsForThePortThePreviousDaemonIsReleasing(t *testing.T) {
	port, release := busyPort(t)
	var st store.State
	st.Settings.ProxyPort = port

	s := newTestServer(t)
	s.state.Settings.Power = true
	s.state.Settings.ProxyPort = port
	s.heldPorts.Store(true)

	// Released after the wait has started, as an exiting Xray closes its listener.
	time.AfterFunc(150*time.Millisecond, release)
	done := make(chan error, 1)
	go func() { done <- s.handoverPatience(st, probePorts(st)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a port on its way out was reported as taken: %v", err)
		}
	case <-time.After(handoverWait + 5*time.Second):
		t.Fatal("the wait never returned")
	}
}

// The patience is for a handover only. A config change made while the daemon runs (a new
// proxy port, an inbound added) reports the busy port at once: there is no previous
// daemon to blame, and waiting would only hide another program holding it.
func TestABusyPortDuringAChangeIsReportedAtOnce(t *testing.T) {
	port, release := busyPort(t)
	defer release()

	s := newTestServer(t)
	s.heldPorts.Store(false) // Boot has finished; nothing is being handed over
	var st store.State
	st.Settings.ProxyPort = port

	start := time.Now()
	err := s.handoverPatience(st, probePorts(st))
	if err == nil {
		t.Fatal("a busy port during a change was accepted")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a change waited %s for a port, want an immediate report", elapsed)
	}
}

// The wait happens once per daemon. A port that stays taken is another program's, and a
// retry must say so immediately rather than sit through five more seconds each time.
func TestTheHandoverWaitIsUsedUpOnce(t *testing.T) {
	old := handoverWait
	handoverWait = 200 * time.Millisecond
	t.Cleanup(func() { handoverWait = old })

	port, release := busyPort(t)
	defer release()

	s := newTestServer(t)
	s.heldPorts.Store(true)
	var st store.State
	st.Settings.ProxyPort = port

	if err := s.handoverPatience(st, probePorts(st)); err == nil {
		t.Fatal("a port nobody released was accepted")
	}
	if s.heldPorts.Load() {
		t.Fatal("the daemon would wait again on the next apply")
	}
	start := time.Now()
	if err := s.handoverPatience(st, probePorts(st)); err == nil {
		t.Fatal("the second apply accepted the port")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("the second apply waited %s, want none", elapsed)
	}
}

// A free port needs no patience at all: the common case must not add a delay to boot.
func TestAFreePortDoesNotWait(t *testing.T) {
	s := newTestServer(t)
	s.heldPorts.Store(true)
	var st store.State
	st.Settings.ProxyPort = freePort(t)

	start := time.Now()
	if err := s.handoverPatience(st, probePorts(st)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a free port took %s", elapsed)
	}
	if !s.heldPorts.Load() {
		t.Fatal("the daemon used up its patience on a free port")
	}
}

// A boot that can't get the port still reports it, naming the port: the person has to
// know what to quit.
func TestAHandoverThatNeverFinishesNamesThePort(t *testing.T) {
	old := handoverWait
	handoverWait = 200 * time.Millisecond
	t.Cleanup(func() { handoverWait = old })

	port, release := busyPort(t)
	defer release()

	s := newTestServer(t)
	s.heldPorts.Store(true)
	var st store.State
	st.Settings.ProxyPort = port

	err := s.handoverPatience(st, probePorts(st))
	if err == nil {
		t.Fatal("a port held by someone else was accepted")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("the failure doesn't name the port: %v", err)
	}
	if !strings.Contains(err.Error(), "still taken") {
		t.Fatalf("the failure doesn't say it waited: %v", err)
	}
}

// The wait is bounded: a port held by another program fails the boot, it doesn't hang it.
func TestTheHandoverWaitIsBounded(t *testing.T) {
	if handoverWait > 30*time.Second {
		t.Fatalf("handoverWait = %s; a boot must not wait that long", handoverWait)
	}
}

// Boot is the handover, and it ends when this daemon holds the ports. A later start —
// Power off, then on again with something else on the port — reports the conflict at
// once instead of waiting out the patience a second time.
func TestBootEndsTheHandoverOnceItHoldsThePorts(t *testing.T) {
	old := handoverWait
	handoverWait = 5 * time.Second // long enough that a second wait would show
	t.Cleanup(func() { handoverWait = old })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir, err := paths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	st := store.State{Settings: store.DefaultSettings()}
	st.Settings.Power = true
	st.Settings.AlwaysOnProxy = false
	st.Settings.AllowLAN = false
	st.Settings.ProxyPort = freePort(t)
	if err := store.Save(dir, st); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(xray.New(""), identity.Identity{UID: os.Getuid(), GID: os.Getgid(), Name: "tester"})
	t.Cleanup(func() { _ = s.xray.Close() })
	if err := s.Boot(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if !s.xray.Running() {
		t.Fatal("setup: boot didn't start xray")
	}
	if s.heldPorts.Load() {
		t.Fatal("the handover stayed open after boot's own apply finished")
	}

	// Power off, then something else takes the port, then Power on: no handover is due.
	if _, err := s.SetPower(ctx, &pb.SetPowerRequest{On: false}); err != nil {
		t.Fatal(err)
	}
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", st.Settings.ProxyPort))
	if err != nil {
		t.Fatalf("hold the proxy port: %v", err)
	}
	defer busy.Close()

	start := time.Now()
	_, err = s.SetPower(ctx, &pb.SetPowerRequest{On: true})
	if err == nil {
		t.Fatal("a port another program holds was accepted")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a later start waited %s for the port, want an immediate report", elapsed)
	}
}

// Shutdown can begin while an apply is waiting for a port. That apply must not bring
// Xray back up: it would bind the proxy port after shutdown said it was down, which is
// the conflict the handover wait exists to avoid.
func TestAnApplyDoesNotRestartXrayAfterShutdownBegins(t *testing.T) {
	old := handoverWait
	handoverWait = 3 * time.Second
	t.Cleanup(func() { handoverWait = old })

	s := newTestServer(t)
	s.heldPorts.Store(true)
	port := s.state.Settings.ProxyPort

	// Something holds the proxy port, so the apply sits in the handover wait.
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	s.mu.Lock()
	s.state.Settings.Power = true
	applied := make(chan error, 1)
	go func() {
		s.mu.Lock()
		err := s.applyLocked(context.Background(), s.state, false)
		s.mu.Unlock()
		applied <- err
	}()
	s.mu.Unlock()

	// Shutdown begins while that apply waits, then the port frees up as the old Xray
	// closes its listener. The waiting apply must notice and stop, not start Xray again.
	time.Sleep(150 * time.Millisecond)
	shutdownDone := make(chan struct{})
	go func() {
		s.BeginShutdown()
		close(shutdownDone)
	}()
	time.Sleep(150 * time.Millisecond)
	busy.Close()

	select {
	case err := <-applied:
		if err == nil {
			t.Fatal("an apply after shutdown began started xray anyway")
		}
	case <-time.After(handoverWait + 5*time.Second):
		t.Fatal("the apply never returned")
	}
	<-shutdownDone
	if s.xray.Running() {
		t.Fatal("xray runs after shutdown closed it")
	}
}

// An apply holds xrayMu across its shutdown check and Core.Apply. Even if it has
// already passed that check, shutdown must wait for it before the final Close.
func TestShutdownWaitsForAnApplyThatPassedItsCheck(t *testing.T) {
	s := newTestServer(t)
	s.xrayMu.Lock()
	done := make(chan struct{})
	go func() { s.BeginShutdown(); close(done) }()
	// Shutdown must wait for the in-flight start rather than close Xray ahead of it.
	select {
	case <-done:
		s.xrayMu.Unlock()
		t.Fatal("shutdown finished while an apply could still start Xray")
	case <-time.After(2500 * time.Millisecond):
	}
	s.xrayMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after the apply released its lock")
	}
	if err := s.applyXray(context.Background(), nil, ""); err == nil {
		t.Fatal("an apply after the final Close was accepted")
	}
	if s.xray.Running() {
		t.Fatal("Xray runs after shutdown")
	}
}
