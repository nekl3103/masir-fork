package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"google.golang.org/grpc"

	pb "github.com/Gozargah/Masir/libs/masir-proto/gen/masir/daemon/v1"
)

// section is which part of the main page is open; the server list and the rules never
// are both.
type section int

const (
	sectionNone section = iota
	sectionServers
	sectionRules
)

// serverKey names a config across subscriptions.
type serverKey struct{ sub, cfg string }

// server is one config of a subscription, as the pickers list it.
type server struct {
	key     serverKey
	subName string
	cfg     *pb.SubscriptionConfig
	search  string // lowercased text the search matches, including the flag's country
}

// textSetting is a settings field being typed in: the text it shows, and whether it
// differs from the daemon's value because the person is editing it.
type textSetting struct {
	text   string
	edited bool
}

// app is everything the panel shows and does. It is read and changed on the main thread
// only: the daemon's streams and calls hand their results over with post.
type app struct {
	// post runs fn on the main thread and redraws; async runs slow work off it. Tests
	// run both inline.
	post  func(fn func())
	async func(fn func())

	router *ui.Router

	d      pb.DaemonServiceClient
	binary string // the masir executable to start a daemon from
	// lastServiceInstalled is what the last status said, so a service installed or
	// removed while the app runs can make binary point at the copy that exists.
	lastServiceInstalled bool
	// serviceChecked is when the app last asked the OS service manager itself. The
	// daemon reports it too, but a stopped service has no daemon to ask.
	serviceChecked time.Time

	// stores
	subscriptions        subscriptionStore
	ruleDestinationQuery string
	ruleServerFolds      serverSearchFolds
	ruleDestinations     ui.ListState
	ruleReview           rulePageState
	rulePage             rulePageState
	ruleApps             ruleAppState
	ruleGeo              ruleGeoState
	ruleGeoIP            ruleGeoState
	rules                ruleStore
	updates              updateStore

	// What the daemon last said.
	reachable   bool
	status      *pb.Status
	refreshRev  uint64
	listRefresh uint64
	down, up    uint64 // bytes per second

	// latency is the last result per server, in milliseconds; negative means a failed probe.
	latency   map[serverKey]int64
	latencyAt map[serverKey]int64
	testing   map[serverKey]bool

	// What the person is doing.
	path                   pagePath
	open                   section
	subscriptionNodes      ui.ListState
	subscriptionRefreshing map[string]bool
	subscriptionList       ui.ListState
	subscriptionPage       subscriptionPageState
	connection             connectionState
	busy                   string          // what a running action is called, e.g. "Connecting…"
	query                  string          // search in the server list
	collapsedSubs          map[string]bool // subscription group IDs collapsed in server lists
	collapsedRules         map[string]bool // rule section titles collapsed in rules panel
	ruleQuery              string
	notices                []string // toasts waiting for the next frame
	noticeTypes            map[string]string
	dismissed              string // the last_error the person closed

	// The add-subscription form.
	subURL, subName                           string
	subCustomInterval                         uint32
	subBusy                                   bool
	subError                                  string
	importHeight                              float32
	importHeader, importContent, importFooter ui.Handle

	// Settings form.
	portText     string // digits only; synced from the daemon when not editing
	portEdited   bool   // the port field has been typed in
	dns          textSetting
	tunDNS       textSetting
	noticeMarked bool // the analytics notice has been shown and marked

	// What the system says about starting at login, read once rather than on every frame
	// (the OS call is not free).
	loginOn            bool
	loginChecked       bool
	loginNeedsApproval bool // the system has the item but needs the person to allow it

	// GUI settings.
	guiSettingsFile   string
	guiSettingsMu     sync.Mutex
	guiSettingsWrites atomic.Uint64
	disconnectQuit    bool

	// Scroll positions for server picker and rules list.
	serverScrollKey      string
	serverScrollRow      int
	serverScrollRestored bool
	lastServerTop        int
	lastServerQuery      string
	mainServerFolds      serverSearchFolds
	scrollToSubID        string

	ruleScrollKey      string
	ruleScrollRow      int
	ruleScrollRestored bool
	lastRuleQuery      string

	// Install actions in Settings, and what the last check of the command-line tool said.
	serviceBusy bool
	cliBusy     bool
	cli         cliStatus
	cliChecked  bool
	cliFile     string // where the automatic install records done or off

	trafficCancel context.CancelFunc
	since         time.Time // when masir last turned on, as far as this app saw

	trayOn bool // the tray shows the connected icon

	// Transitions and the window's height. The springs say how far each part is open; the
	// view steps them from the frame's clock, and the window is resized every frame while
	// it moves, following them as a window of its own does.
	motion       motion
	lastMotion   time.Time          // the frame the springs last stepped on
	lastSize     time.Time          // when a window that follows the springs last asked for its height
	frameSize    bool               // the window can follow the springs every frame
	subpageMove  bool               // slide between two subpages, independently of the main page
	holdPage     bool               // the frame a page change starts, held for its pages
	pinMotion    bool               // the frame after it, so that frame's work is not stepped
	noWarm       bool               // tests skip the off-screen warm of the settings page
	warmed       map[pagePath]bool  // and the pages that were built that way
	lastSub      pagePath           // the page that slides over the main one, kept while it slides out
	fromHeight   float32            // the height of the page a change leaves
	toHeight     float32            // and of the page it goes to
	mainHeight   float32            // the main page's height as last measured
	measuredOpen section            // the section that was open when mainHeight was measured
	baseHeight   float32            // the main page's height with both lists closed
	savedHeight  float32            // the base height last written to heightFile
	heightFile   string             // where the drawn height is remembered across launches
	heightWrites atomic.Uint64      // the scheduled height write that still stands
	heightMu     sync.Mutex         // orders a scheduled write with one at quit
	askedHeight  float32            // the height last given to the window, so data arriving at rest resizes it once
	natural      map[string]float32 // the height each list has when fully open
	serverListH  float32            // the server list's height with no search, so filtering keeps it
	ruleListH    float32            // the rules list's height with no search, so filtering keeps it
	pend         pending            // changes shown ahead of the daemon

	// Rules: the swipe of each row, which row is open, the right-click menu, the app names
	// and icons, and where the list was last scrolled to.
	swipes         map[string]*swipe
	openSwipe      string
	menu           ruleMenu
	apps           *appInfos
	favicons       *faviconStore
	rulesMetas     map[string]ruleMeta
	rulesList      ui.ListState
	ruleItems      []ruleListItem
	lastRuleTop    int
	lastRuleEnd    int
	serversList    ui.ListState
	serverItems    []serverListItem
	pickerParent   func() *mygo.Window
	openRuleDialog func(mygo.OpenDialogOptions) ([]string, error)
	onHeight       func(float32) // tells the window how tall the content is at rest

	version          string
	amneziaWGVersion string
	palette          palette // the colors of the frame being built, for helpers that format text
}

func newApp(d pb.DaemonServiceClient, binary string) *app {
	a := &app{
		d:              d,
		binary:         binary,
		disconnectQuit: true,
		post:           func(fn func()) { fn() },
		async:          func(fn func()) { go fn() },
		natural:        map[string]float32{},
		pend:           newPending(),
		swipes:         map[string]*swipe{},
		apps:           newAppInfos(),
		favicons:       newFaviconStore(),
		latency:        map[serverKey]int64{},
		latencyAt:      map[serverKey]int64{},
		testing:        map[serverKey]bool{},
		collapsedSubs:  map[string]bool{},
		collapsedRules: map[string]bool{},
		rulesMetas:     map[string]ruleMeta{},
		warmed:         map[pagePath]bool{},
		lastRuleTop:    -1,
		lastRuleEnd:    -1,
		lastServerTop:  -1,
		version:        version,
	}
	a.installRouter()
	a.rulesList.Key = func(i int) any {
		if i < 0 || i >= len(a.ruleItems) {
			return nil
		}
		it := a.ruleItems[i]
		if it.rule == nil {
			return "h:" + it.title
		}
		return it.rule.RuleTag
	}
	a.rulesList.Header = func(i int) bool {
		return i >= 0 && i < len(a.ruleItems) && a.ruleItems[i].rule == nil
	}
	a.rulesList.Label = func(i int) string {
		if i < 0 || i >= len(a.ruleItems) {
			return ""
		}
		it := a.ruleItems[i]
		if it.rule == nil {
			return it.title
		}
		return a.metaFor(it.rule).info.title
	}
	a.serversList.Key = func(i int) any {
		if i < 0 || i >= len(a.serverItems) {
			return nil
		}
		it := a.serverItems[i]
		if it.group != nil {
			return "g:" + it.group.id
		}
		return it.srv.key
	}
	a.serversList.Header = func(i int) bool {
		return i >= 0 && i < len(a.serverItems) && a.serverItems[i].group != nil
	}
	a.serversList.Label = func(i int) string {
		if i < 0 || i >= len(a.serverItems) {
			return ""
		}
		it := a.serverItems[i]
		if it.group != nil {
			return it.group.name
		}
		_, title, _ := serverTitle(it.srv.cfg.GetName())
		return title
	}
	return a
}

// rpcTimeout bounds one daemon call; fetching a subscription can take a while.
const rpcTimeout = 90 * time.Second

func rpc() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rpcTimeout)
}

// quitTimeout bounds the disconnect a quit sends: the daemon answers on a local socket in
// milliseconds, and a quit must never hang on one that doesn't answer at all.
const quitTimeout = 2 * time.Second

// fail tells the person what went wrong with an action.
func (a *app) fail(prefix string, err error) {
	if isConnectionDrop(err) {
		a.notifyError(describe(err))
		return
	}
	a.notifyError(prefix + describe(err))
}

// describe turns a daemon error into one line for a toast.
func describe(err error) string {
	return humanizeRPCError(err)
}

// ---- state from the daemon ----

// applyStatus takes a status from the stream and refetches the lists when something they
// show changed.
func (a *app) applyStatus(st *pb.Status) {
	wasOn := a.connected()
	previousConnection := a.connectionIdentity()
	a.reachable = true
	a.status = st
	if previousConnection != a.connectionIdentity() {
		a.connection.checked = false
		a.connection.generation++
	}
	if st.GetServiceInstalled() != a.lastServiceInstalled {
		a.lastServiceInstalled = st.GetServiceInstalled()
		a.binary = daemonBinary()
	}
	a.pend.caughtUp()
	if a.connected() && !wasOn {
		a.since = time.Now()
	} else if !a.connected() {
		a.since = time.Time{}
	}
	// The fields on the settings page are only filled while the page isn't being
	// edited: a status arriving mid-typing must not overwrite what was typed.
	if a.path != settingsPath {
		a.syncTextSettings(st.GetSettings())
	}
	if a.refreshRev == 0 || st.Revision != a.refreshRev {
		a.refreshRev = st.Revision
		a.refreshLists()
	}
	a.observeUpdates(st)
}

func (a *app) setUnreachable() {
	a.connection.checked = false
	a.connection.generation++
	a.reachable = false
	a.down, a.up = 0, 0
	a.refreshServiceState()
}

// refreshLists fetches subscriptions with their configs, and the rules.
func (a *app) refreshLists() {
	a.listRefresh++
	refresh := a.listRefresh
	a.async(func() {
		ctx, cancel := rpc()
		defer cancel()
		subsRes, err := callRPCContext(ctx, func(ctx context.Context) (*pb.ListSubscriptionsResponse, error) {
			return a.d.ListSubscriptions(ctx, &pb.ListSubscriptionsRequest{IncludeConfigs: true})
		})
		if err != nil {
			a.post(func() {
				if refresh == a.listRefresh {
					a.refreshRev = 0 // let the next status retry even at the same revision
				}
			})
			return
		}
		var servers []server
		for _, s := range subsRes.GetSubscriptions() {
			for _, cfg := range s.GetConfigs() {
				servers = append(servers, server{
					key:     serverKey{s.Id, cfg.Id},
					subName: s.Name,
					cfg:     cfg,
					search:  serverSearchText(cfg.GetName(), s.Name, cfg),
				})
			}
		}

		rulesRes, rerr := callRPCContext(ctx, func(ctx context.Context) (*pb.ListRoutingRulesResponse, error) {
			return a.d.ListRoutingRules(ctx, &pb.ListRoutingRulesRequest{})
		})

		a.post(func() {
			if refresh != a.listRefresh {
				return
			}
			a.subscriptions.subscriptions = subsRes.GetSubscriptions()
			a.subscriptions.servers = servers
			for _, s := range servers {
				if r := s.cfg.GetLatency(); r.GetTestedAtMs() > a.latencyAt[s.key] && !a.testing[s.key] {
					a.recordLatency(s.key, r)
				}
			}
			if rerr == nil {
				a.rules.rules = rulesRes.GetRules()
				a.indexRules()
				a.prefetchAppIcons()
				a.prefetchFavicons()
			}
		})
	})
}

// watchTraffic streams speeds while the panel is open.
func (a *app) watchTraffic() {
	a.stopTraffic()
	ctx, cancel := context.WithCancel(context.Background())
	a.trafficCancel = cancel
	a.async(func() {
		// Every post invalidates the window. Keep equal samples (including
		// repeated zeroes and stream-error resets) off the UI queue.
		var lastDown, lastUp uint64
		sent := false
		publish := func(down, up uint64) {
			if sent && down == lastDown && up == lastUp {
				return
			}
			sent, lastDown, lastUp = true, down, up
			a.post(func() { a.down, a.up = down, up })
		}
		for ctx.Err() == nil {
			stream, err := a.d.WatchTraffic(ctx, &pb.WatchTrafficRequest{IntervalMs: 1000}, grpc.WaitForReady(false))
			if err == nil {
				for {
					res, err := stream.Recv()
					if err != nil {
						break
					}
					publish(res.DownlinkBps, res.UplinkBps)
				}
			}
			publish(0, 0)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	})
}

func (a *app) stopTraffic() {
	if a.trafficCancel != nil {
		a.trafficCancel()
		a.trafficCancel = nil
	}
}

// ---- actions ----

// connected reports whether masir is on and the daemon agrees.
func (a *app) connected() bool {
	return a.status != nil && a.status.Settings.GetPower() && a.status.XrayRunning
}

// running reports whether the person asked for masir on, whatever Xray says yet.
func (a *app) wantsOn() bool { return a.status != nil && a.status.Settings.GetPower() }

// run does a daemon call off the main thread; the status stream shows its effect, and a
// failure becomes a toast.
func (a *app) run(failPrefix string, fn func(ctx context.Context) error) {
	a.mutate(failPrefix, nil, nil, fn)
}
