package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type ruleApp struct {
	Name, Path, Icon string
	Running          bool
}

const (
	ruleAppsAll = iota
	ruleAppsManual
)

const ruleAppLineBreakError = "Choose paths without line breaks. No paths were added."

type ruleAppItem struct {
	group         string
	app           ruleApp
	selectedGroup bool
}

type ruleAppState struct {
	dirty                            bool
	count                            int
	all, filtered, chosen            []ruleApp
	items                            []ruleAppItem
	query, lastQuery, err, lastMatch string
	tab, lastTab                     int
	loading, picking                 bool
	list                             ui.ListState
	selected                         map[string]bool
	custom                           map[string]ruleApp
	session                          *int
}

func (a *app) loadRuleApps() {
	token := new(int)
	a.ruleApps = ruleAppState{loading: true, lastTab: -1, session: token, custom: map[string]ruleApp{}}
	a.syncRuleAppSelection()
	a.async(func() {
		apps, err := discoverRuleApps()
		a.post(func() {
			if a.ruleApps.session != token {
				return
			}
			a.ruleApps.all = apps
			a.ruleApps.loading = false
			if err != nil && a.ruleApps.err == "" {
				a.ruleApps.err = err.Error()
			}
			a.filterRuleApps()
		})
	})
}

func (a *app) syncRuleAppSelection() {
	st := &a.ruleApps
	match := a.rulePage.values["process"]
	if st.selected != nil && st.lastMatch == match {
		return
	}
	st.selected = map[string]bool{}
	for _, path := range ruleEntries("process", match) {
		st.selected[path] = true
	}
	st.lastMatch = match
	st.count = len(ruleEntries("process", match))
	st.dirty = true
}

func (a *app) filterRuleApps() {
	st := &a.ruleApps
	resetScroll := st.query != st.lastQuery || st.tab != st.lastTab
	st.filtered = nil
	q := strings.ToLower(strings.TrimSpace(st.query))
	known := map[string]bool{}
	add := func(v ruleApp) {
		if q == "" || strings.Contains(strings.ToLower(v.Name+" "+v.Path), q) {
			st.filtered = append(st.filtered, v)
		}
	}
	st.chosen = nil
	for _, v := range st.all {
		known[v.Path] = true
		add(v)
		if st.selected[v.Path] {
			st.chosen = append(st.chosen, v)
		}
	}
	for path, on := range st.selected {
		if !on || known[path] {
			continue
		}
		v, ok := st.custom[path]
		if !ok {
			v = ruleApp{Name: fallbackName(path), Path: path, Icon: strings.TrimSuffix(path, "/")}
		}
		st.chosen = append(st.chosen, v)
	}
	sortApps := func(a, b ruleApp) int {
		if n := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	}
	slices.SortFunc(st.filtered, sortApps)
	slices.SortFunc(st.chosen, sortApps)
	st.items = nil
	if len(st.chosen) > 0 {
		st.items = append(st.items, ruleAppItem{group: tr("Selected")})
		for _, v := range st.chosen {
			st.items = append(st.items, ruleAppItem{app: v, selectedGroup: true})
		}
	}
	if len(st.filtered) > 0 {
		st.items = append(st.items, ruleAppItem{group: tr("Apps")})
		for _, v := range st.filtered {
			st.items = append(st.items, ruleAppItem{app: v})
		}
	}
	st.lastQuery, st.lastTab = st.query, st.tab
	st.dirty = false
	if resetScroll {
		st.list.ScrollTo(0, ui.Start)
	}
	st.list.Key = func(i int) any {
		v := st.items[i]
		return fmt.Sprintf("%s/%t/%s", v.group, v.selectedGroup, v.app.Path)
	}
	st.list.Header = func(i int) bool { return st.items[i].group != "" }
	st.list.Label = func(i int) string {
		v := st.items[i]
		if v.group != "" {
			return v.group
		}
		return v.app.Name
	}
}

// A selected path must fit on one line of the manual process editor.
func (st *ruleAppState) checkSelectionPaths(paths ...string) bool {
	for _, path := range paths {
		if strings.ContainsAny(path, "\r\n") {
			st.err = ruleAppLineBreakError
			return false
		}
	}
	if len(paths) > 0 && st.err == ruleAppLineBreakError {
		st.err = ""
	}
	return true
}

func (a *app) addPickedRuleApps(paths []string, directory bool) {
	st := &a.ruleApps
	if !st.checkSelectionPaths(paths...) {
		return
	}
	if st.custom == nil {
		st.custom = map[string]ruleApp{}
	}
	for _, path := range paths {
		if directory || strings.HasSuffix(path, ".app") {
			path = strings.TrimRight(path, "/\\") + "/"
		}
		st.selected[path] = true
		st.custom[path] = ruleApp{Name: fallbackName(path), Path: path, Icon: strings.TrimSuffix(path, "/")}
	}
	if len(paths) > 0 {
		st.tab = ruleAppsAll
		st.query = ""
		a.applyRuleApps()
	}
}
func (a *app) pickRuleApp(directory bool) {
	st := &a.ruleApps
	if st.picking || a.rulePage.busy {
		return
	}
	st.picking = true
	token := st.session
	options := mygo.OpenDialogOptions{Title: tr("Choose application"), Directory: directory, Multiple: true}
	if directory {
		options.Title = tr("Choose folder")
	} else if runtime.GOOS == "darwin" {
		options.DefaultPath = "/Applications"
	}
	if a.pickerParent != nil {
		options.Parent = a.pickerParent()
	}
	open := a.openRuleDialog
	if open == nil {
		open = mygo.Dialog.Open
	}
	a.async(func() {
		paths, err := open(options)
		a.post(func() {
			if a.ruleApps.session != token {
				return
			}
			st.picking = false
			if err != nil {
				st.err = err.Error()
				return
			}
			a.addPickedRuleApps(paths, directory)
		})
	})
}

func (a *app) ruleAppsEditor(c *ui.Context, p palette) {
	st := &a.ruleApps
	a.syncRuleAppSelection()
	ui.Column(c).Shrink(0).Children(func() {
		ui.Row(c).Padding(6, 8).Shrink(0).Children(func() {
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() { ruleSegments(c, "app-scope", &st.tab, p, tr("Apps"), tr("Manual")) })
		})
		panelBar(c, p, func() {
			if st.tab == ruleAppsManual {
				label(c, tr("Executable names and paths"), sizeCaption, 16, 500, p.muted).Grow(1)
			} else {
				searchInput(c, "app-search", &st.query, tr("Search applications…"), p)
			}
		}, func() {
			ui.Row(c).Gap(2).Children(func() {
				if iconButton(c, "pick-executable", iconApp, tr("Choose application"), p, iconMD, variantSubtle, 12) && !st.picking {
					a.pickRuleApp(false)
				}
				if iconButton(c, "pick-directory", iconFolder, tr("Choose folder"), p, iconMD, variantSubtle, 12) && !st.picking {
					a.pickRuleApp(true)
				}
			})
		})
		if st.tab == ruleAppsManual {
			ui.Column(c).Padding(8).Gap(6).Children(func() {
				label(c, tr("One executable name or full path per line. End a folder path with / to include its applications."), sizeMicro, 15, 400, p.muted)
				value := a.rulePage.values["process"]
				labeledInput(c, p, "process-paths", tr("Application paths"), &value, tr("Any application"), iconTerminal, p.muted, 116, nil).AutoFocus()
				if value != a.rulePage.values["process"] {
					a.rulePage.values["process"] = value
					a.syncRuleAppSelection()
				}
			})
		} else {
			if st.query != st.lastQuery || st.tab != st.lastTab || st.dirty {
				a.filterRuleApps()
			}
			if st.loading {
				ui.Row(c).Padding(10).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(12, 12)
					label(c, tr("Finding installed applications…"), sizeCaption, 16, 400, p.muted)
				})
			}
			if !st.loading && len(st.filtered) == 0 && st.err == "" {
				message := tr("No installed applications found. Choose an app or folder, or use Manual.")
				switch {
				case st.query != "":
					message = tr("No matching applications.")
				}
				label(c, message, sizeCaption, 16, 400, p.muted).Padding(12, 10)
			}
			height := float32(8)
			for _, item := range st.items {
				if item.group != "" {
					height += 25
				} else {
					height += 48
				}
			}
			containListScroll(ui.List(c, &st.list, len(st.items), func(i int) {
				item := st.items[i]
				if item.group != "" {
					ui.Row(c).Label(tr("Application group ")+item.group).Height(22).Padding(0, 6).AlignItems(ui.Center).Background(p.listHeader).Children(func() { label(c, strings.ToUpper(item.group), sizeMicro, 14, 600, p.muted).SingleLine() })
					return
				}
				a.ruleAppRow(c, p, item.app)
			})).Label(tr("Application choices")).Height(min(208, height)).Shrink(0).Padding(4, 6).Gap(3).ScrollbarInsets(0, 1, 0)
		}
		if st.err != "" {
			label(c, st.err, sizeCaption, 16, 400, p.errorC).Padding(6, 10)
		}
		ui.Row(c).Label(tr("Application selection status")).Padding(6, 10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			count := st.count
			title := tr("Any application")
			if count > 0 {
				title = fmt.Sprintf(tr("%d selected"), count)
			}
			label(c, title, sizeMicro, 15, 400, p.muted)
			if count > 0 && ui.Button(c.Key("clear-apps"), tr("Reset to Any")).Label(tr("Clear application selection")).Disabled(a.rulePage.busy).Clicked() {
				a.rulePage.values["process"] = ""
				a.syncRuleAppSelection()
			}
		})
	})
}

func (a *app) ruleAppRow(c *ui.Context, p palette, v ruleApp) {
	st := &a.ruleApps
	selected := st.selected[v.Path]
	row := ui.ToggleBase(c.Key(v.Path), &selected).Label(tr("Select ")+v.Name).Disabled(a.rulePage.busy).Padding(6).Gap(8).Radius(6).AlignItems(ui.Center).Border(1, ui.Transparent).Cursor(ui.CursorPointer)
	if selected {
		row.Background(p.accentSurface).Border(1, p.borderFocus)
	} else if row.Hovered() {
		row.Background(p.cardHover)
	}
	row.Children(func() {
		meta := a.apps.lookup(v.Icon, a.async, func() { a.post(func() {}) })
		_, icon := a.apps.read(meta)
		iconTile(c, p, func() {
			if icon != nil {
				ui.Image(c, icon).Size(tw(7), tw(7)).Radius(4).MarginY(-4).Label(tr("Picker application icon"))
			} else {
				ui.Icon(c, iconApp).FontSize(14).TextColor(p.muted)
			}
		})
		label(c, v.Name, sizeBody, 16, 500, p.text).Grow(1).Basis(0).MinWidth(0).SingleLine()
		if v.Running {
			label(c, tr("Running"), sizeMicro, 14, 400, p.healthy).Label(tr("Application is running")).Shrink(0)
		}

		if selected {
			ui.Icon(c, iconCheck).FontSize(12).TextColor(p.accent).Shrink(0)
		}
	})
	if row.Changed() && (!selected || st.checkSelectionPaths(v.Path)) {
		st.selected[v.Path] = selected
		a.applyRuleApps()
	}
}

// Discovery runs outside the frame. Launcher identities are resolved to process
// paths; desktop files and shell:AppsFolder identifiers are never routing matches.
func discoverRuleApps() ([]ruleApp, error) {
	if !mygo.App.IsReady() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var installed []mygo.InstalledApp
	var err error
	if runtime.GOOS != "windows" {
		installed, err = mygo.Shell.InstalledApps()
	}
	var out []ruleApp
	if runtime.GOOS == "windows" {
		// Start-menu shortcuts carry the actual executable, unlike AppsFolder IDs.
		data, e := ruleCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `[Console]::OutputEncoding=[System.Text.Encoding]::UTF8
$w=New-Object -ComObject WScript.Shell
$desktop=@(@(($env:APPDATA+'\Microsoft\Windows\Start Menu\Programs'),($env:ProgramData+'\Microsoft\Windows\Start Menu\Programs')) | ForEach-Object {
 Get-ChildItem -LiteralPath $_ -Filter *.lnk -Recurse -ErrorAction SilentlyContinue
} | ForEach-Object {
 $s=$w.CreateShortcut($_.FullName)
 if($s.TargetPath -like '*.exe'){[pscustomobject]@{Name=$_.BaseName;Path=$s.TargetPath;Icon=$s.TargetPath}}
})
$packaged=@(Get-AppxPackage -ErrorAction SilentlyContinue | ForEach-Object {
 $p=$_
 $m=Get-AppxPackageManifest -Package $p -ErrorAction SilentlyContinue
 foreach($a in $m.Package.Applications.Application){
  if($a.Executable -and $a.Executable -notmatch '\$'){
   $path=Join-Path $p.InstallLocation $a.Executable
   if(Test-Path -LiteralPath $path -PathType Leaf){[pscustomobject]@{Name=$p.Name;Path=$path;Icon=$path}}
  }
 }
})
ConvertTo-Json -InputObject @($desktop+$packaged) -Compress`).Output()
		if e == nil {
			e = json.Unmarshal(data, &out)
		}
		if e != nil {
			err = fmt.Errorf("%s", tr("Couldn't list installed apps; choose an executable manually."))
		}
	} else {
		for _, v := range installed {
			path := v.Path
			if runtime.GOOS == "linux" {
				path = desktopExecutable(path)
				if path == "" {
					continue
				}
			}
			if runtime.GOOS == "darwin" {
				path = strings.TrimSuffix(path, "/") + "/"
			}
			out = append(out, ruleApp{Name: v.Name, Path: path, Icon: v.Path})
		}
	}
	running, runErr := runningRuleApps(ctx)
	if runErr != nil && err == nil {
		err = fmt.Errorf("%s", tr("Couldn't list running apps; installed apps and manual picking are still available."))
	}
	return mergeInstalledRuleApps(out, running, runtime.GOOS), err
}

// Process enumeration only annotates installed launchers. A service or helper can
// never turn into an application choice just because it is running.
func mergeInstalledRuleApps(installed, running []ruleApp, platform string) []ruleApp {
	active := map[string]bool{}
	for _, v := range running {
		active[ruleAppIdentity(v.Path, platform)] = true
	}
	seen := map[string]bool{}
	var out []ruleApp
	for _, v := range installed {
		key := ruleAppIdentity(v.Path, platform)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		v.Running = active[key]
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b ruleApp) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}
func ruleAppIdentity(path, platform string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if platform == "darwin" {
		if i := strings.Index(path, ".app/"); i >= 0 {
			path = path[:i+4]
		}
	}
	path = strings.TrimRight(path, "/")
	if platform == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func runningRuleApps(ctx context.Context) ([]ruleApp, error) {
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		data, err := ruleCommand(ctx, "ps", "-axo", "comm=").Output()
		if err != nil {
			return nil, err
		}
		paths = strings.Split(string(data), "\n")
	case "linux":
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return nil, err
		}
		for _, v := range entries {
			if path, err := os.Readlink(filepath.Join("/proc", v.Name(), "exe")); err == nil && !strings.HasSuffix(path, " (deleted)") {
				paths = append(paths, path)
			}
		}
	case "windows":
		data, err := ruleCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; Get-Process | ForEach-Object { if ($_.Path) { $_.Path } }`).Output()
		if err != nil {
			return nil, err
		}
		paths = strings.Split(string(data), "\n")
	}
	var out []ruleApp
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			continue
		}
		if i := strings.Index(path, ".app/"); i >= 0 {
			path = path[:i+5]
		}
		out = append(out, ruleApp{Name: fallbackName(path), Path: path, Icon: strings.TrimSuffix(path, "/"), Running: true})
	}
	return out, nil
}
func desktopExecutable(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	section := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line == "[Desktop Entry]"
		}
		if !section || !strings.HasPrefix(line, "Exec=") {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, "Exec="))
		var token strings.Builder
		quoted, escaped := false, false
		for _, r := range command {
			if escaped {
				token.WriteRune(r)
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				quoted = !quoted
				continue
			}
			if !quoted && (r == ' ' || r == '\t') {
				break
			}
			token.WriteRune(r)
		}
		name := token.String()
		if name == "env" || name == "flatpak" || name == "snap" || name == "sh" || name == "bash" || name == "gtk-launch" {
			return ""
		}
		if name == "" || strings.Contains(name, "%") {
			return ""
		}
		resolved, err := exec.LookPath(name)
		if err == nil {
			if real, err := filepath.EvalSymlinks(resolved); err == nil {
				resolved = real
			}
			return resolved
		}
		return ""
	}
	return ""
}

func (a *app) applyRuleApps() {
	var paths []string
	for path, on := range a.ruleApps.selected {
		if on {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	a.rulePage.values["process"] = strings.Join(paths, "\n")
	a.ruleApps.lastMatch = a.rulePage.values["process"]
	a.ruleApps.count = len(paths)
	a.ruleApps.dirty = true
}
