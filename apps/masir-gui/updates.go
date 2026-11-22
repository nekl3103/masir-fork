package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"google.golang.org/protobuf/proto"

	"github.com/Gozargah/Masir/libs/masir-core/service"
	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// The GUI asks the daemon for a release when the person opens the panel or focuses the
// window, at most every updateForegroundEvery: a daily cache would leave a security
// release unnoticed for a day. It asks the signed app feed when that release is one the
// feed hasn't answered for, and every updateFeedEvery regardless, since the app is
// released from the same tag but may one day not be.
const (
	// updateCheckEvery is how old the daemon's release check may be before a status
	// makes the GUI ask for a newer one.
	updateCheckEvery = 24 * time.Hour
	// updateForegroundEvery is what opening the panel, or focusing the window, asks for.
	updateForegroundEvery = 10 * time.Minute
	// updateFeedEvery is the fallback cadence for the signed desktop app feed.
	updateFeedEvery = 6 * time.Hour
	// updateRetryWait is how soon a failed check is tried again. A lost network at launch
	// must not hide a security update for a day, and a failing GitHub must not be asked
	// on every open.
	updateRetryWait = 15 * time.Minute
)

// updatePhase is what the footer's update control shows.
type updatePhase int

const (
	updateIdle updatePhase = iota
	updateAvailable
	updateStarting
	updateDownloading
	updatePaused
	updateVerifying
	updateInstalling
	updateRestarting
	updateAppDownloading
	updateAppPaused
	updateAppInstalling
	updateReady
	updateAuthorizing
	updateFailed
)

// updateState is the daemon's release check, the desktop app's signed update, and the
// job in progress. It is read and written on the main thread only.
type updateState struct {
	phase    updatePhase
	rel      *pb.ReleaseInfo
	app      *mygo.Update
	job      *pb.UpdateJob
	err      string
	checking bool
	// appCheckedVersion is the release the signed app feed last answered for,
	// appCheckedAt is when it answered, and appAttempt is when it was last asked, so a
	// failed check is retried later rather than on every status.
	appCheckedVersion string
	appCheckedAt      time.Time
	appAttempt        time.Time
	// lastAttempt keeps a failed release check from being repeated on every status, and
	// lastForeground is the last check an open panel asked for.
	lastAttempt    time.Time
	lastForeground time.Time
	lastStart      int64
	// parts counts separate payloads. Only a two-payload update shows part numbers.
	parts int
	// download progress for the app update; the daemon job has its own bytes.
	progress, total int64
	appPaused       bool
	appCancel       context.CancelFunc
	appRun          int
	appSync         bool
	appInstallDone  chan struct{}
	installReady    func()
	watchCancel     context.CancelFunc
	ignoreBusy      bool
	attached        bool
	attaching       bool
	sawJob          bool // the current watch stream saw a job that was doing something
	restarting      bool
	// tunWasOn remembers the tunnel before a daemon update, so the update can report
	// that it didn't come back instead of calling itself installed.
	tunWasOn bool
	// gapSeen is the daemon/app version pair already checked, so a persistent gap asks
	// for a fresh release once instead of on every status.
	gapSeen string
	// Local discovery offers an explicit action; only a click enters bundleSync.
	// bundleGap remembers a dismissed version pair without contacting the service.
	bundleOffer bool
	unitPending bool
	bundleSync  bool
	bundleGap   string
	// block says why MyGo can't replace this app; a .deb or .rpm is updated by its
	// package manager instead. pkgSync says that flow owns the control, and pkgDir is
	// the download it keeps for a retry.
	block      installBlock
	pkgSync    bool
	pkgDir     string
	pkgVersion string // the release the package flow downloads
	// pkgLast is closed when the last download or cleanup of the package cache ends.
	pkgLast chan struct{}
	// wantApp says the person asked for the update and the app feed hasn't answered yet.
	// A bundled daemon is updated by the app, so the answer decides what runs.
	wantApp bool
	// relaunchAfterRestart relaunches the app once a bundled daemon has restarted into
	// the app update's copy.
	relaunchAfterRestart bool
	// relaunching says the quit now starting is an update's relaunch, not the person
	// leaving: the app comes back, so masir stays on (see disconnectOnQuit).
	relaunching bool
}

// daemonWaitTimeout bounds how long a restarted daemon may take to answer with the new
// version (and the tunnel). Tests shorten it.
var daemonWaitTimeout = 2 * time.Minute

// The app's updater is behind variables so tests do not need a signed release or a
// running application.
var (
	preparedUpdatesEnabled   = preparedUpdateSupported
	confirmPreparedUpdates   = preparedUpdateSupported
	installPreparedAppUpdate = installPreparedUpdate
	importSharedComponents   = importDesktopComponents
	appUpdatesEnabled        = func() bool { return mygo.Updater.Enabled() }
	checkAppUpdate           = func(ctx context.Context) (*mygo.Update, error) { return mygo.Updater.Check(ctx) }
	installAppUpdate         = func(ctx context.Context, u *mygo.Update, progress func(int64, int64)) error {
		return u.Install(ctx, progress)
	}
	relaunchApp             = func() { mygo.App.Relaunch() }
	reconcileUpdatedService = func() error {
		if service.Installed() {
			return applyServiceUnit()
		}
		return nil
	}
)

// observeUpdates takes the daemon's latest release and starts a check when the cached
// one is missing or older than the check interval.
func (a *app) observeUpdates(st *pb.Status) {
	start := st.GetStartedAtMs()
	if start != 0 && a.updates.state.lastStart != 0 && start != a.updates.state.lastStart && !a.busyUpdate() {
		// A new daemon: its ignored version is gone, and its release cache with it.
		a.updates.state.rel = nil
		a.updates.state.app = nil
		a.updates.state.appCheckedVersion = ""
		a.updates.state.appCheckedAt = time.Time{}
		a.updates.state.lastAttempt = time.Time{}
		a.updates.state.attached = false
		if !a.updates.state.bundleSync && (a.updates.state.phase == updateAvailable || a.updates.state.phase == updateFailed) {
			a.updates.state.phase = updateIdle
		}
	}
	if start != 0 {
		a.updates.state.lastStart = start
	}
	if rel := st.GetLatestRelease(); rel != nil {
		a.applyRelease(rel)
	}
	// A status without a release (a daemon that restarted, or one that hasn't checked
	// yet) is not news that the release is gone: it keeps the answer the last check
	// brought, and a daemon restart clears it above.
	if a.offerBundledDaemonUpdate(st) {
		return
	}
	a.noteVersionGap(st)
	a.maybeCheckUpdates()
	if !a.updates.state.attached {
		// A job started by the CLI or a previous GUI is still the one to show.
		a.updates.state.attached = true
		a.updates.state.attaching = true
		a.watchDaemonUpdate()
	}
}

// applyRelease takes a release check and updates what the control offers.
func (a *app) applyRelease(rel *pb.ReleaseInfo) {
	a.updates.state.rel = rel
	if a.updates.state.phase == updateAvailable && !a.hasUpdate() {
		a.updates.state.phase = updateIdle
	}
	if a.updates.state.phase == updateIdle && a.hasUpdate() {
		a.updates.state.phase = updateAvailable
	}
}

// noteVersionGap notices that the app and the daemon run different releases. The
// daemon's release check is cached for a day, so it can still say "up to date" for the
// old version; ask GitHub through it with Force. When the app is already past the
// daemon (an app-only update), finish the same transaction by updating the service
// only after an explicit click. A gap that is still there is not checked again.
func (a *app) noteVersionGap(st *pb.Status) {
	if a.busyUpdate() || a.updates.state.checking || a.updates.state.restarting {
		return
	}
	if a.version == "" || st.GetVersion() == "" {
		return
	}
	if sameVersion(a.version, st.GetVersion()) {
		a.updates.state.gapSeen = ""
		return
	}
	gap := a.version + "|" + st.GetVersion()
	if a.updates.state.gapSeen == gap {
		return
	}
	a.updates.state.gapSeen = gap
	a.updates.state.checking = true
	a.async(func() {
		res, err := callRPC(func(ctx context.Context) (*pb.CheckUpdateResponse, error) {
			return a.d.CheckUpdate(ctx, &pb.CheckUpdateRequest{Force: true})
		})
		a.post(func() {
			a.updates.state.checking = false
			if a.offerBundledDaemonUpdate(a.status) {
				return
			}
			if err == nil && res.GetRelease() != nil {
				a.applyRelease(res.GetRelease())
			}
			a.maybeCheckUpdates()
		})
	})
}

// maybeCheckUpdates is the status-driven check: the daemon's daily cache is what it
// trusts, so a status only asks when that cache is a day old.
func (a *app) maybeCheckUpdates() {
	a.refreshUpdateCheck(updateCheckEvery)
}

// checkForUpdateOnForeground is what opening the panel, or focusing the window, asks for:
// a release reflected while the person is looking, at most every updateForegroundEvery.
// It is cheap when nothing changed: the daemon answers a conditional check with a 304
// that carries no body.
func (a *app) checkForUpdateOnForeground() {
	if !a.updates.state.lastForeground.IsZero() && time.Since(a.updates.state.lastForeground) < updateForegroundEvery {
		return
	}
	a.updates.state.lastForeground = time.Now()
	a.refreshUpdateCheck(updateForegroundEvery)
}

// refreshUpdateCheck asks the daemon for a release younger than maxAge, then the signed
// app feed for a release it hasn't answered for. Neither call polls.
func (a *app) refreshUpdateCheck(maxAge time.Duration) {
	if a.busyUpdate() || a.updates.state.checking || a.updates.state.restarting {
		return
	}
	rel := a.updates.state.rel
	if rel == nil || time.Since(time.UnixMilli(rel.GetCheckedAtMs())) >= maxAge {
		if a.updates.state.lastAttempt.IsZero() || time.Since(a.updates.state.lastAttempt) >= updateRetryWait {
			a.updates.state.lastAttempt = time.Now()
			a.checkDaemonRelease(maxAge)
			return // its answer asks the app feed, with the release it brought
		}
		// A recent attempt failed, or a daemon too old to accept max age keeps answering
		// from its own cache. The feed is still worth asking: it is the app's own.
	}
	a.checkAppFeed()
}

// appFeedDue reports whether the signed app feed should be asked: a release version it
// hasn't answered for, updateFeedEvery since it last answered, or a check that never
// succeeded. With no daemon release at all the feed is still asked on its own schedule,
// because the app carries a daemon and can fix one that won't start.
func (a *app) appFeedDue() bool {
	rel := a.updates.state.rel
	if rel != nil && a.ignored(rel.GetLatestVersion()) {
		return false
	}
	if !a.appFeedAnswered() {
		return true
	}
	return time.Since(a.updates.state.appCheckedAt) >= updateFeedEvery
}

// appFeedAnswered reports whether the signed app feed has answered for the release the
// daemon last reported.
func (a *app) appFeedAnswered() bool {
	rel := a.updates.state.rel
	return rel != nil && !a.updates.state.appCheckedAt.IsZero() && a.updates.state.appCheckedVersion == rel.GetLatestVersion()
}

// checkAppFeed asks the app feed when it is due, and not again until a failed check may
// be retried.
func (a *app) checkAppFeed() {
	if !a.appFeedDue() {
		return
	}
	if !a.updates.state.appAttempt.IsZero() && time.Since(a.updates.state.appAttempt) < updateRetryWait {
		return
	}
	a.checkAppRelease()
}

// checkDaemonRelease asks the daemon for the latest release, saying how old an answer it
// may give. A daemon that predates the hint ignores it and answers from its own cache.
func (a *app) checkDaemonRelease(maxAge time.Duration) {
	a.updates.state.checking = true
	a.async(func() {
		res, err := callRPC(func(ctx context.Context) (*pb.CheckUpdateResponse, error) {
			return a.d.CheckUpdate(ctx, &pb.CheckUpdateRequest{MaxAgeMs: maxAge.Milliseconds()})
		})
		a.post(func() {
			a.updates.state.checking = false
			if a.offerBundledDaemonUpdate(a.status) {
				return
			}
			if err == nil && res.GetRelease() != nil {
				// The attempt worked, so a later open may ask again: the retry wait is
				// for failures, and an open should not inherit the launch check's.
				a.updates.state.lastAttempt = time.Time{}
				a.applyRelease(res.GetRelease())
			}
			a.maybeCheckUpdates()
		})
	})
}

// checkAppRelease asks MyGo for a signed update of the desktop app itself.
func (a *app) checkAppRelease() {
	key := ""
	if a.updates.state.rel != nil {
		key = a.updates.state.rel.GetLatestVersion()
	}
	a.updates.state.appAttempt = time.Now()
	if !appUpdatesEnabled() {
		a.updates.state.appCheckedVersion, a.updates.state.appCheckedAt = key, time.Now()
		if a.updates.state.wantApp {
			a.updates.state.wantApp = false
			a.updates.state.phase = updateIdle
		}
		return
	}
	a.async(func() {
		ctx, cancel := rpc()
		u, err := checkAppUpdate(ctx)
		cancel()
		a.post(func() {
			if err == nil {
				a.updates.state.app = u
				// Keyed by the release the answer belongs to, so a release that appeared
				// while this was in flight asks the feed again rather than trusting it.
				a.updates.state.appCheckedVersion, a.updates.state.appCheckedAt = key, time.Now()
			}
			if a.updates.state.wantApp {
				a.updates.state.wantApp = false
				if u != nil {
					a.startAppUpdate()
					return
				}
				a.updates.state.phase = updateIdle
				a.updates.state.err = ""
			}
			if a.updates.state.phase == updateAvailable && !a.hasUpdate() {
				a.updates.state.phase = updateIdle
			}
			if a.updates.state.phase == updateIdle && a.hasUpdate() {
				a.updates.state.phase = updateAvailable
			}
		})
	})
}

// ignored reports whether this version is hidden by the daemon's ignore.
func (a *app) ignored(version string) bool {
	hidden := a.updates.state.rel.GetIgnoredVersion()
	return hidden != "" && sameVersion(hidden, version)
}

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

// daemonUpdateAvailable reports whether the daemon and CLI have a newer release that
// this copy may install. A daemon running from the desktop app's bundle is the app's
// copy: only the app updater may replace it. A service or standalone copy is the
// daemon's own to update from the signed release.
func (a *app) daemonUpdateAvailable() bool {
	if a.daemonIsBundled() {
		return false
	}
	return a.daemonReleaseAvailable()
}

// daemonIsBundled reports whether the daemon the app starts or talks to is the app's
// own copy. The path says so even when the daemon predates the status field, which is
// the first update after this change.
func (a *app) daemonIsBundled() bool {
	if a.status != nil && a.status.GetBundled() {
		return true
	}
	// The command executable can be the bundled fallback while an older installed
	// service runs elsewhere. Its status, not the selected CLI, identifies it.
	// Protobuf getters accept nil; unknown status falls through to the path below.
	if a.status.GetServiceInstalled() {
		return false
	}
	p := filepath.ToSlash(a.binary)
	return strings.Contains(p, ".app/Contents/") ||
		strings.Contains(p, "/resources/bin/") || strings.Contains(p, "/Resources/bin/") ||
		strings.Contains(p, "/opt/masir-desktop/bin/") // the .deb and .rpm layout
}

// daemonReleaseAvailable is the release check alone, before deciding which copy (the
// app's bundle or the service) is the one that may install it.
func (a *app) daemonReleaseAvailable() bool {
	rel := a.updates.state.rel
	return rel != nil && rel.GetAvailable() && !a.ignored(rel.GetLatestVersion())
}

// appUpdateCoversDaemon reports whether the only way to update this daemon is the app
// update, because it runs from the app bundle.
func (a *app) appUpdateCoversDaemon() bool {
	return a.daemonIsBundled()
}

// appUpdateAvailable reports whether the signed desktop app has a newer release.
func (a *app) appUpdateAvailable() bool {
	return a.updates.state.app != nil && !a.ignored(a.updates.state.app.Version)
}

// hasUpdate reports whether either side has an update the person has not ignored. A
// bundled daemon's release is shown too: the app update is how it is installed.
func (a *app) hasUpdate() bool {
	if a.updates.state.bundleOffer || a.daemonUpdateAvailable() || a.appUpdateAvailable() || a.packageUpdateAvailable() {
		return true
	}
	// A bundled daemon is updated by the app. Until the app feed has answered for this
	// release, it is still worth showing; once it says the app is current, it isn't.
	return a.appUpdateCoversDaemon() && a.daemonReleaseAvailable() && !a.appFeedAnswered()
}

// updatePartCount counts independent payload downloads. A capable service imports
// its files from the prepared desktop payload; older daemons need a separate archive.
func (a *app) updatePartCount() int {
	if a.daemonUpdateAvailable() && a.appUpdateAvailable() && !a.canShareDesktopUpdate() {
		return 2
	}
	return 1
}

// Both updates must target the same release. Older daemons and stock MyGo builds
// keep the existing independent-download path.
func (a *app) canShareDesktopUpdate() bool {
	return preparedUpdatesEnabled() && a.privileged() && a.updates.state.rel.GetDesktopImportSupported() &&
		a.daemonUpdateAvailable() && a.appUpdateAvailable() &&
		sameVersion(a.updates.state.rel.GetLatestVersion(), a.updates.state.app.Version)
}

// daemonPath is a.binary, read on the main thread.
func (a *app) daemonPath() string {
	var p string
	a.post(func() { p = a.binary })
	return p
}

// busy reports whether an update is doing something, so a check must not interrupt it.
func (a *app) busyUpdate() bool {
	switch a.updates.state.phase {
	case updateStarting, updateDownloading, updatePaused, updateVerifying,
		updateInstalling, updateRestarting, updateAppDownloading, updateAppPaused,
		updateAppInstalling, updateAuthorizing, updateReady:
		return true
	}
	return false
}

// showUpdateControl reports whether the footer and Settings show the control rather than
// the version text.
func (a *app) showUpdateControl() bool {
	if a.updates.state.phase == updateAvailable || a.updates.state.phase == updateFailed {
		return true
	}
	return a.busyUpdate()
}

// startUpdate starts the daemon update, then the desktop app update, in one flow.
func (a *app) startUpdate() {
	if a.updates.state.phase != updateAvailable && a.updates.state.phase != updateFailed {
		return
	}
	if a.updates.state.bundleOffer {
		a.startBundledDaemonUpdate()
		return
	}
	// The package carries the service's components too, so it goes before the daemon.
	if b := a.updates.state.block; (b == blockPackageDeb || b == blockPackageRpm) && a.packageUpdateAvailable() {
		a.startPackageUpdate()
		return
	}
	// A read-only portable copy can only be replaced by hand; its daemon still updates.
	if a.updates.state.block == blockReadOnly && !a.daemonUpdateAvailable() {
		return
	}
	a.updates.state.err = ""
	a.updates.state.parts = a.updatePartCount()
	if a.canShareDesktopUpdate() {
		a.startAppUpdate()
		return
	}
	if a.daemonUpdateAvailable() {
		a.startDaemonUpdate()
		return
	}
	if a.appUpdateAvailable() {
		a.startAppUpdate()
		return
	}
	if a.appUpdateCoversDaemon() {
		// The daemon runs from the app bundle, so the app update is the update. Its
		// feed may still be answering; start it as soon as it does.
		a.updates.state.wantApp = true
		a.updates.state.phase = updateStarting
		a.checkAppRelease() // asks now, whatever the cadence says
	}
}

func (a *app) startDaemonUpdate() {
	a.updates.state.phase = updateStarting
	a.updates.state.tunWasOn = a.status != nil && a.status.GetTunRunning()
	client := ""
	if a.cli.installed {
		client = a.cli.path
	}
	root := a.privileged()
	a.async(func() {
		if service.Installed() && !root {
			// The service is the supervisor. Starting a session root daemon here would
			// replace it (launchd doesn't restart a clean exit), so ask the person to
			// start the service instead of quietly taking over.
			a.post(func() {
				a.updates.state.phase = updateFailed
				a.updates.state.err = tr("the background service isn't running; start it in Settings")
				a.notifyError(tr("The update failed: ") + a.updates.state.err)
			})
			return
		}
		ctx, cancel := rpc()
		res, err := a.d.StartUpdate(ctx, &pb.StartUpdateRequest{
			Target: pb.UpdateTarget_UPDATE_TARGET_MASIR, Trigger: "user", ClientBinary: client,
		})
		cancel()
		a.post(func() {
			if err != nil {
				a.updates.state.phase = updateFailed
				a.updates.state.err = describe(err)
				a.notifyError(tr("The update failed: ") + a.updates.state.err)
				return
			}
			a.updates.state.job = res.GetJob()
			a.applyJob(a.updates.state.job)
			a.watchDaemonUpdate()
		})
	})
}

// watchDaemonUpdate follows the daemon's job until it ends or the daemon restarts.
func (a *app) watchDaemonUpdate() {
	ctx, cancel := context.WithCancel(context.Background())
	a.updates.state.watchCancel = cancel
	a.async(func() {
		stream, err := a.d.WatchUpdate(ctx, &pb.WatchUpdateRequest{Target: pb.UpdateTarget_UPDATE_TARGET_MASIR})
		if err != nil {
			a.post(func() {
				attaching := a.updates.state.attaching
				a.updates.state.attaching = false
				if a.updates.state.bundleSync || a.updates.state.pkgSync || a.updates.state.appSync {
					return
				}
				if attaching {
					a.offerBundledDaemonUpdate(a.status)
					return // no job to attach to is not a failed update
				}
				a.updates.state.phase = updateFailed
				a.updates.state.err = describe(err)
				a.notifyError(tr("The update failed: ") + a.updates.state.err)
			})
			return
		}
		for {
			res, err := stream.Recv()
			if err != nil {
				break
			}
			job := res.GetJob()
			a.post(func() { a.applyJob(job) })
		}
		a.post(func() {
			a.updates.state.watchCancel = nil
			a.daemonWatchEnded()
		})
	})
}

// applyJob turns the daemon's job state into the control's phase.
func (a *app) applyJob(job *pb.UpdateJob) {
	if a.updates.state.bundleSync || a.updates.state.pkgSync || a.updates.state.appSync {
		// A desktop update owns the control. A late or unrelated daemon job must
		// not change it or route the pause action to the daemon.
		return
	}
	if a.updates.state.bundleOffer {
		if job == nil || !updateActive(job.GetState()) {
			return
		}
		// An already running CLI/service job takes priority over an idle local offer.
		a.updates.state.bundleOffer = false
		a.updates.state.bundleGap = ""
	}
	if job == nil {
		return
	}
	if a.updates.state.attaching {
		a.updates.state.attaching = false
		if !updateActive(job.GetState()) {
			a.offerBundledDaemonUpdate(a.status)
			return // a finished job from an earlier update is not ours to show
		}
	}
	a.updates.state.job = job
	if updateActive(job.GetState()) {
		a.updates.state.sawJob = true
	}
	switch job.GetState() {
	case pb.UpdateState_UPDATE_STATE_CHECKING:
		a.updates.state.phase = updateStarting
	case pb.UpdateState_UPDATE_STATE_DOWNLOADING:
		a.updates.state.phase = updateDownloading
	case pb.UpdateState_UPDATE_STATE_PAUSED:
		a.updates.state.phase = updatePaused
	case pb.UpdateState_UPDATE_STATE_VERIFYING:
		a.updates.state.phase = updateVerifying
	case pb.UpdateState_UPDATE_STATE_INSTALLING:
		a.updates.state.phase = updateInstalling
	case pb.UpdateState_UPDATE_STATE_RESTARTING:
		a.updates.state.phase = updateRestarting
	case pb.UpdateState_UPDATE_STATE_DONE:
		a.daemonAfterInstall(job)
	case pb.UpdateState_UPDATE_STATE_UP_TO_DATE:
		a.daemonFinished()
	case pb.UpdateState_UPDATE_STATE_STOPPED:
		a.updates.state.phase = updateAvailable
		a.updates.state.job = nil
	case pb.UpdateState_UPDATE_STATE_FAILED, pb.UpdateState_UPDATE_STATE_BLOCKED:
		a.updates.state.phase = updateFailed
		a.updates.state.err = job.GetError()
		if a.updates.state.err == "" && len(job.GetBlockedRules()) > 0 {
			a.updates.state.err = tr("the new version can't build some of your rules")
		}
		if a.updates.state.err == "" {
			a.updates.state.err = tr("the update couldn't be installed")
		}
		a.notifyError(tr("The update failed: ") + a.updates.state.err)
	}
}

// daemonWatchEnded handles a watch stream that ended, including the daemon going away
// to restart into the new version. A stream that saw a real job leaves the attach flag
// clear, so the next job (for example one started by the CLI) is followed too.
func (a *app) daemonWatchEnded() {
	a.updates.state.attaching = false
	if a.updates.state.sawJob {
		a.updates.state.sawJob = false
		a.updates.state.attached = false
	}
	if a.updates.state.pkgSync || a.updates.state.appSync {
		return // the desktop update owns the control and follows the restart itself
	}
	if a.offerBundledDaemonUpdate(a.status) {
		return
	}
	if a.updates.state.phase == updateRestarting && a.updates.state.restarting {
		return // restartDaemon is following the daemon it asked to restart
	}
	if a.updates.state.phase == updateRestarting {
		a.awaitDaemon(a.updates.state.job.GetVersion())
		return
	}
	switch a.updates.state.phase {
	case updateFailed, updateIdle, updateAppDownloading, updateAppPaused, updateAppInstalling:
		return
	case updateAvailable:
		return
	}
	a.updates.state.phase = updateFailed
	a.updates.state.err = tr("lost the update while it was running")
	a.notifyError(tr("The update failed: ") + a.updates.state.err)
}

// awaitDaemon waits for the restarted daemon to report the version the update installed,
// and for the tunnel to come back when it was on before the update.
func (a *app) awaitDaemon(version string) {
	a.async(func() {
		if err := a.waitForDaemon(version, daemonWaitTimeout); err != nil {
			msg := err.Error()
			if service.Installed() {
				msg += tr("; run `masir daemon rollback` to put the previous version back")
			}
			a.post(func() {
				a.updates.state.phase = updateFailed
				a.updates.state.err = msg
				a.notifyError(tr("The update failed: ") + a.updates.state.err)
			})
			return
		}
		a.post(a.daemonFinished)
	})
}

// waitForDaemon polls until the daemon reports version and, if the tunnel was on
// before the update, has it up again. It never treats a daemon without the tunnel as
// installed.
func (a *app) waitForDaemon(version string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	sawVersion := false
	// A cached pre-update version is not evidence of what is running after restart.
	lastVersion := ""
	for time.Now().Before(deadline) {
		ctx, cancel := rpc()
		res, err := a.d.GetStatus(ctx, &pb.GetStatusRequest{})
		cancel()
		if err == nil {
			st := res.GetStatus()
			lastVersion = st.GetVersion()
			// This version or newer: a newer daemon (a service ahead of the app) isn't
			// a failed update; an older or cached one is.
			if version == "" || sameVersion(st.GetVersion(), version) || newerRelease(st.GetVersion(), version) {
				sawVersion = true
				if !a.updates.state.tunWasOn || st.GetTunRunning() {
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if sawVersion {
		return errors.New(tr("the daemon came back without the tunnel; it will be back when masir reconnects"))
	}
	if lastVersion != "" && version != "" {
		return fmt.Errorf(tr("the daemon did not report %s after the update; last reported version was %s"), version, lastVersion)
	}
	return errors.New(tr("the daemon didn't come back after the update"))
}

// daemonAfterInstall handles a job that installed without restarting (an older daemon,
// or a target that has no restart step): restart only when the running version differs.
func (a *app) daemonAfterInstall(job *pb.UpdateJob) {
	if a.status != nil && job.GetVersion() != "" && sameVersion(a.status.GetVersion(), job.GetVersion()) {
		a.daemonFinished()
		return
	}
	if job.GetVersion() != "" {
		a.updates.state.tunWasOn = a.status != nil && a.status.GetTunRunning()
		a.restartDaemon(job.GetVersion())
		return
	}
	a.daemonFinished()
}

// daemonFinished moves on to the app update, or back to the version text.
func (a *app) daemonFinished() {
	if a.updates.state.appSync {
		return // the app update already started from the daemon's final job
	}
	a.updates.state.job = nil
	if a.updates.state.rel != nil {
		// Keep the release for the app-only ignore; it is no longer daemon-available.
		rel := proto.Clone(a.updates.state.rel).(*pb.ReleaseInfo)
		rel.Available = false
		rel.IgnoredVersion = ""
		a.updates.state.rel = rel
	}
	if a.appUpdateAvailable() {
		a.startAppUpdate()
		return
	}
	a.updates.state.phase = updateIdle
}

// startAppUpdate prepares the signed desktop update with MyGo. Published builds
// wait for confirmation at the prepared hook, before committing any files. A
// pause cancels the download; a resume starts it again.
func (a *app) startAppUpdate() {
	u := a.updates.state.app
	shared := a.canShareDesktopUpdate()
	if shared {
		a.updates.state.tunWasOn = a.status != nil && a.status.GetTunRunning()
	}
	if u == nil {
		return
	}
	a.updates.state.phase = updateAppDownloading
	a.updates.state.progress, a.updates.state.total = 0, 0
	a.updates.state.appPaused = false
	a.updates.state.appRun++
	run := a.updates.state.appRun
	ctx, cancel := context.WithCancel(context.Background())
	a.updates.state.appCancel = cancel
	waitForInstall := confirmPreparedUpdates()
	a.updates.state.appSync = true
	done := make(chan struct{})
	a.updates.state.appInstallDone = done
	a.async(func() {
		lastPct, lastAt := -1, time.Now()
		progress := func(done, total int64) {
			pct := -1
			if total > 0 {
				pct = int(done * 100 / total)
			}
			if pct == lastPct && time.Since(lastAt) < 100*time.Millisecond {
				return
			}
			lastPct, lastAt = pct, time.Now()
			a.post(func() {
				if run == a.updates.state.appRun && (a.updates.state.phase == updateAppDownloading || a.updates.state.phase == updateVerifying) {
					a.updates.state.phase = updateAppDownloading
					a.updates.state.progress, a.updates.state.total = done, total
					if total > 0 && done >= total {
						a.updates.state.phase = updateAppInstalling
						if waitForInstall {
							a.updates.state.phase = updateVerifying
						}
					}
				}
			})
		}
		var err error
		if shared || waitForInstall {
			err = installPreparedAppUpdate(ctx, u, progress, func(dir string) error {
				if waitForInstall {
					approved := make(chan struct{})
					a.post(func() {
						if run == a.updates.state.appRun && !a.updates.state.appPaused {
							a.updates.state.installReady = func() { close(approved) }
							a.updates.state.phase = updateReady
						}
					})
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-approved:
					}
				}
				if shared {
					return importSharedComponents(ctx, a.d, dir, u.Version)
				}
				return nil
			})
		} else {
			err = installAppUpdate(ctx, u, progress)
		}
		cancel()
		close(done)
		a.post(func() {
			if run != a.updates.state.appRun {
				return // a pause and resume started a newer attempt
			}
			a.updates.state.appCancel = nil
			a.updates.state.appInstallDone = nil
			a.updates.state.appSync = false
			switch {
			case a.updates.state.appPaused:
				a.updates.state.phase = updateAppPaused
			case err != nil:
				a.updates.state.phase = updateFailed
				a.updates.state.err = err.Error()
				a.notifyError(tr("The app update failed: ") + a.updates.state.err)
			default:
				a.updates.state.phase = updateIdle
				// A daemon that runs from the old bundle must restart into the new one
				// before the app relaunches, so the tunnel comes back with this release.
				// If no daemon is running there is nothing to restart: the new app starts
				// one itself.
				if (shared || a.appUpdateCoversDaemon()) && a.reachable && a.status != nil && !sameVersion(a.status.GetVersion(), u.Version) {
					a.updates.state.tunWasOn = a.status.GetTunRunning()
					a.updates.state.relaunchAfterRestart = true
					a.restartDaemon(u.Version)
					return
				}
				a.updates.state.relaunching = true
				relaunchApp()
			}
		})
	})
}

// confirmInstall commits the payload held by the desktop or package updater.
func (a *app) confirmInstall() {
	st := &a.updates.state
	if st.phase != updateReady || st.installReady == nil {
		return
	}
	install := st.installReady
	st.installReady = nil
	st.phase = updateAppInstalling
	install()
}

// pauseUpdate pauses a daemon download, or cancels the app download until it is resumed.
func (a *app) pauseUpdate() {
	switch a.updates.state.phase {
	case updateDownloading:
		a.async(func() {
			ctx, cancel := rpc()
			res, err := a.d.PauseUpdate(ctx, &pb.PauseUpdateRequest{Target: pb.UpdateTarget_UPDATE_TARGET_MASIR})
			cancel()
			a.post(func() {
				if err != nil {
					a.updates.state.phase = updateFailed
					a.updates.state.err = describe(err)
					return
				}
				a.applyJob(res.GetJob())
			})
		})
	case updateAppDownloading:
		a.updates.state.appPaused = true
		if a.updates.state.appCancel != nil {
			a.updates.state.appCancel()
			a.updates.state.appCancel = nil
		}
		a.updates.state.phase = updateAppPaused
	}
}

// resumeUpdate continues a paused daemon download, or starts the app download again.
func (a *app) resumeUpdate() {
	switch a.updates.state.phase {
	case updatePaused:
		a.async(func() {
			ctx, cancel := rpc()
			res, err := a.d.ResumeUpdate(ctx, &pb.ResumeUpdateRequest{Target: pb.UpdateTarget_UPDATE_TARGET_MASIR})
			cancel()
			a.post(func() {
				if err != nil {
					a.updates.state.phase = updateFailed
					a.updates.state.err = describe(err)
					return
				}
				a.applyJob(res.GetJob())
				if a.updates.state.watchCancel == nil {
					a.watchDaemonUpdate()
				}
			})
		})
	case updateAppPaused:
		if a.updates.state.pkgSync {
			a.startPackageUpdate()
			return
		}
		a.startAppUpdate()
	}
}

// cancelUpdate stops a daemon download and deletes its partial file, or abandons the app
// download and returns to the available button.
func (a *app) cancelUpdate() {
	switch a.updates.state.phase {
	case updateDownloading, updatePaused:
		a.async(func() {
			ctx, cancel := rpc()
			res, err := a.d.StopUpdate(ctx, &pb.StopUpdateRequest{Target: pb.UpdateTarget_UPDATE_TARGET_MASIR})
			cancel()
			a.post(func() {
				if err != nil {
					a.updates.state.phase = updateFailed
					a.updates.state.err = describe(err)
					return
				}
				a.applyJob(res.GetJob())
			})
		})
	case updateAppDownloading, updateAppPaused, updateReady:
		a.updates.state.installReady = nil
		a.updates.state.appRun++
		a.updates.state.appInstallDone = nil
		if a.updates.state.appCancel != nil {
			a.updates.state.appCancel()
			a.updates.state.appCancel = nil
		}
		a.updates.state.phase = updateAvailable
		a.updates.state.appSync = false
		a.updates.state.progress, a.updates.state.total = 0, 0
		if a.updates.state.pkgSync {
			a.updates.state.pkgSync = false
			// Queued now: after the cancelled download ends and before any retry, which
			// would otherwise resume the partial a cancel promised to delete.
			if v := a.updates.state.pkgVersion; v != "" {
				a.pkgSerial(func() func() {
					if root, err := packageCacheDir(); err == nil {
						os.RemoveAll(filepath.Join(root, v))
					}
					return nil
				})
			}
		}
	}
}

// retryUpdate starts the failed side again.
func (a *app) retryUpdate() {
	if a.updates.state.phase != updateFailed {
		return
	}
	if a.updates.state.bundleSync {

		a.updates.state.bundleSync = false
		a.updates.state.bundleOffer = true
		a.startBundledDaemonUpdate()
		return
	}
	a.updates.state.phase = updateAvailable
	a.startUpdate()
}

// ignoreUpdate hides this version in the daemon's memory until the next fresh check or
// the next daemon start.
func (a *app) ignoreUpdate() {
	if a.updates.state.bundleOffer {
		a.updates.state.bundleOffer = false
		a.updates.state.gapSeen = a.updates.state.bundleGap
		a.updates.state.phase = updateIdle
		return
	}
	if a.updates.state.bundleSync {
		if a.updates.state.phase != updateFailed {
			return
		}
		a.updates.state.bundleSync = false
		a.updates.state.gapSeen = a.updates.state.bundleGap
		a.updates.state.phase = updateIdle
		a.updates.state.err = ""
		return
	}
	if a.updates.state.ignoreBusy {
		return
	}
	version := ""
	switch {
	case a.daemonReleaseAvailable():
		version = a.updates.state.rel.GetLatestVersion()
	case a.appUpdateAvailable():
		version = a.updates.state.app.Version
	case a.packageUpdateAvailable():
		// The daemon keeps any version, available or not.
		version = packageTargetVersion(a.updates.state.rel)
	}
	if version == "" {
		return
	}
	a.updates.state.ignoreBusy = true
	a.async(func() {
		ctx, cancel := rpc()
		res, err := a.d.IgnoreUpdate(ctx, &pb.IgnoreUpdateRequest{Version: version})
		cancel()
		a.post(func() {
			a.updates.state.ignoreBusy = false
			if err != nil {
				a.fail(tr("Couldn't ignore the update: "), err)
				return
			}
			if res.GetRelease() != nil {
				a.applyRelease(res.GetRelease())
			}
			a.updates.state.phase = updateIdle
		})
	})
}

// updateActive reports whether a job is doing something a UI can show.
func updateActive(s pb.UpdateState) bool {
	switch s {
	case pb.UpdateState_UPDATE_STATE_CHECKING, pb.UpdateState_UPDATE_STATE_DOWNLOADING,
		pb.UpdateState_UPDATE_STATE_PAUSED, pb.UpdateState_UPDATE_STATE_VERIFYING,
		pb.UpdateState_UPDATE_STATE_INSTALLING, pb.UpdateState_UPDATE_STATE_RESTARTING:
		return true
	}
	return false
}

// updatePercent is the download progress, 0 when it isn't known yet.
func (a *app) updatePercent() int {
	var done, total int64
	if a.updates.state.phase == updateAppDownloading || a.updates.state.phase == updateAppPaused {
		done, total = a.updates.state.progress, a.updates.state.total
	} else if a.updates.state.job != nil {
		done, total = a.updates.state.job.GetBytesDone(), a.updates.state.job.GetBytesTotal()
	}
	if total <= 0 {
		return 0
	}
	return int(max(0, min(100, done*100/total)))
}

// downloadLabel is the control's text while a download runs: which part of the update,
// when there are two, and how far along. The control is 42 tw wide and the footer has no
// room to grow it, so the part is a short marker, never a word.
func (a *app) downloadLabel(part int) string {
	if a.updates.state.parts < 2 {
		if pct := a.updatePercent(); pct > 0 {
			return fmt.Sprintf(tr("Downloading %d%%"), pct)
		}
		return tr("Downloading…")
	}
	return fmt.Sprintf(tr("Part %d/2 · %d%%"), part, a.updatePercent())
}

// pausedLabel is downloadLabel while the download is paused.
func (a *app) pausedLabel(part int) string {
	if a.updates.state.parts < 2 {
		return fmt.Sprintf(tr("Paused · %d%%"), a.updatePercent())
	}
	return fmt.Sprintf(tr("Paused %d/2 · %d%%"), part, a.updatePercent())
}

// updateTooltip says what the numbered label cannot: what each part is fetching.
func (a *app) updateTooltip() string {
	if a.updates.state.phase == updateAuthorizing {
		return tr("Your password is needed to install the update.")
	}
	if a.updates.state.phase == updateFailed && a.updates.state.err != "" {
		return a.updates.state.err
	}
	if a.updates.state.bundleOffer && a.updates.state.unitPending && !newerRelease(a.version, a.status.GetVersion()) {
		return tr("Apply the background service configuration. Administrator permission is required.")
	}
	if a.updates.state.bundleOffer {
		return fmt.Sprintf(tr("Finish the update: daemon %s to %s from this app. Administrator permission is required; the connection will briefly restart."), a.status.GetVersion(), a.version)
	}
	if a.updates.state.bundleSync {
		return tr("Updating the daemon from this app; the connection will briefly restart")
	}
	if a.updates.state.block == blockReadOnly && a.updates.state.phase == updateAvailable && a.packageUpdateAvailable() && !a.daemonUpdateAvailable() {
		return tr("Masir can't update itself here: its folder isn't writable. Download the new version from the release page.")
	}
	if a.updates.state.parts < 2 {
		return ""
	}
	switch a.updates.state.phase {
	case updateDownloading, updatePaused:
		return tr("Part 1 of 2: the daemon and the masir command")
	case updateAppDownloading, updateAppPaused:
		return tr("Part 2 of 2: the Masir app")
	}
	return ""
}

// updateLabel is the text inside the control.
func (a *app) updateLabel() string {
	if a.updates.state.bundleSync && a.updates.state.phase == updateInstalling {
		return tr("Updating daemon...")
	}
	if a.updates.state.phase == updateAuthorizing {
		return tr("Enter Password…")
	}
	if a.updates.state.bundleOffer {
		return tr("Finish Update")
	}
	if a.updates.state.bundleSync && a.updates.state.phase == updateFailed {
		return tr("Retry")
	}
	switch a.updates.state.phase {
	case updateAvailable:
		return tr("New Update")
	case updateReady:
		return tr("Install Update")
	case updateStarting:
		return tr("Starting...")
	case updateDownloading:
		return a.downloadLabel(1)
	case updatePaused:
		return a.pausedLabel(1)
	case updateVerifying:
		return tr("Verifying...")
	case updateInstalling:
		return tr("Installing...")
	case updateRestarting:
		return tr("Restarting...")
	case updateAppDownloading:
		return a.downloadLabel(2)
	case updateAppPaused:
		return a.pausedLabel(2)
	case updateAppInstalling:
		return tr("Installing...")
	case updateFailed:
		if a.updates.state.err != "" {
			return tr("Update failed")
		}
		return tr("Update failed")
	}
	return ""
}

// restartDaemon asks the daemon to start the binary now on disk: the service or the
// app's watch starts the same process again. It waits for the expected version and the
// tunnel, so a binary that installs but doesn't run is a failure, not a success.
func (a *app) restartDaemon(version string) { a.followDaemonRestart(version, true) }

// Legacy imports restart through the supervisor while elevated; follow their
// result without requesting a second restart or another permission prompt.
func (a *app) followDaemonRestart(version string, request bool) {
	if a.updates.state.restarting {
		return
	}
	a.updates.state.restarting = true
	a.updates.state.phase = updateRestarting
	a.updates.state.err = ""
	packageRestart := a.updates.state.pkgSync
	relaunch := a.updates.state.relaunchAfterRestart
	a.async(func() {
		if request {
			ctx, cancel := rpc()
			_, err := a.d.Restart(ctx, &pb.RestartRequest{})
			cancel()
			if err != nil { // an older daemon without Restart, or no longer reachable
				ctx, cancel := rpc()
				_, _ = a.d.Shutdown(ctx, &pb.ShutdownRequest{})
				cancel()
			}
		}
		if err := a.waitForDaemon(version, daemonWaitTimeout); err != nil {
			msg := err.Error()
			if service.Installed() {
				msg += tr("; run `masir daemon rollback` to put the previous version back")
			}
			a.post(func() {
				a.updates.state.restarting = false
				a.updates.state.relaunchAfterRestart = false
				a.updates.state.phase = updateFailed
				a.updates.state.err = msg
				a.notifyError(tr("The update failed: ") + a.updates.state.err)
				// The package manager already replaced this app: keep running the old
				// one and a retry can only find the new version installed.
				if a.updates.state.pkgSync {
					a.updates.state.pkgSync = false
					a.updates.state.relaunching = true
					relaunchApp()
				}
			})
			return
		}
		// A release can change the service definition (capabilities, sandbox). It asks
		// for permission only when the definition really changed.
		if !packageRestart {
			if err := reconcileUpdatedService(); err != nil {
				log.Printf("service definition: %v", err)
			}
		}
		a.post(func() {
			a.updates.state.restarting = false
			if a.updates.state.bundleSync {
				// The old service's cached release still calls its version outdated.
				// Let the new service answer afresh rather than offer that update again.
				a.updates.state.rel = nil
				a.updates.state.lastAttempt = time.Time{}
				a.updates.state.gapSeen = ""
			}
			a.updates.state.bundleSync = false
			a.updates.state.pkgSync = false
			a.updates.state.relaunchAfterRestart = false
			a.updates.state.phase = updateIdle
			if relaunch {
				a.updates.state.relaunching = true
				relaunchApp()
			}
		})
	})
}
