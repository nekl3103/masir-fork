<div align="center">

<h1>
  <img src="apps/masir-gui/resources/icon.png" width="50px" alt="Masir logo">
  <br>
  Masir Fork
</h1>

<p>
  <a href="https://github.com/nekl3103/masir-fork/releases"><img src="https://img.shields.io/github/v/release/nekl3103/masir-fork?style=flat&amp;color=00b884" alt="Fork releases"></a>
  <a href="https://github.com/nekl3103/masir-fork/actions/workflows/ci.yml"><img src="https://github.com/nekl3103/masir-fork/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="#building-from-source"><img src="https://img.shields.io/badge/Go-1.27%2B-00ADD8?style=flat&amp;logo=go&amp;logoColor=white" alt="Go 1.27 or newer"></a>
  <a href="#platform-support"><img src="https://img.shields.io/badge/macOS_%C2%B7_Linux_%C2%B7_Windows-30363d?style=flat" alt="macOS, Linux and Windows"></a>
</p>

<p><strong>The Xray client that routes the way you want.</strong></p>

<p>
  A native desktop app and CLI for managing Xray connections and routing.<br>
  Route by app or site. Switch servers and change rules without restarting the core.
</p>

<p>
  <a href="https://github.com/nekl3103/masir-fork/releases"><strong>Download Masir Fork</strong></a>
  &nbsp;·&nbsp; <a href="#whats-new-in-the-fork">Fork improvements</a>
  &nbsp;·&nbsp; <a href="#quick-start">Quick start</a>
  &nbsp;·&nbsp; <a href="#routing-rules">Routing rules</a>
  &nbsp;·&nbsp; <a href="#building-from-source">Build from source</a>
</p>

<p>
  <img src="docs/assets/banner.png" width="100%" alt="Masir's native desktop app with its server picker open, alongside demo CLI status output">
</p>

</div>

Masir Fork is maintained by Alexander ([@nekl3103](https://github.com/nekl3103)). The original Masir was created by [Gozarman](https://github.com/gozarman) and developed with the contributors to [Gozargah/Masir](https://github.com/Gozargah/Masir).

## What's new in the fork

- **More VPN protocols.** Import AmneziaWG profiles, including Amnezia share links, and use them with Masir's Xray routing rules. The fork also accepts standalone `tuic://` links and Xray JSON configurations with MASQUE outbounds.
- **Multiple sources per subscription.** Group several links under one subscription. Masir merges duplicate servers and keeps the last successful server list for each source if its refresh fails.
- **Domain route diagnostics.** Use **Check Domain Route** from the rules panel to see the matched rule and outbound. The check also tests HTTPS access and reports the public IP through that outbound.
- **Kill Switch.** Enable it under **Settings → Advanced** to block traffic assigned to the VPN when its route is unavailable. Explicit direct rules apply while Xray is routing traffic. In TUN mode on macOS, Windows and Linux, a system firewall also guards against traffic bypassing the tunnel if the daemon stops. The Linux firewall requires `nftables`.
- **Russian and English desktop interface.** Switch languages under **Settings → Language**. The app remembers your choice. The Masir and AmneziaWG versions are shown at the bottom of Settings, above **Quit Masir**.
- **Hysteria 2 import compatibility.** Provider exports can include server-only masquerade settings. Masir removes those settings from imported client outbounds, including members of a balanced profile.
- **Fork-specific updates.** The app and daemon use this fork's release feed. Desktop updates can install the matching signed service components bundled with the app.

## Why Masir

- **Route by app.** One rule sends a whole app through the proxy or keeps it direct, helper processes included. Pick it from a list of your installed apps.

- **Full Xray routing.** Every rule is a real Xray routing rule: domains (+geosite), IPs (+geoip), ports, protocols, processes, or raw JSON. Xray checks each rule before it's saved.

- **Live changes, no restarts.** Rules, server switches and pins are applied to the running core through Xray's own gRPC API. Xray keeps running while you tweak, so nothing else drops.

- **Pin rules to any server.** Send one service through one server, from any subscription, whatever the active server is. If that server disappears, the rule falls back instead of breaking.

- **Subscriptions on autopilot.** JSON configs and share links (`vless://`, `vmess://`, `trojan://`, `ss://`, `hysteria2://`, ...), refreshed on schedule, with traffic and expiry shown, fetched through the proxy when the provider is blocked.

- **Live latency.** The server picker tests what's on screen as you scroll. `l` retests one, `a` retests all, or let `masir use --fastest` pick.

- **TUN or proxy, your choice.** TUN routes the whole system without touching your DNS settings (app-based routings work in TUN mode only); proxy mode gives you Mixed protocol (SOCKS5 + HTTP) on a port, which you can share over your LAN if you choose to.

- **Keeps itself current.** Install Masir and its bundled geo data together with `masir update`; use `masir update geo` to refresh only the geo data from upstream.

- **Scriptable, and built to be built on.** Plain output when piped, `--json` where it matters, smart shell completions, and a gRPC API for your own tools or a GUI.

## Philosophy

V2Ray/Xray routing is a powerful feature that can send each connection exactly where it should go: this app through the proxy, that site direct, ads nowhere, one service through the one server that works for it. Most Xray clients barely expose any of that. They start Xray system-wide, push all traffic through the tunnel, and hide routing behind a screen that's hard to find and harder to configure. Doing anything beyond the defaults means hand-editing JSON, and switching servers often restarts the core and drops every connection.

Masir started from the opposite end. Routing comes first: rules are easy to write and easy to read, and nothing about them is watered down, so anything Xray can match, Masir can route. The core stays up while you change things, because Xray's own API can apply changes live. Servers from several subscriptions sit side by side, and any rule can point at any of them. And the tool should feel good to use every day: fast to pick a server, clear about what's running, quiet when everything works.

## Install

### 1. GUI App (Recommended)

The easiest way to use Masir is the desktop app. It lives in your menu bar / system tray, sets up the environment automatically, and installs the `masir` CLI tool into your `PATH` on first launch (`~/.local/bin/masir` on macOS/Linux, or user `PATH` on Windows).

Download the desktop installer for your system from the **[Masir Fork releases](https://github.com/nekl3103/masir-fork/releases)**.


---

### 2. Standalone CLI (Without GUI)

If you are on a headless server or prefer using the command line only:

1. Download the CLI archive for your system from the **[Masir Fork releases](https://github.com/nekl3103/masir-fork/releases)** (`masir-cli-<os>-<version>-<arch>.tar.gz` or `.zip`). Each archive contains the `masir` executable along with `geosite.dat` and `geoip.dat`.

2. Extract the archive and place `masir` on your `PATH`:

   ```sh
   mkdir -p ~/.local/masir ~/.local/bin
   tar -xzf masir-cli-darwin-<version>-arm64.tar.gz -C ~/.local/masir
   ln -s ~/.local/masir/masir ~/.local/bin/masir
   ```

   Make sure `~/.local/bin` is in your `PATH` (e.g. `export PATH="$HOME/.local/bin:$PATH"` in your `~/.zshrc` or `~/.bashrc`).

   On Windows, extract the zip into a folder such as `%LOCALAPPDATA%\masir` and add that folder to your `PATH`.

---

### macOS Quarantine

Because release builds are not yet notarized with an Apple Developer ID, macOS Gatekeeper may flag them on first run. Clear the quarantine attribute once:

```sh
xattr -rd com.apple.quarantine ~/.local/bin/masir
xattr -rd com.apple.quarantine /Applications/Masir.app
```

---

### Shell Completions (Optional)

Enable command-line auto-completion for your shell:

```sh
# zsh
echo 'source <(masir completion zsh)' >> ~/.zshrc
source ~/.zshrc

# bash
echo 'source <(masir completion bash)' >> ~/.bashrc
source ~/.bashrc

# fish
mkdir -p ~/.config/fish/completions
masir completion fish > ~/.config/fish/completions/masir.fish
```

PowerShell:

```powershell
if (!(Test-Path -Path $PROFILE)) { New-Item -ItemType File -Path $PROFILE -Force }
Add-Content $PROFILE 'masir completion powershell | Out-String | Invoke-Expression'
```

## Quick start

```sh
masir sub add https://example.com/your-subscription    # add a subscription
masir use                                              # pick a server, with live latency
masir connect                                          # turn masir on (TUN mode)
masir status                                           # see what's running
```

`masir connect` uses TUN mode, which needs root (Administrator rights on Windows). If its daemon isn't running with those rights, Masir starts it that way for the session: sudo asks for your password on macOS and Linux (unless it remembers you), Windows shows its permission prompt. To never be asked again, run `masir daemon enable` once: it installs the daemon as a system service that starts at boot. Prefer not to? `masir connect --proxy` runs proxy mode only, as your normal user.

On macOS and Linux, run ordinary commands as your normal user, without `sudo`; Masir
asks for permission when needed. If a service is installed but stopped, start it with
`masir daemon start`. The CLI reuses the service rather than starting a separate daemon.

Then make it yours:

```sh
masir rules add        # the guided rule wizard
masir mode direct      # (optional) send unmatched traffic direct
```

Run `masir` with no arguments for the full command list, or `masir menu` for an interactive menu of everything.

## Routing rules

Rules are checked from top to bottom. The first match wins. Unmatched traffic follows the **routing mode**, either `proxy` (the default) or `direct`. Use `masir mode` to switch modes. In proxy mode with no server selected, unmatched internet traffic goes direct when Kill Switch is off and is blocked when it is on. Built-in rules handle local network traffic and DNS.

Each rule sends its traffic to one of:

- `proxy`: the active server
- `direct`: straight out, skipping the proxy
- `block`: dropped
- a specific server, from any subscription (`--via <server id>`)
- a custom Xray outbound (`outbound:<tag>`)

```sh
# open a wizard to define a rule step by step (Recommended)
masir rules add

# Iranian websites and addresses go direct
masir rules add --domain geosite:category-ir --ip geoip:ir -o direct

# Telegram, with all its helper processes, through the proxy
masir rules add --app /Applications/Telegram.app/ -o proxy

# Google always through one specific server, whatever the active one is
masir rules add --domain geosite:google --via <server-id>

# Block ads and trackers
masir rules add --domain geosite:category-ads-all -o block

# SSH always direct
masir rules add --port 22 -o direct

# Anything Xray understands, as raw rule JSON
masir rules add --name lab --json '{"source":["192.168.1.0/24"],"network":"udp"}' -o direct
```

Matchers combine within a rule (all must match), and each accepts a comma-separated list:

| Flag | Matches | Examples |
|---|---|---|
| `--domain` | websites | `example.com`, `geosite:google`, `domain:x.com`, `keyword:cdn`, `regexp:...` |
| `--ip` | destination addresses | `1.2.3.4`, `10.0.0.0/8`, `geoip:ir`, `geoip:private` |
| `--port` | destination ports | `443`, `1000-2000` |
| `--network` | transport | `tcp`, `udp` |
| `--protocol` | sniffed protocol | `http`, `tls`, `quic`, `bittorrent` |
| `--process`, `--app` | the app sending the traffic | `Telegram`, `/Applications/Spotify.app/` (a path ending in `/` matches everything inside) |
| `--source` | the sending device, when sharing over LAN | `192.168.1.20` |
| `--source-port` | the sending port | `5000-6000` |

Manage rules with `masir rules` (list), `masir rules edit <name>` (an interactive editor, or flags such as `--rename`, `--append`, `--unset`), `masir rules rm`, and `masir rules test <domain|ip>` to see exactly where a destination would go right now. `masir rules disable <name>` and `masir rules enable <name>` turn a rule off and on without removing it.

Geo data comes from [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat), refreshed daily upstream: `geosite:` is the v2fly domain list with hundreds of thousands of extra domains (a far larger ad and tracker list, for one), and `geoip:` covers every country by its two-letter code (`ir`, `de`, `us`, ...) plus lists such as `private`, `google`, `telegram`, `netflix`, `twitter` and `cloudflare`.

The rule wizard's website and address pickers include a country shortlist (Iran, Russia, China, Turkmenistan, Pakistan, and other restricted networks) and search every country in the installed data. Type a country name, adjective or code — `russia`, `russian`, `ru`, `turkmenistan`, `uae` — and the flags follow.

## Settings

```sh
masir settings                       # interactive
masir settings set mixedPort 7891
masir settings reset mixedPort
```

| Key | Default | What it does |
|---|---|---|
| `routingMode` | `proxy` | Where traffic no rule matches goes: `proxy` or `direct` |
| `mixedPort` | `7890` | The local proxy port, SOCKS5 and HTTP on one port |
| `allowLan` | `off` | Let other devices on your network use the proxy |
| `alwaysOnProxy` | `off` | Keep the proxy port up even while masir is off |
| `dohDns` | `https://1.1.1.1/dns-query` | DNS over HTTPS, reached through the proxy |
| `dnsServers` | `1.1.1.1, 8.8.8.8` | Plain DNS, sent direct, used to resolve proxy server names |
| `tunDns` | empty | DNS servers the TUN interface advertises; empty uses `dnsServers` |
| `gateway` | `10.10.20.1/24` | IPv4 range for the TUN interface |
| `subInterval` | `720` | Minutes between background subscription refreshes (5 to 43200) |
| `logLevel` | `warning` | Xray's own log level: `debug`, `info`, `warning`, `error`, or `none`; `masir logs` shows these |
| `analytics` | `on` | Send anonymous usage and error events to PostHog, once the first-run notice has been shown |

**The proxy has no authentication.** The mixed inbound (SOCKS5 + HTTP) on `mixedPort` is `noauth`: anyone who can reach the port can use your proxy. By default it listens on `127.0.0.1` only. With `allowLan on` it listens on every interface, so every device on your network can use it; keep it off on networks you don't trust.


## Commands

| Command | What it does |
|---|---|
| `masir connect [--proxy]` | Turn masir on: TUN mode, or proxy mode only with `--proxy` |
| `masir disconnect` | Turn masir off; the daemon keeps running |
| `masir status [--no-check] [--json]` | Daemon, Xray and live connectivity |
| `masir use [id] [--fastest]` | Switch the active server |
| `masir mode [proxy\|direct]` | Where traffic no rule matches goes |
| `masir sub` | List subscriptions with traffic and expiry; `add`, `update`, `rm`, `list --nodes` |
| `masir rules` | List rules; `add`, `edit`, `rm`, `test`, `check`, `enable`, `disable` |
| `masir settings` | An interactive settings menu; `list`, `get`, `set`, `reset` |
| `masir update [--check]` | Install the latest masir release |
| `masir update geo [--disable-broken-rules]` | Refresh geosite.dat and geoip.dat, unless that would break a rule |
| `masir update pause\|resume\|stop\|status` | Control an update in progress |
| `masir logs [--no-follow] [--level debug\|info\|warn\|error]` | Show recent daemon and Xray activity, then follow it |
| `masir get-config` | Print the exact Xray config masir runs |
| `masir daemon start\|stop\|restart\|enable\|disable` | Manage the daemon and the system service |
| `masir daemon rollback` | Put the service's previous version back after an update that didn't start |
| `masir menu` | Every command, one pick away |

Every command has `--help` with examples.

## Right-to-left text

Masir automatically shapes and reorders Persian/Arabic text on terminals that don’t
do bidi themselves. On Windows it leaves the text to the terminal by default, because
the current Windows terminal handles the shaping and ordering itself.

Override the guess with `MASIR_BIDI`:

```powershell
# powershell
$env:MASIR_BIDI = "on"    # force masir's shaping/reordering
$env:MASIR_BIDI = "off"   # leave the text to the terminal
```

```sh
# unix
MASIR_BIDI=on masir use
MASIR_BIDI=off masir use
```

Use `on` if your Windows terminal shows reversed or disconnected Persian text. Use
`off` if your terminal already renders RTL correctly and Masir’s extra shaping is
reversing it.

## Privacy

Masir collects anonymous usage statistics and error reports to help improve it. Nothing is collected before a one-time notice on your first run. To turn it off, run `masir settings set analytics off` (or set `DO_NOT_TRACK=1`). The details are in [docs/telemetry.md](docs/telemetry.md).

## Scripting and the API

Output is plain text whenever it isn't going to a terminal, and status and rules speak JSON:

```sh
masir status --json | jq '.connectivity.proxy'
masir rules --json
masir use --fastest && masir connect --proxy
```

The daemon's gRPC API (`masir.daemon.v1.DaemonService`, defined in [`libs/masir-proto`](libs/masir-proto/proto/masir/daemon/v1/daemon.proto)) listens on a Unix socket (a named pipe on Windows), with server reflection enabled, so tools like [grpcurl](https://github.com/fullstorydev/grpcurl) work out of the box. Where the socket is depends on how the daemon runs, and neither needs root to connect:

- A daemon running as you (proxy mode, or before you've asked for TUN) listens on `daemon.sock` in the config folder.
- The root daemon (the system service, or a TUN session) listens on `/var/run/masir/<your uid>.sock`.

Only you and root may connect; the kernel checks who is on the other end.

```sh
SOCK="unix:///var/run/masir/$(id -u).sock"                              # root daemon
SOCK="unix://$HOME/Library/Application Support/masir/daemon.sock"      # daemon running as you (macOS)
grpcurl -plaintext "$SOCK" list
grpcurl -plaintext "$SOCK" masir.daemon.v1.DaemonService/GetStatus
```

The API covers everything the CLI does and more: custom inbounds and outbounds, live
status streaming, log streaming, per-server latency tests, cumulative per-server traffic
totals retained across Xray restarts within a daemon session,
and update jobs with progress, pause, resume and stop. Status includes a revision counter
for caching state; see the protobuf comments for its contract and the traffic counters
that can change independently.

## Security

Masir's daemon can run as root (for TUN), so it is built not to be a way for other software on your machine to become root. [SECURITY.md](SECURITY.md) explains what it protects, what it doesn't, and how to report a problem.

## Platform support

| | macOS | Linux | Windows |
|---|---|---|---|
| Proxy mode | yes | yes | yes |
| TUN mode | yes | yes | yes |
| System service | launchd | systemd | Windows service |
| Updates | yes | yes | yes |

The initial Android app supports Android 8.0 or later on ARM64 and x86-64. It includes profile and subscription import, VPN mode, direct domain rules, and English and Russian. Enable **Always-on VPN** and **Block connections without VPN** in Android's VPN settings for system-level blocking when the service stops.

On Windows, TUN runs on the [Wintun](https://www.wintun.net) driver, built into `masir.exe` and written beside the service's copy when it starts, and Masir keeps DNS queries inside the tunnel with Xray's Windows Filtering Platform rules, so lookups can't leak through other network adapters. Windows TUN support is new: if something doesn't work on your setup, please open an issue with the output of `masir logs`.

## Building from source

You need Go 1.27 or newer.

```sh
git clone https://github.com/nekl3103/masir-fork.git
cd masir-fork
make build                  # dist/masir
make gui-local              # desktop app for this machine in apps/masir-gui/dist/
make android                # signed APK in dist/android/
```

For the Android build, set `ANDROID_HOME` and use JDK 17, Android SDK Platform 35, Build Tools 35.0.0, and NDK 28.2.13676358. Local builds use a test signing key by default.

For geo rules, put `geosite.dat` and `geoip.dat` next to the binary, or let `masir update geo` fetch them.

## License

Masir's source code is licensed under the [GNU Affero General Public License v3.0](LICENSE)
(SPDX: `AGPL-3.0-only`).

Third-party components and assets retain their respective licenses, including the
[flag icons](apps/masir-gui/resources/flags.LICENSE.txt) and
[Wintun](libs/masir-daemon/internal/wintun/LICENSE.txt).
