# Updates

Two signed channels update masir: the daemon and command line from the release's minisign
`checksums.txt`, and the desktop app from MyGo's ed25519 manifest. This is how they run,
what they download, and how a change stays compatible with copies already installed.

## Publishing to the fork

The desktop app and daemon check public releases in `nekl3103/masir-fork`. The fork
uses its own keys. Start from a fork build with those public keys embedded. A version
released as a tag must be newer than the installed fork build.

Configure the signing keys from `build/fork-signing/` under
**Settings → Secrets and variables → Actions** in the fork:

| Type | Name | Local file |
|---|---|---|
| Repository variable | `MASIR_UPDATE_PUBKEY` | `minisign.pub` |
| Repository secret | `MINISIGN_SECRET_KEY` | `minisign.key` |
| Repository secret | `MYGO_UPDATER_PRIVATE_KEY` | `mygo-update.key` |

Keep backups of the private keys. The desktop public key is in
`apps/masir-gui/mygo.json`.

Push the source to `main`, then push a version tag built from that commit. For a build
installed as `0.1.8`, the next release can use `v0.1.9`.

```sh
git push -u origin main
git tag v0.1.9
git push origin v0.1.9
```

The Release workflow publishes the signed assets to **Releases** after the builds
and checks pass. It includes the installer, updater archives, manifests, and
checksums. A push to `main` alone does not publish an update.

## What asks, and when

| Who | When | Costs |
|---|---|---|
| the daemon | a minute after it starts, then hourly while its cache is a day old | one GitHub request, none while the cache is fresh |
| the app | on a status, when the daemon's cache is a day old | one request, from the daemon's cache |
| the app | when the person opens the panel (macOS) or focuses the window, at most every 10 minutes | a conditional request: a 304 when nothing changed |
| the app | the signed app feed, once per release it hasn't answered for, and every 6 hours | the feed resolution |

`CheckUpdate.max_age_ms` is how a UI says how old an answer it will accept. The daemon
answers from its cache when it is younger, asks GitHub otherwise, and never trusts a
release more than a day old: a longer ask is capped. GitHub's `ETag` is kept with the
cached release, so a check that finds nothing new is a `304` with no body. That saves the
download, not the quota: an unauthenticated request counts even when it answers `304`
(measured, not assumed). Six an hour is a tenth of GitHub's 60-an-hour limit for an IP,
and the app feed is read from `github.com`, which is not the API and has no such limit.

A hidden prompt (`IgnoreUpdate`) is for one version. It stays hidden across checks — a UI
that asks every ten minutes must not bring it back — and clears when a newer release is
published, or when the daemon restarts.

## What an update downloads

Every download is `<app>-<role>-<os>-<version>-<arch>`.

| Download | Holds |
|---|---|
| `masir-cli-<os>-<v>-<arch>.tar.gz` | masir and both geo files: offline installation and combined updates |
| `masir-desktop-portable-<os>-<v>-<arch>.tar.gz` | the app, on macOS and Linux; also the archive its updater reads |
| `masir-desktop-update-windows-<v>-<arch>.tar.gz` | the app on Windows, where the portable download is a `.zip` |
| `masir-desktop-linux-<v>-<arch>.deb` | the Linux app for apt; also what a packaged app updates itself with |
| `masir-desktop-linux-<v>-<arch>.rpm` | the Linux app for dnf; also what a packaged app updates itself with |
| `masir-desktop-<from>-to-<v>-<os>-<arch>.delta` | the change from the previous app release |

The CLI updater downloads the full archive and installs its executable and both geo files
as one release. Windows CLI archives use `.zip`; other systems use `.tar.gz`. Thin
`masir-cli-update-…` archives are no longer published. Desktop deltas remain optional:
a missing or inapplicable delta falls back to the full desktop payload.

### Transition from binary-only updates

The previous updater already falls back to the full archive when the thin archive is
absent. Keep the full asset names, `checksums.txt`, its minisign signature and the signing
key unchanged. Existing clients can install this release without a reinstall, but their
old updater extracts only the executable: existing geo files stay unchanged during that
first update. Once the new daemon is running, later updates install the bundled geo pair
as well. There is no extra first-launch download. Very old clients must be checked against
their shipped naming convention before claiming compatibility.

## What an update installs

On macOS, Windows and writable Linux installs, **New Update** downloads and verifies
the desktop payload. **Install Update** then commits it and restarts the app as needed.
The separately downloaded daemon archive used by older services still follows its
existing update flow.

When both sides have the same newer release and the daemon advertises desktop import,
the patched GUI downloads only the desktop delta or full archive. MyGo verifies and
reconstructs the complete app, then invokes a preparation hook before app replacement.
The GUI streams the bundled executable and geo pair, plus `service-update.json` and its
minisign signature, to the daemon. These two small metadata files live inside the existing
desktop payload; they are not additional release assets.

Desktop bundles store geo data as `geoip.dat.gz` and `geosite.dat.gz`, with expanded
sizes and SHA-256 hashes in `geo.json`. The component signer hashes the verified
expanded bytes, and the GUI expands them while streaming to the service. The signed
descriptor and import protocol still name the original `.dat` files; existing
import-capable services need no protocol upgrade. Both gzip integrity and expanded
size/hash limits are checked before an import can complete. The service independently
checks its signed descriptor and every received byte as before.

The initial v0.1.0 release establishes shared desktop import and upgrades through the
separate CLI archive path. Keep compressed resources and their GUI decoding support
in the same release: an intermediate GUI with raw-only shared import cannot prepare
a later compressed desktop payload. This compatibility check matters when splitting
or backporting these changes.

Initial service installation expands and verifies both resources in private staging
before replacing the service executable. Installed services and standalone CLI archives
continue to use raw `.dat` files. Without a service, the bundled daemon materializes a
verified pair outside the application: in the user's config cache, a root-owned Unix
run directory, or a protected Administrators-owned Windows cache. A content-addressed
directory is published only when the entire pair is complete. Its original hashes
are retained to distinguish managed data from later manual replacements. The selected
cache is remembered across bundle versions: if either file differs, the pair remains
selected and is never regenerated over the changes. Unchanged data can advance to a
new bundled pair. As with the previous immutable bundle data, direct geo updates for this cache
ask the person to update the app. Service geo updates remain independent. No first-run
download is needed, and extraction never changes the signed app bundle.

The daemon verifies that descriptor with its own embedded minisign keys (never with the
development unsigned override). The signed role, version, OS, architecture, exact component
names, sizes and SHA-256 hashes authorize the transfer. A bounded local stream writes only
those three files into private staging (root-controlled on Unix; Administrators-owned
with protected System/Administrators-only ACLs under Program Files on Windows), and every
byte is checked before preflight or execution. The existing combined installer installs the executable and geo pair without
restarting. MyGo then commits the app; the GUI asks the service manager/daemon to restart,
checks the expected version and tunnel, reconciles the service definition and relaunches.
A failed service import prevents app commit; a failed app commit leaves the old daemon
process running and the prepared service version on disk. Retrying an identical service
payload preserves its earlier rollback backups. The copies remain protocol-compatible
through this interval; this is not a crash-atomic multi-file transaction.

Older daemons do not advertise import, so the first transition keeps the independent CLI
archive download followed by the desktop update. Stock/unpatched source builds and releases
whose desktop/daemon versions differ use that same fallback. Standalone CLI updates still
use the signed full CLI archive. With no service and a bundled daemon, the desktop update
continues to replace the bundle and restart its daemon as before.

Deltas remain useful: they reconstruct the same complete app with less network traffic.
A failed or unavailable delta falls back to the full desktop archive, so "one part" means
one shared desktop update, not a guarantee of exactly one HTTP request.

The app bundle has one writer (MyGo's updater). A daemon or CLI inside `*.app` refuses to
replace itself and says to update the app.

### Replacing the desktop app manually

Launching a newer signed desktop app compares its release with the installed daemon
and offers **Finish Update** in the footer and Settings update control. Opening the app
never installs or elevates. The installed service is identified from its reported
status, even when the app selects a bundled CLI because the legacy service path moved.

Clicking the action requests OS administrator permission and runs the bundled CLI's
`daemon import-desktop` helper directly. While the password dialog is open the control
shows **Enter Password…**; cancelling the dialog returns to **Finish Update** without an
error. The helper independently verifies the signed local
components, preflights the rules, preserves custom geo files and rollback backups, and
restarts the service through the supervisor. The GUI confirms the running version and
tunnel before reporting success. Other failures display **Retry** with the error in the
tooltip; status events do not retry them or fall back to GitHub.

Equal versions, newer services, development versions and bundled fallback daemons are
left alone. Missing local components produce a visible error on click, before any
administrator prompt. Unsigned or altered components fail verification in the helper;
there is no release-download fallback for this action.

The bundled helper works offline and with private repositories: it never asks GitHub for assets. The helper uses
its own embedded signing keys, checks the installed service version, independently
verifies the manifest and all component bytes in protected staging, and uses the existing
geo-preserving installer and rule preflight. It stops the old service through the OS
supervisor only after verification/preflight, rechecks saved rules after stopping, and
restarts the installed service on both success and failure. A process lock serializes
helpers; a crash releases it. A rejected signature, declined permission, or failed
preflight stays visible for explicit retry and does not trigger a download.
The elevated compatibility helper uses the new installer’s custom geo preservation policy
even when the installed service predates it.
On Windows, user-owned service directories are migrated into a fresh
Administrators-owned directory. Pre-0.1.0 unauthenticated user-owned executables are refused
and require reinstalling the service (`masir daemon enable`). Fixed-name geo files are preserved
through no-follow, single-link snapshots; old DLLs are not copied. The old tree is retained under
a random `.geo-*.legacy` sibling for administrator recovery, never recursively deleted by the
helper. An unsafe recovery target is refused rather than restarted.

On macOS the service installs under `/Library/PrivilegedHelperTools/masir`, whose parent is
root-controlled. Legacy `/usr/local/lib/masir` services outside root control are refused and
require reinstalling the service (`masir daemon enable`). The helper pins the service directory,
rejects links and non-regular/oversized resources, preserves custom geo data and service icons,
and publishes into the protected location after signature and rule checks. An app-managed CLI link
to the old service is repointed to the current bundled CLI.

The app-managed CLI is a link into the app on macOS/Linux and the app's bin directory on
the Windows user PATH, so replacing the app at the same location updates that command too.
Unrelated standalone or package-manager CLI installations are not overwritten. This
repository has no ownership record that would safely identify those as app-managed.

The bundled geo pair replaces the active pair only when the installed provenance still
matches. Custom or legacy data without provenance is preserved.
It is the release's tested baseline, not a promise of newer upstream data; `masir update
geo` still fetches the latest upstream pair separately. If the baseline cannot build an
enabled rule, the combined update fails before changing any active file. The previous
pair is retained as `.previous` alongside the active data, and a binary installation
failure restores it. Service rollback restores the saved binary and saved geo pair when
both data backups exist. CLI and geo jobs are serialized, including background refreshes.

## Linux packages (.deb/.rpm)

The `.deb` and `.rpm` install the app in `/opt/masir-desktop`, which root owns. MyGo's
updater turns itself off when it can't write the app's folder, so a packaged app never
reads the signed app feed. `/opt/masir-desktop` has one writer: the package manager.
The app updates it through the bundled CLI, and the daemon and CLI treat
`/opt/masir-desktop/bin/masir` as the app's own copy. They don't replace it themselves:
`masir update` there says to update the app.

At launch the app looks once for the package that owns its executable (`dpkg-query -S`,
then `rpm -qf`). A development build, or one whose version isn't a release, never enters
this flow. The version to install is the daemon's latest release, since the app feed
can't be asked.

**New Update** downloads and verifies the package. **Install Update** begins installation and requests administrator permission.

1. **Download, as the person.** The bundled CLI's `desktop fetch-package` downloads the
   package, `checksums-desktop.txt` and its `.minisig` into the app's cache
   (`~/.cache/<App>/updates/<version>`). It tries directly first. It then tries Masir's
   local proxy, but only when the proxy is already running: the update never starts it.
   A paused download resumes, and downloads of other versions are deleted. The CLI
   verifies the signed list and the package before it reports success, and refuses a
   signed package of another version or format than the one asked for.
2. **Install, as root.** On the second click, the app checks that the bundled CLI and every directory above
   it are owned by root and not writable by anyone else. It then runs that exact file
   through `pkexec masir desktop install-package`. No custom polkit policy is involved,
   so the person types their password every time. The root command trusts nothing the
   person can still write, and works in this order:
   - take `/var/lib/masir-desktop.update.lock` (`flock`), so a second install fails
     as a busy installer instead of deleting the first one's staging. apt, dpkg and
     the service import inherit the lock, so it holds until they end too;
   - copy the three files, as regular files opened with `O_NOFOLLOW` and capped in
     size, into a fresh 0700 directory under `/var/lib/masir-desktop.update-*`
     (left-over ones from a killed run are removed first);
   - verify that copy against the signed list, with the minisign keys built into the
     CLI. The version comes from the signed line, never from the file name;
   - read the package's own metadata (`dpkg-deb`/`rpm`): it must be `masir-desktop`
     at the signed version, and newer than the installed package. A package dpkg
     holds unpacked or half-configured may be finished at its own version, but never
     replaced by an older one;
   - install the copy with `apt-get install -y` (dpkg lock timeout 0, so a busy
     installer fails at once) or `dnf install -y`;
   - with a service installed and older than the package, run the *new* bundle's
     `daemon import-desktop` and `daemon sync-unit`. The service and its unit are
     updated with the same password. When the service binary is already current,
     synchronize its unit under the same authorization. If the process still needs
     a restart and synchronizing the unit did not restart it, use
     `systemctl try-restart`. A stopped service stays stopped.

Then the app deletes its download, follows the service restart and relaunches. Without a
service, the app restarts its bundled daemon as an app update does.

Exit codes the app reads from `masir desktop …` (`apps/masir/exit_code.go`):

| Code | Meaning |
|---|---|
| 10 | the package isn't the signed release |
| 11 | another installer holds the package manager's lock, or another Masir update is installing |
| 12 | the package manager refused the package |
| 13 | the app is installed, the service didn't take the new version |
| 14 | the package isn't newer than what is installed |
| 16 | the app is installed and the service unit still needs reconciliation |
| 20 | the release host couldn't be reached |

What the control shows:

| Case | Control | Tooltip |
|---|---|---|
| password dialog cancelled | **Install Update** again | — |
| account not allowed to elevate | Update failed | Your account isn't allowed to install software. |
| no polkit agent (or its text fallback can't start) | Update failed | Download the new version from the release page and verify its signed checksum before installing. |
| 10 | Update failed (the download is deleted) | The download didn't match the signed release; nothing was installed. |
| 11 | Update failed | Another installer is running. Try again in a minute. |
| 14 | Update failed | This version is already installed. Restart Masir. |
| 20 | Update failed | GitHub isn't reachable. Connect Masir and try again. |
| 12, other | Update failed | the last line of the package manager's error |
| 13 | the app relaunches | the new app offers **Finish Update** for an outdated daemon |
| 16 | the app relaunches | **Finish Update** retries the service unit even when the daemon is already current |

The new app stays installed if the service step fails.

A portable copy unpacked into a folder the person can't write is not a package. Its daemon
still updates, and the app tooltip says to download the new version from the release page.

Installs of 0.1.6 and older predate this flow and need one manual install of a newer
package:

```sh
sudo apt install ./masir-desktop-linux-<v>-<arch>.deb
sudo dnf install ./masir-desktop-linux-<v>-<arch>.rpm
```

The `.rpm` path is untested. dnf5 may refuse an unsigned local package without
`--nogpgcheck`, which the minisign check would make safe, and dnf4 may wait on its lock
instead of failing with code 11.

### Testing package updates locally

`MASIR_DESKTOP_RELEASE_BASE` points the CLI at another release host, and only a loopback
address is accepted. In the app, `MASIR_DESKTOP_RELEASE_VERSION` stands in for the daemon's
latest release, and only together with a loopback base.
`scripts/build-test-packages.sh <out> <version>…` builds `.deb` packages signed with a
throwaway key (in `<out>/test-key.env`), laid out like a release. It calls
`scripts/build-gui.sh` rather than `make gui-local`, because the Makefile includes `.env`,
whose release keys would replace the test key. Serve `<out>` with
`python3 -m http.server 8799 --bind 127.0.0.1`, install the lower version with apt, and
launch it with both variables. Restore a real release afterwards: the test packages trust
only the throwaway key.

## Compatibility rules

A release must work with the copies already installed, in both directions:

- **Protocol**: additive fields only, never reuse a field number, and a new enum value
  only reachable when a client asks for it. `buf breaking` in `make proto-lint` enforces
  the field rules.
- **Hints, not requirements**: `max_age_ms` is a hint. A daemon that predates it ignores
  the field and answers from its daily cache, which is what the app did before the hint
  existed. `defer_restart` is only ever set by a GUI that handles a daemon which restarts
  anyway (an older daemon ignores the field and restarts at the end of the job).
- **Asset names have fallbacks**: a client asks for the exact name, then for the older
  name it knows. Keep the full CLI names and desktop manifest URLs stable. Thin CLI assets may be
  retired because the previous updater falls back to the full archive.
- **Every asset is signed**: new downloads join `checksums.txt` (minisign) or
  `checksums-desktop.txt` (minisign). Deltas are covered by both the signed manifest and
  the desktop list (they are named `masir-desktop-…`). The app's manifest is signed with
  the MyGo ed25519 key. A build without its key refuses updates rather than installing an
  unverified one.
- **One version number**: `mygo.json` and the release tag are the same number, so the app
  can key its feed check on the release the daemon reports. The 6-hour fallback keeps that
  from being load-bearing if the two ever diverge.

## Verifying a release by hand

```sh
minisign -V -P <MASIR_UPDATE_PUBKEY> -m checksums.txt -x checksums.txt.minisig
shasum -a 256 -c checksums.txt

minisign -V -P <MASIR_UPDATE_PUBKEY> -m checksums-desktop.txt -x checksums-desktop.txt.minisig
shasum -a 256 -c checksums-desktop.txt
```

## Changing this

- A new artifact: add it to the release, to a signed checksum list, and to the name table
  above; give it a role (`portable`, `update`, …) rather than a bare name.
- A new behaviour the GUI asks for: a new field, plus the behaviour without it. Test the
  old-daemon path with the fakes in `apps/masir-gui/updates_test.go`.
- A new signing key: `publicKeys` in the daemon accepts a comma-separated list, so add the
  new key beside the old one for a release before removing the old one.

## Preserving manually replaced geo data

`installed-geo.json` records SHA-256 hashes and sizes of the last complete pair Masir
installed. Service installs, verified geo refreshes and CLI release archives establish
this baseline. App/service updates compare the active pair with that prior baseline,
not with incoming release hashes. If either file differs, both files are preserved.
Existing files without provenance are conservatively preserved; missing or invalid
custom data causes validation to fail rather than being silently repaired.

The updater snapshots preserved data into private staging and preflights the new
executable against that snapshot. It rechecks that the active pair did not change
during validation before replacing the executable. Current files stay untouched;
rollback backups include the preserved pair and its prior provenance. Metadata advances
only when Masir actually installs a managed pair. Background geo refreshes skip custom
or untracked data. An explicit geo-refresh command remains a deliberate replacement
and establishes a fresh managed baseline after success.

Bundled fallback caches likewise retain manual changes on restart and across app
updates. Legacy caches are detected conservatively if no active-cache pointer exists;
ambiguous modified caches fail rather than guessing. No custom-folder setting or
protocol migration is introduced.

This policy requires the updater executing the installation to contain this change.
Already-shipped older updaters cannot retroactively apply it on their first upgrade;
keep an independent backup of manual geo replacements for that transition. Their
existing `.previous` geo backups may also allow recovery, but are not a substitute for
an independent backup.
