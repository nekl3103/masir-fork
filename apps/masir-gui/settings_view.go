package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) settingsView(c *ui.Context, p palette) {
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(pageInset, tw(4), 0, tw(4)).Shrink(0).Children(func() {
			a.pageHeader(c, p, tr("Settings"))
		})
		// The scroll itself runs to the panel's edge, so its bar sits beside the cards
		// instead of over them; the sections keep the same margins inside it.
		ui.Scroll(c.Key("settings-scroll")).Grow(1).Basis(0).MinHeight(0).
			ScrollbarInsets(0, 2, 0).Children(func() {
			ui.Column(c).Gap(tw(3)).Padding(0, tw(4), tw(0), tw(4)).Shrink(0).Children(func() {
				a.languageSection(c, p)
				a.advancedSection(c, p)
				a.dnsSection(c, p)
				a.loggingSection(c, p)
				a.privacySection(c, p)
				a.systemSection(c, p)
				ui.Column(c.Key("app")).Gap(tw(3)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Shrink(0).Children(func() {
					ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Start).FillWidth().Gap(tw(0.5)).Grow(1).Children(func() {
						ui.Link(c, "GitHub · main ↗", "https://github.com/nekl3103/masir-fork/tree/main").FontSize(10).FixedLineHeight(13).FontWeight(300).TextColor(ui.Hex("#73b0ff"))
						ui.Column(c).Gap(tw(0.5)).Children(func() {
							if version := a.status.GetXrayVersion(); version != "" {
								mono(c, "Xray "+versionLabel(version), sizeMicro, 13, 400, p.muted).MaxLines(2)
							}
						})
					})
					if a.showUpdateControl() {
						ui.Row(c).FillWidth().Gap(tw(1)).AlignItems(ui.Center).Children(func() {
							a.updateControl(c, p, true)
						})
					}
				})
				ui.Column(c.Key("versions")).Gap(tw(0.5)).Shrink(0).Children(func() {
					daemon := tr("Daemon unavailable")
					if version := a.status.GetVersion(); version != "" {
						daemon = tr("Daemon · ") + strings.TrimSpace(a.status.GetDistribution()+" "+versionLabel(version))
					}
					label(c, daemon, sizeCaption, 16, 400, p.muted)
					label(c, "Masir Fork "+versionLabel(a.version), sizeCaption, 16, 500, p.text)
					if engine := a.status.GetAmneziawgVersion(); engine != "" {
						label(c, "AmneziaWG "+versionLabel(engine), sizeCaption, 16, 400, p.muted)
					} else {
						label(c, tr("AmneziaWG version unavailable"), sizeCaption, 16, 400, p.muted)
					}
				})
				ui.Column(c).Padding(0, 0, tw(4)).Children(func() {
					if button(c, "quit", tr("Quit Masir"), false, false, false, p) {
						quit()
					}
				})
			})
		})
	})
}

func (a *app) languageSection(c *ui.Context, p palette) {
	cardBox(c, p, func() {
		a.selectRow(c, p, "language", tr("Language"), "", appLanguage(),
			[]choice{{"en", "English"}, {"ru", "Русский"}}, false, setAppLanguage)
	})
}

func (a *app) advancedSection(c *ui.Context, p palette) {
	titled(c, p, tr("ADVANCED"), nil, func() {
		cardBox(c, p, func() {
			hint := tr("Block VPN traffic when no VPN route is available.")
			if runtime.GOOS == "darwin" {
				hint = tr("Block VPN traffic on connection failure. With TUN enabled, also block network traffic if Masir stops unexpectedly.")
			}
			a.settingToggle(c, p, "kill-switch", tr("Kill Switch"), hint, a.killSwitchOn(), a.setKillSwitch)
			cardDivider(c, p)
			always := a.alwaysOnProxy()
			a.settingToggle(c, p, "always", tr("Always-on Proxy Port"), tr("A local proxy port always available for apps you configure manually, using your active outbound."), always, func(on bool) {
				a.setAlwaysOnProxy(on)
			})
			cardDivider(c, p)
			ui.Column(c.Key("port-row")).Padding(tw(2.5), tw(3)).Gap(tw(1)).Shrink(0).Children(func() {
				in := labeledInput(c, p, "port", tr("Port Number"), &a.portText, "7890", iconHash, p.muted, inputHeight, nil)
				if in.Changed() {
					a.portText = digitsOnly(a.portText)
				}
				if in.Submitted() || (!in.Focused() && a.portEdited) {
					a.applyPort()
				}
				if in.Focused() {
					a.portEdited = true
				}

				label(c, tr("Mixed SOCKS5 / HTTP"), sizeCaption, 14.85, 400, p.muted)
			})
			cardDivider(c, p)
			lan := a.allowLAN()
			a.settingToggle(c, p, "lan", tr("Share on the Network"), tr("Let other devices on your network use the local proxy."), lan, func(on bool) {
				a.setAllowLAN(on)
			})
			cardDivider(c, p)
			login := a.loginEnabled()
			hint = tr("Start Masir in the menu bar when you sign in.")
			if a.loginNeedsApproval {
				hint = tr("Allow Masir in Login Items to start at login.")
			}
			a.settingToggle(c, p, "login", tr("Open at Login"), hint, login, func(on bool) {
				a.setLoginEnabled(on)
			})
			if a.loginNeedsApproval && runtime.GOOS == "darwin" {
				ui.Row(c).Padding(0, tw(3), tw(2.5)).Children(func() {
					if button(c, "login-items", tr("Open Login Items"), false, false, false, p) {
						if err := openLoginItems(); err != nil {
							a.fail(tr("Couldn't open Login Items: "), err)
						}
					}
				})
			}
			cardDivider(c, p)
			a.settingToggle(c, p, "disconnect-quit", tr("Disconnect on Quit"), tr("Turn off the connection when you quit Masir."), a.disconnectOnQuitEnabled(), func(on bool) {
				a.setDisconnectOnQuit(on)
			})
			cardDivider(c, p)
			minutes := a.subInterval()
			a.selectRow(c, p, "interval", tr("Refresh Subscriptions"),
				tr("How often masir checks each subscription for new servers."),
				strconv.Itoa(int(minutes)), intervalChoices(minutes), false, func(v string) {
					n, err := strconv.Atoi(v)
					if err != nil {
						return
					}
					a.setSubInterval(uint32(n))
				})
		})
	})
}

// settingToggle is a card row with a title, a subdued switch and a hint under them.
func (a *app) settingToggle(c *ui.Context, p palette, key, title, hint string, on bool, set func(bool)) {
	ui.Row(c.Key("toggle-"+key)).Padding(tw(2.5), tw(3)).Gap(tw(3)).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Column(c).Gap(tw(0.5)).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ui.Row(c).Gap(tw(2)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				label(c, title, sizeSubhead, 18, 500, p.text)
				if next, changed := nativeSwitch(c, key, title, on, switchSubdued, false, false, p); changed {
					set(next)
				}
			})
			label(c, hint, sizeCaption, 14.85, 400, p.muted)
		})
	})
}

// ---- dns, logging and privacy ----

// choice is one option of a drop-down: the value saved, and how it reads.
type choice struct{ value, label string }

// gatewayChoices is the addresses v1 offered, plus this version's default. The current
// value is kept as an option of its own when it came from the command line.
func gatewayChoices(current string) []choice {
	out := []choice{
		{"10.10.20.1/24", "10.10.20.1/24"},
		{"10.10.10.1/24", "10.10.10.1/24"},
		{"10.0.0.1/24", "10.0.0.1/24"},
		{"172.19.0.1/24", "172.19.0.1/24"},
		{"192.168.127.1/24", "192.168.127.1/24"},
		{"198.18.0.1/24", "198.18.0.1/24"},
	}
	for _, o := range out {
		if o.value == current {
			return out
		}
	}
	if current != "" {
		out = append([]choice{{current, current}}, out...)
	}
	return out
}

// intervalChoices is the subscription refresh interval, in minutes.
func intervalChoices(current uint32) []choice {
	out := []choice{
		{"15", tr("Every 15 minutes")},
		{"30", tr("Every 30 minutes")},
		{"60", tr("Every hour")},
		{"180", tr("Every 3 hours")},
		{"720", tr("Every 12 hours")},
		{"1440", tr("Every 24 hours")},
	}
	for _, o := range out {
		if o.value == strconv.Itoa(int(current)) {
			return out
		}
	}
	return append([]choice{{strconv.Itoa(int(current)), intervalLabel(current)}}, out...)
}

// intervalLabel reads a refresh interval as the choice row shows it.
func intervalLabel(minutes uint32) string {
	switch {
	case minutes == 720:
		return tr("Every 12 hours")
	case minutes%1440 == 0:
		return fmt.Sprintf(tr("Every %d day(s)"), minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf(tr("Every %d hour(s)"), minutes/60)
	default:
		return fmt.Sprintf(tr("Every %d minutes"), minutes)
	}
}

// logLevelChoices is Xray's log level, quietest first.
func logLevelChoices() []choice {
	return []choice{
		{"none", tr("Off")},
		{"error", tr("Error")},
		{"warning", tr("Warning")},
		{"info", tr("Info")},
		{"debug", tr("Debug")},
	}
}

// dohChoices is the DNS-over-HTTPS resolvers the app offers; a resolver set from the
// command line is kept as an option of its own.
func dohChoices(current string) []choice {
	out := []choice{
		{"https://1.1.1.1/dns-query", "https://1.1.1.1/dns-query"},
		{"https://dns.google/dns-query", "https://dns.google/dns-query"},
		{"https://dns.quad9.net/dns-query", "https://dns.quad9.net/dns-query"},
		{"https://dns.adguard-dns.com/dns-query", "https://dns.adguard-dns.com/dns-query"},
	}
	for _, o := range out {
		if o.value == current {
			return out
		}
	}
	if current != "" {
		out = append([]choice{{current, current}}, out...)
	}
	return out
}

// settingHead is the title and hint at the top of a settings row.
func settingHead(c *ui.Context, p palette, title, hint string) ui.Element {
	return ui.Column(c).Gap(tw(0.5)).Children(func() {
		label(c, title, sizeSubhead, 18, 500, p.text)
		if hint != "" {
			label(c, hint, sizeCaption, 14.85, 400, p.muted)
		}
	})
}

// labelOf is the label of a choice, or the value itself when it isn't one of them.
func labelOf(opts []choice, value string) string {
	for _, o := range opts {
		if o.value == value {
			return o.label
		}
	}
	return value
}

// selectRow is a card row with a title, a hint and a drop-down of choices. Inline puts the
// value at the row's end; otherwise the drop-down spans the row under the hint, for a
// value too wide to share the line.
func (a *app) selectRow(c *ui.Context, p palette, key, title, hint, current string, opts []choice, inline bool, set func(string)) {
	selected := labelOf(opts, current)
	byLabel := make(map[string]string, len(opts))
	for _, o := range opts {
		byLabel[o.label] = o.value
	}
	draw := func() {
		sel := ui.SelectBase(c, &selected)
		sel.Trigger.Label(title).Cursor(ui.CursorPointer).Height(inputHeight).Padding(0, 10).
			Radius(inputRadius).Gap(6).AlignItems(ui.Center).
			Background(p.subdued).Border(1, p.borderDefault)
		if inline {
			sel.Trigger.Shrink(0)
		} else {
			sel.Trigger.Justify(ui.SpaceBetween)
		}
		bg, border, _ := inputColors(p, sel.Trigger.Focused())
		sel.Trigger.Background(bg).Border(1, border)
		sel.Trigger.Children(func() {
			glyph := iconGlobe
			switch key {
			case "interval":
				glyph = iconClock
			case "gateway":
				glyph = iconLayers
			case "loglevel":
				glyph = iconTerminal
			}
			ui.Icon(c, glyph).FontSize(14).TextColor(p.muted).Shrink(0)
			label(c, selected, sizeSubhead, 16, 400, p.text).Grow(1).Basis(0).MinWidth(0).SingleLine()
			ui.Icon(c, iconChevronDown).FontSize(11).TextColor(p.tertiary).Shrink(0)
		})
		sel.Popup(func(panel ui.Element) {
			panel.Padding(tw(1)).Radius(tw(2)).Background(p.popup).Border(1, p.borderDefault)
			if p.dark {
				panel.Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.35))
			} else {
				panel.Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.12))
			}
			if inline {
				panel.MinWidth(tw(40))
			}
			for _, o := range opts {
				item := sel.Item(o.label).Padding(tw(1.5), tw(2)).Radius(6).Gap(tw(1.5)).
					AlignItems(ui.Center).Cursor(ui.CursorPointer)
				if item.Highlighted() {
					item.Background(p.accentSurface)
				}
				item.Children(func() {
					ui.Box(c).Size(12, 12).Shrink(0).Children(func() {
						if o.value == current {
							ui.Icon(c, iconCheck).FontSize(12).TextColor(p.accent)
						}
					})
					label(c, o.label, sizeCaption, 16, 500, p.text).SingleLine()
				})
			}
		})
		if sel.Trigger.Changed() {
			set(byLabel[selected])
		}
	}
	ui.Column(c.Key("select-"+key)).Padding(tw(2.5), tw(3)).Gap(inputLabelGap).Shrink(0).Children(func() {
		inputLabel(c, p, title)
		draw()
		if hint != "" {
			label(c, hint, sizeCaption, 15, 400, p.muted)
		}
	})
}

// settingField is a card row with a title, a hint and a text field under them.
func (a *app) settingField(c *ui.Context, p palette, key, title, hint, placeholder string, f *textSetting, commit func(string)) {
	ui.Column(c.Key("field-"+key)).Padding(tw(2.5), tw(3)).Gap(tw(1)).Shrink(0).Children(func() {
		in := labeledInput(c, p, key, title, &f.text, placeholder, iconGlobe, p.muted, inputHeight, nil)
		if hint != "" {
			label(c, hint, sizeCaption, 15, 400, p.muted)
		}

		if in.Submitted() || (!in.Focused() && f.edited) {
			commit(f.text)
		}
		if in.Focused() {
			f.edited = true
		}
	})
}

func (a *app) dnsSection(c *ui.Context, p palette) {
	gateway := a.gateway()
	titled(c, p, tr("DNS & GATEWAY"), nil, func() {
		cardBox(c, p, func() {
			a.selectRow(c, p, "gateway", tr("TUN Gateway"),
				tr("The tunnel's own address. Pick one that doesn't clash with your network."),
				gateway, gatewayChoices(gateway), false, a.setGateway)
			cardDivider(c, p)
			doh := a.dohURL()
			a.selectRow(c, p, "doh", tr("DNS over HTTPS"),
				tr("Resolves names for proxied traffic, through the proxy."),
				doh, dohChoices(doh), false, a.setDoH)
			cardDivider(c, p)
			a.settingField(c, p, "dns", tr("Bootstrap DNS"), tr("Plain DNS for proxy server names, sent direct. Separate addresses with commas."),
				"1.1.1.1, 8.8.8.8", &a.dns, a.saveBootstrapDNS)
			cardDivider(c, p)
			a.settingField(c, p, "tundns", tr("TUN DNS"), tr("DNS the tunnel advertises. Leave empty to use the bootstrap DNS."),
				tr("Same as bootstrap DNS"), &a.tunDNS, a.saveTunDNS)
		})
	})
}

// logFollowCommand is how the person watches the daemon's log, which carries Xray's own
// warnings and errors too. It is the v1 panel's live tail, without needing root.
const logFollowCommand = "masir logs --follow"

func (a *app) loggingSection(c *ui.Context, p palette) {
	level := a.logLevel()
	titled(c, p, tr("LOGGING"), nil, func() {
		cardBox(c, p, func() {
			a.selectRow(c, p, "loglevel", tr("Xray Log Level"), "",
				level, logLevelChoices(), true, a.setLogLevel)
			cardDivider(c, p)
			ui.Column(c.Key("follow-log")).Padding(tw(2.5), tw(3)).Gap(tw(1)).Shrink(0).Children(func() {
				label(c, tr("Follow the Log"), sizeSubhead, 18, 500, p.text)
				hint := tr("Xray's own warnings and errors appear in the daemon's log.")
				if level == "none" {
					hint = tr("Nothing is logged while the level is Off.")
				}
				label(c, hint, sizeCaption, 14.85, 400, p.muted)
				ui.Row(c).Margin(4, 0, 0).Gap(tw(1)).AlignItems(ui.Center).Children(func() {
					ui.Row(c).Grow(1).Basis(0).MinWidth(0).Padding(tw(1.5), tw(2)).Radius(tw(1.5)).
						Background(p.subdued).Border(1, p.borderDefault).Children(func() {
						mono(c, logFollowCommand, sizeCaption, 16, 400, p.text).SingleLine().Selectable()
					})
					if iconButton(c, "log-copy", iconCopy, tr("Copy the log command"), p, iconSM, variantSubtle, 13) {
						c.WriteClipboard(logFollowCommand)
						a.notify(tr("Copied ") + logFollowCommand)
					}
					if iconButton(c, "log-terminal", iconTerminal, tr("Open in Terminal"), p, iconSM, variantSubtle, 13) {
						if err := openTerminalIn(logFollowCommand); err != nil {
							a.fail(tr("Couldn't open a terminal: "), err)
						}
					}
				})
				if !a.cliState().installed {
					label(c, tr("Install the command line tool below to run it."), sizeCaption, 14, 400, p.degraded)
				}
			})
			cardDivider(c, p)
			ui.Column(c.Key("xray-config")).Padding(tw(2.5), tw(3)).Gap(tw(1)).Shrink(0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(tw(2)).Children(func() {
					label(c, tr("Xray Config"), sizeSubhead, 18, 500, p.text)
					if thinButton(c, "open-config", tr("Open JSON"), false, false, false, p) {
						a.openXrayConfig()
					}
				})
				label(c, tr("The configuration the daemon is running right now, as masir get-config prints it."),
					sizeCaption, 14.85, 400, p.muted)
			})
		})
	})
}

func (a *app) privacySection(c *ui.Context, p palette) {
	titled(c, p, tr("PRIVACY"), nil, func() {
		cardBox(c, p, func() {
			a.settingToggle(c, p, "analytics", tr("Anonymous Analytics"),
				tr("Sends anonymous usage and crash data to help improve Masir. It never includes addresses, names, or the sites you visit."),
				a.analyticsOn(), a.setAnalytics)
		})
	})
}

func (a *app) systemSection(c *ui.Context, p palette) {
	st := a.status
	cli := a.cliState()
	titled(c, p, tr("SYSTEM"), nil, func() {
		cardBox(c, p, func() {
			ui.Column(c).Padding(tw(2.5), tw(3)).Gap(tw(1.5)).Shrink(0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					label(c, tr("Background Service"), sizeSubhead, 18, 500, p.text)
					switch {
					case st.GetServiceInstalled() && st.GetServiceRunning():
						badge(c, tr("Active"), nil, p.healthy, p.healthySurface, 0, 0)
					case st.GetServiceInstalled() && !st.GetServiceEnabled():
						badge(c, tr("Disabled"), nil, p.degraded, p.degradedSurface, 0, 0)
					case st.GetServiceInstalled():
						badge(c, tr("Stopped"), nil, p.degraded, p.degradedSurface, 0, 0)
					case st.GetPrivileged():
						badge(c, tr("This Session"), nil, p.degraded, p.degradedSurface, 0, 0)
					default:
						badge(c, tr("Not Installed"), nil, p.muted, p.neutralSurface, 0, 0)
					}
				})
				label(c, tr("Starts the daemon at boot, so connecting in tunnel mode never asks for permission again."), sizeCaption, 14.85, 400, p.muted)
				if st.GetServiceInstalled() && !st.GetServiceEnabled() {
					label(c, tr("The system has it turned off. Allow Masir in Login Items, or start it here."), sizeCaption, 14.85, 400, p.degraded)
				}
				ui.Column(c).Gap(tw(1)).Margin(4, 0, 0).Children(func() {
					if st.GetServiceInstalled() {
						if !st.GetServiceRunning() {
							if thinButton(c, "service-start", tr("Start Service"), true, a.serviceBusy, a.serviceBusy, p) {
								a.startDaemonService()
							}
						}
						if runtime.GOOS == "darwin" && !st.GetServiceEnabled() {
							if thinButton(c, "service-login-items", tr("Open Login Items"), false, false, false, p) {
								if err := openLoginItems(); err != nil {
									a.fail(tr("Couldn't open Login Items: "), err)
								}
							}
						}
						if thinButton(c, "service-remove", tr("Remove Service"), false, a.serviceBusy, a.serviceBusy, p) {
							a.removeDaemonService()
						}
					} else if thinButton(c, "service-install", tr("Install Service"), true, a.serviceBusy, a.serviceBusy, p) {
						a.installDaemonService()
					}
				})
			})
			cardDivider(c, p)
			ui.Column(c).Padding(tw(2.5), tw(3)).Gap(tw(1.5)).Shrink(0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					label(c, tr("Command Line Tool"), sizeSubhead, 18, 500, p.text)
					if cli.installed {
						badge(c, tr("Installed"), nil, p.healthy, p.healthySurface, 0, 0)
					} else {
						badge(c, tr("Not Installed"), nil, p.muted, p.neutralSurface, 0, 0)
					}
				})
				switch {
				case cli.installed && cli.ours:
					label(c, tr("Use masir in your terminal; the app keeps this command up to date."), sizeCaption, 14.85, 400, p.muted)
				case cli.installed:
					label(c, tr("masir is already installed at ")+cli.path+tr("; Masir leaves it unchanged."), sizeCaption, 14.85, 400, p.muted)
				default:
					label(c, tr("Use masir in your terminal; the app sets it up automatically."), sizeCaption, 14.85, 400, p.muted)
				}
				if !cli.installed {
					ui.Column(c).Gap(tw(1)).Margin(4, 0, 0).Children(func() {
						if thinButton(c, "cli-install", tr("Install"), true, a.cliBusy, a.cliBusy, p) {
							a.installCommandLine()
						}
					})
				}
				if cli.needsShell {
					label(c, tr("Open a new terminal to use masir."), sizeCaption, 14, 400, p.degraded)
				} else if cli.installed && cli.ours && !cli.onPath {
					label(c, tr("Add ")+cli.dir+tr(" to your PATH to run it by name."), sizeCaption, 14, 400, p.degraded)
					mono(c, cli.path, sizeMicro, 13, 400, p.muted).MaxLines(2).Selectable()
				}
			})
			cardDivider(c, p)
			ui.Column(c).Padding(tw(2.5), tw(3)).Gap(tw(1.5)).Shrink(0).Children(func() {
				label(c, tr("Resets the proxy port, DNS, gateway, refresh interval and logging. Your servers, rules, power and privacy choice stay as they are."),
					sizeCaption, 14.85, 400, p.muted)
				ui.Column(c).Gap(tw(1)).Margin(4, 0, 0).Children(func() {
					if thinButton(c, "reset-settings", tr("Restore Defaults"), false, false, false, p) {
						a.restoreDefaults()
					}
				})
			})
		})
	})
}
