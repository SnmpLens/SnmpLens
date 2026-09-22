# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

SnmpLens is a cross-platform SNMP MIB browser / network-management desktop app built with **Wails v2**: a Go backend and a Svelte 5 + Vite frontend compiled into a single native binary. The Go module is `SnmpLens` (see `go.mod`); the frontend lives entirely under `frontend/`.

## Commands

```bash
wails dev                 # hot-reload dev server (regenerates Go→JS bindings, then runs vite + Go)
wails build               # production binary → build/bin/
wails build -platform linux/amd64 -tags webkit2_41   # Linux REQUIRES the webkit2_41 build tag
wails build -platform windows/amd64
wails build -platform darwin/universal
```

Frontend-only scripts (run from `frontend/`): `npm run dev`, `npm run build`. You normally don't invoke these directly — `wails dev`/`wails build` drive them via `wails.json`.

`go run ./tools/cataloguecheck <directory>` validates a catalogue of contributed simulator models and
dashboard presets — the shape [SnmpLens/snmplens-community](https://github.com/SnmpLens/snmplens-community)
has — by BUILDING each one through `pkg/simulator` and `pkg/preset` rather than reimplementing their rules: a
checker that drifts accepts what the import then refuses. That repository's workflow checks out both and runs
it on every pull request.

The Go toolchain is pinned by the `toolchain` directive in `go.mod`, and every workflow reads it with
`go-version-file` rather than naming a version — one source of truth. It matters: Go supports only the two most
recent majors, so a project sitting on an older one stops receiving security fixes without anything failing.

Security workflows: `codeql.yml` (SAST for Go and the frontend, weekly as well as per-push, because a query added
after a commit lands would otherwise never see it), `dependency-review` on pull requests (govulncheck reports on
what is already merged; this blocks it before), `govulncheck`, and `npm audit --audit-level=high` — which used to
end in `|| true` and therefore never failed. Actions are pinned by commit SHA, with Dependabot configured for
gomod, npm and github-actions, because pinning without something to move the pins just freezes them.

CI verification gate (`.github/workflows/ci.yml`, all run from repo root):

```bash
go vet -tags webkit2_41 ./...
go test -race -count=1 -tags webkit2_41 ./...       # on ALL THREE platforms, not just Linux
staticcheck -tags webkit2_41 ./...                  # PINNED: honnef.co/go/tools/cmd/staticcheck@v0.8.1
go mod tidy && git diff --exit-code go.mod go.sum   # go.mod must stay tidy
go build -tags webkit2_41 ./...                     # the tripwire, see below
govulncheck -tags webkit2_41 ./...
```

Three of those lines are load-bearing in a way the obvious version is not.

**The tests run on every platform we ship.** They used to run on Linux alone "to avoid redundant runs", and they
are not redundant: `pkg/autostart` is an HKCU Run key, a LaunchAgent and an XDG entry; `pkg/secrets` is DPAPI,
the Keychain and a file backend; `pkg/network` runs `traceroute6` on macOS and `tracert` on Windows. Three
implementations behind one interface, one of them ever exercised. The trap-listener race reproduced on Linux and
never on Windows; the converse is as likely and would have been as invisible.

**`go build` before `govulncheck` is a tripwire, not ceremony.** govulncheck's analysis is only as complete as
the packages it manages to LOAD, and it does not fail when one does not build — it reports on the rest and
prints a clean bill of health. The security job had neither the `frontend/dist` placeholder nor the GTK/WebKit
headers, so on Linux the main package dropped out of every scan it ever ran. Reproduced by deleting
`frontend/dist`: `go build ./...` fails on the embed while `govulncheck ./...` answers "No vulnerabilities
found" and exits 0.

**staticcheck is pinned and govulncheck is not**, deliberately and in opposite directions. A linter that floats
fails a commit that changed nothing when a new check lands; a vulnerability scanner that is pinned reports an
old world. Dependabot does not see versions inside a `run:` step, so staticcheck is bumped by hand.

Go unit tests live beside the code they cover (`go test ./...`, run in CI). They are deliberately few and target
the logic that is subtle and easy to break silently — storage pragmas/migrations, aggregation, threshold
semantics, the poll clock, counter-wrap maths — not coverage for its own sake.

One of them guards prose rather than logic. This repository states gosnmp's behaviour — the serial receive
loop, a USM table that validates nothing, a `SafeString` that prints the community — and cites the version each
was read in, which is what lets the next reader check it. Two bumps carried gosnmp from v1.43.2 to v1.45.0 with
every citation left naming the old one and nothing failing anywhere, since a bump touches `go.mod` alone. A
cited observation that has quietly become a guess reads as evidence, so `pkg/snmp/citedversion_test.go` pins
every `gosnmp vX.Y.Z` in the tree to what `go.mod` requires: an upgrade fails there, which is the moment to
re-read the cited source and confirm the claim before moving the number.

The frontend tests are `cd frontend && npm test`. Three of them check a contract that crosses a language
boundary and has no other symptom: `presetkeys.test.mjs` (every `errf` message and widget kind `pkg/preset` emits
has an `en.json` key, with the placeholders the `Args` map supplies), `dashboard.test.mjs` (the widget dispatch
names every kind Go serves — one it does not renders as nothing at all), and `series.test.mjs` (one plan decides
every chart colour; there were four rules and they disagreed whenever OIDs had uneven target counts). The first runs the real `pollingStore` against a stubbed Wails
bridge under node — it exists because a `ReferenceError` on that path once disabled all monitoring silently (the
outer `try/catch` swallowed it) and neither the Vite build nor `go vet` can see that class of bug. The second
checks that the five locale files carry the same keys and the same placeholders; svelte-i18n falls back silently,
so a locale can drift for months and only be noticed by whoever reads that language.

Two of them are static checks over the components, and both exist because SVELTE 5 REMOVED THE WARNING that used
to do the job. `compile.test.mjs` fails on a name nothing declares: Svelte 3 reported that as
`missing-declaration`, Svelte 5 reports nothing under any compile option, and the reference survives as a bare
global read that throws the first time its branch is evaluated — so the check now looks for the free identifier
in the code the compiler emits. `reactive.test.mjs` fails on a rendered expression that depends on state it does
not name: Svelte 3 recomputed every `{@const}` and block expression on any update, so a function reaching for
instance state was re-run often enough to look correct, while Svelte 5 tracks what the EXPRESSION reads and a
read inside a callee is not one. Eight of those were in the tree at migration time — the visible one rendered
`ifTable` with a column headed "Index" holding the raw instance, which is precisely what decoding an INDEX
exists to replace; the rest simply stopped updating, including the enum formatter that decides between `6` and
`ethernetCsmacd(6)`. The rule the test enforces: **a function called from the markup takes what it needs as
arguments.** Two exemptions are deliberate: event handlers, because reading instance state is what a handler
does and reporting them takes the count from 8 to 82; and `const`, which cannot be reassigned. `export let` is
NOT exempt — props are the state most likely to arrive after the first render, and forgetting that
`ExportNamedDeclaration` wraps the declaration hid half of them.

The integration tests talk to a real agent — the simulator, run in-process by `pkg/simulator/simtest` — so they
run on every build and every platform. They used to need the Python agent and `SNMPLENS_TEST_AGENT`, and in
practice ran nowhere. `pkg/monitor`'s poll a simulated server, and a 32-bit counter that wraps every two seconds
to prove the delta goes through the wrap; `pkg/snmp`'s run every operation and a discovery scan against simulated
devices. `internal/app/integration_test.go` follows one thing through several features at once: a model identified, a
preset bound and polled with every OID answered; a band crossed, journalled and delivered to a webhook; a device
going down and coming back; a switch's notifications in every version, journalled as coming from the switch and
routed; a request SnmpLens sends with the wrong community coming back as the device's authenticationFailure; a v3
session outliving the device's restart. What a feature does on its own is tested beside it — these are for the
seams between features, which no package owns.

Beyond that, correctness is verified by the build + `go vet` + `staticcheck` passing, and by manual testing
against the application's own simulated devices — the **Simulator** in the header, see **The simulator**: a
Linux server on `127.0.0.2` answering v2c `public` is one click, **Add as target** the next, and its Data tab
previews every OID it answers. The Python agent that used to be `tools/snmp_test_agent.py` is gone: it served
five interfaces where one simulated Catalyst serves thousands of objects that agree with each other, and since the
integration tests moved to `simtest` nothing ran it.

## The Wails bridge (most important architectural fact)

The frontend calls Go through auto-generated bindings in `frontend/wailsjs/`, which is **gitignored and regenerated by `wails dev`/`wails build`**. Consequences:

- Adding a Go method changes the generated module's EXPORTS, which hot module replacement cannot patch: a page
  that already imported it fails with "does not provide an export named X", which reads like a missing method
  rather than a stale tab. `vite.config.js` forces a full reload when anything under `wailsjs/` changes.
- The public API surface is the set of exported methods on the `App` struct in `internal/app/` — EVERY exported
  method, which is why the lifecycle (`startup`, `shutdown`, the close decision) is unexported and `app.Run` is a
  function rather than a method: a method would be callable from the renderer. Each becomes a callable JS
  function (e.g. `import { SnmpGet } from '../wailsjs/go/app/App'`); the directory is named after the Go package.
- `main.go` holds only what the binary embeds — `frontend/dist`, `mibs/`, `presets/`, the tray artwork — because
  `//go:embed` cannot reach above the directory of the file it sits in, and hands it to `app.Run` as `fs.FS`. Tests
  in `internal/app` read the same directories from the tree (`repo_test.go`: `repoRoot`, `mibs`, `presets`).
- After changing any `App` method signature or any Go struct that crosses the bridge, you **must** re-run `wails dev`/`wails build` to regenerate bindings before the frontend can use them. A fresh checkout has no `wailsjs/` until the first build.
- Request params are passed as structs defined in `pkg/snmp/params.go` (`SnmpRequest`, `SetRequest`, `GetBulkRequest`, etc.). The frontend constructs matching plain objects in
  `frontend/src/utils/snmpParams.js`, and the JSON field names are the whole of the contract.
  `frontend/tests/snmpparams.test.mjs` compares the two, per struct and with embedding resolved,
  in BOTH directions — because both failures are silent. A key Go does not declare is discarded by
  `encoding/json`, so a renamed `retries` becomes zero retries; a field Go declares that the renderer
  never sets arrives as the ZERO VALUE, so a forgotten `port` is port 0 and gosnmp fails somewhere
  far from the cause. Neither shows up in a build or a lint. `V3Params` is the one whose tags are
  CAPITALISED (`json:"User"`), matching the renderer exactly rather than relying on
  `encoding/json`'s case-insensitive fallback; the test pins that too.
- **Numeric serialization gotcha:** `formatSnmpValue` in `pkg/snmp/client.go` converts `*big.Int` to `int64`/`uint64` before returning. This is deliberate — raw `*big.Int` serializes as an object across the bridge and breaks frontend numeric parsing / chart rendering. Preserve this when touching SNMP value handling.

## Backend layout (`pkg/`)

- `pkg/mib/service.go` — MIB loading/parsing and tree construction via **gosmi**. `LoadAll`/`LoadSpecific`/`LoadWithDiagnostics` (the diagnostics variant returns per-file load errors for the drag-&-drop import UI). Also OID translation/resolution (`Translate`, `ResolveOid(s)`).
- `pkg/snmp/` — SNMP over **gosnmp**: `client.go` (connection config, v3 security-protocol mapping, concurrent fan-out via `concurrentExecute`, debug
  ring-buffer logger — scrubbed at the WRITER, because gosnmp's `SnmpPacket.SafeString` is not safe: it prints
  `Community:%s` on every SENDING PACKET and `Parsed community %s` on every receive, and the buffer is what the
  debug panel shows), `operations.go` (GET/SET/GETNEXT/GETBULK/WALK), `trap.go` (listener + sender, traps and acknowledged INFORMs — v1 is refused rather than downgraded, since RFC 1157 has no InformRequest PDU; the listener's engine ID comes from outside, `SetTrapEngineID`, because it cannot be discovered — see **The simulator**), `discovery.go` (CIDR scan, capped at `MaxDiscoveryHosts` — the prefix sizes the allocation before a packet is
  sent, so `10.0.0.0/8` was 16.7M strings and an IPv6 `/64` never finished expanding), `params.go` (bridge request structs).
- `pkg/simulator/` — simulated SNMP agents: v1, v2c and v3 with the whole USM, answering from a tree of objects on
  a loopback address, so SnmpLens can be tested and shown without a device. A leaf package — gosnmp and nothing of
  ours — so `pkg/snmp`'s own tests can drive it without an import cycle. See **The simulator**.
- `pkg/monitor/` — the poll clock and the alerting engine. `scheduler.go` owns one goroutine per monitoring session (this is what makes background mode real — the clock used to be a `setInterval` in the renderer, so closing the window silently stopped every session and every alert with it); `breach.go` turns samples into threshold/reachability episodes; `counters.go` corrects counter wraps and derives rates from the time that actually elapsed.
- `pkg/preset/` — the dashboard preset format: `preset.go` (the frozen widget vocabulary, `Validate`,
  `Estimate`, `PollOIDs`), `load.go` (reading a file off disk, listing a directory, matching a device). Pure: it
  touches no network, no database and no gosmi. See **Dashboard presets**.
- `internal/app/router.go` — the event router: routing runs on its own goroutine in bounded batches, with a durable watermark so a crash replays rather than loses. See **The trap path**.
- `pkg/events/`, `pkg/notify/`, `pkg/secrets/`, `pkg/service/`, `pkg/tray/`, `pkg/autostart/` — the event journal vocabulary, notification routing (syslog over UDP/TCP/**TLS per RFC5425**, webhook, email, with a durable outbox), OS-protected credential storage, the pre-GUI preference file, the fail-soft system-tray icon, and the per-user login entry (HKCU Run key / LaunchAgent / XDG autostart — never machine-wide, so it never needs elevation).
- `pkg/netaddr/address.go` — address handling shared by `pkg/snmp` and `pkg/network`. `NormaliseTarget` strips
  the brackets people paste around an IPv6 literal, because gosnmp adds its own via `JoinHostPort` and
  `[[::1]]:161` fails naming neither; zones (`fe80::1%eth0`) are kept. `SplitTarget` reads a port the target
  names (`10.0.0.5:1161`, `[2001:db8::5]:1161`) and lets it win over the port field — every dial, the trap
  sender's included, goes through it, a bare IPv6 literal's colons are never taken for a port, and `portOf` in
  `utils/targets.js` is the same reading on the renderer's side. `ListenAddress` returns the bare `:port`
  wildcard, which Go opens as a DUAL-STACK socket — `0.0.0.0` behaves identically but reads like a deliberate
  IPv4-only choice, which is how it gets "fixed" into one. `LastAddressIn` parses rather than pattern-matches.
- `pkg/network/tools.go` — pure-Go ping & traceroute (**pro-bing**); no elevated privileges required. Targets go
  through `netaddr.ValidTarget` before they become argv: there is no shell, so nothing can be injected as a
  command, but a value starting with `-` IS read as an option — measured, `tracert -d -w 2000 -h` answers "a value
  must be supplied for the option -h". A whitelist (IP literal or RFC 1123 hostname), not a `-` blacklist. macOS ships
  traceroute as IPv4-only with v6 in a separate binary, so an IPv6 target there runs `traceroute6`.
- `pkg/storage/storage.go` — SQLite (**modernc.org/sqlite**, WAL mode) for monitoring history. Data points are **batch-buffered**: `QueueDataPoints` appends to an in-memory batch flushed by a ticker goroutine, not written per-call. Sessions keyed by generated UUID.

Multi-target operations run concurrently with goroutines; one `BulkResult` per target is collected and returned.

## MIB lifecycle

Standard SNMPv2 MIBs are embedded via `//go:embed mibs` in `main.go`. On **first run only**, `app.startup` extracts them to the per-user config dir, points gosmi's search path there, and loads core modules (`SNMPv2-SMI`, `SNMPv2-TC`). Users add their own MIBs into that same persistent directory (via the import UI / drag-&-drop), so it is the single source of truth at runtime — not the embedded `mibs/`.

Persistent data location (`os.UserConfigDir()` + `SnmpLens/`):

| OS | Path |
| --- | --- |
| Windows | `%APPDATA%\SnmpLens\` |
| macOS / Linux | `~/.config/SnmpLens/` |

Contains `mibs/` (extracted + user MIBs), `presets/` (dashboard presets — a SIBLING of `mibs/`, never inside
it) and `monitoring.db`.

## Frontend layout (`frontend/src/`)

- `App.svelte` — top-level tabbed shell (Operations / Traps / History / Monitor / Dashboard / Discovery), global keyboard shortcuts, resizable MIB panel, file-drop wiring.
- One `*Panel.svelte` per tab; smaller pieces grouped under `operations/`, `mib/`, `settings/`. `DashboardPanel.svelte` is the eighth tab (Ctrl+8) and draws a bound preset — see **Dashboard presets**.
- `stores/` — Svelte writable stores are the state layer. Most persist to `localStorage` (settings, history, favorites, saved queries, polling sessions, MIB paths).
- `utils/` — `crypto.js` (AES-256-GCM encryption of credentials in localStorage; key stored as JWK), `anonymize.js` (Anonymous Mode masking), `formatting.js`, `csv.js`, `nativeNotify.js`, `snmpParams.js` (mirrors Go request structs).
- `i18n/` — svelte-i18n with 5 locales (`en`, `fr`, `de`, `es`, `zh`); locale auto-detected, overridable in settings. `setupI18n()` must resolve before the app mounts (`main.js`).

### Frontend conventions worth knowing

- **Credential custody.** The renderer seals the sensitive settings fields (`community`, the v3 passphrases,
  per-target overrides and credential profiles — `fields()` in `crypto.js`) and stores them in localStorage as
  before, in the same `enc:` + base64(12-byte IV ‖ GCM output) format. What changed is that the KEY is no longer
  beside them: it lives in `pkg/secrets` (`SettingsKeyRef`) and the renderer never holds it. `internal/app/settings.go`
  seals and opens in batches, because a save covers one value per sensitive field, plus three per target override
  and one or two per credential profile.

  Be precise about what that buys, because it differs: DPAPI and the Keychain tie the key to the account, while
  the Linux file backend keeps it away from OTHER accounts and out of a copied profile and nothing more. The
  banner in `SnmpSettings.svelte` names the backend rather than saying "encrypted". And the plaintext still lives
  in renderer memory while the app runs — every request builder reads it from the settings store. Moving that too
  means Go resolving credentials by profile, which is a much larger change and is NOT what this is.

  Two rules carry the safety. Sealing that fails must never fall back to writing the plaintext, and must never
  overwrite good ciphertext with a half-sealed object — `settingsStore.save` leaves the stored blob alone and the
  session keeps working from memory. Opening that fails blanks the IN-MEMORY value only: an `enc:…` string must
  never reach `buildSnmpRequest` and go on the wire as a community, and blanking the STORED copy — which the old
  code did on any error — turned one locked keychain into permanently lost credentials.

  The migration adopts the legacy JWK and removes localStorage's copy only after the store has been observed
  opening that user's own ciphertext. GCM authenticates, so a successful open IS proof the key is right; there is
  no window in which the credentials exist in neither place.

  `secrets.Open` mints a key ONLY when the protector has never held one. Answering any read failure with a new key
  — which it used to — writes over the real one, and every stored secret then fails to decrypt permanently. macOS
  distinguishes `security` exit 44 (no such item) from a locked keychain for the same reason. The store also opens
  independently of `storage.Init`: a corrupt `monitoring.db` must not take the credentials with it.
- **Anonymous Mode** is purely frontend masking and is intentionally **non-persistent** (always off on restart) — see `settingsStore.js` forcing `anonymousMode = false` on load. Don't make it persist.
- **An editor opened inside Settings handles Escape in the CAPTURE phase and stops it** (`<svelte:window
  on:keydown|capture>`, `NotifySettings`, `CredentialProfiles`). `SettingsModal` closes on an Escape reaching the
  window while BUBBLING, and two window listeners both run for one key: the sink editor's Escape closed the whole
  settings dialog with it, and every unsaved change in it. Captured on the window and stopped there, the event
  never reaches the bubbling phase. The screenshot director's `key:` step cannot show this — it dispatches AT the
  window, where stopping propagation does not stop other listeners on the same target.

## Credential profiles

A **credential profile** is a named set of SNMP identifiers a target is given instead of the default ones: a
community (v1 or v2c) or an SNMPv3 USM user — user, security level, both protocols and passphrases, context. It
carries its VERSION, because "this switch speaks v3 as ops-ro" is one statement, and keeping the version apart from
the user it goes with is how a v3 user ends up sent as a v2c community. The default identifiers are not a profile:
they stay the settings' own `community` and `v3` block, spoken in the version picked in the header, and every
target without a profile uses them.

The model is `utils/credentialProfiles.js`, pure and tested under node (`tests/profiles.test.mjs`). The SNMPv3
choices are `utils/snmpSecurity.js`, declared ONCE — the default identifiers offered six authentication protocols
while the per-target form offered four — and the test reads `getAuthProtocol`/`getPrivProtocol` in
`pkg/snmp/client.go` and requires every value offered to be one Go maps. Otherwise a profile saves cleanly and
fails on its first request.

**A target's identity comes from exactly one place**, resolved by `getEffectiveSettings` in this order: its profile
(`targetOverrides[address].profile`), its own overrides (community, version, v3), or the defaults. A profile
REPLACES the target's own credentials rather than being layered under them — `assignProfile` drops them — because
two answers to "what does this target authenticate with" is one too many. Transport (port, timeout, retries) stays
per target. A profile id that names nothing falls back to the defaults, never to nothing. A request carries only
the credential its version uses: a v3 target is not sent the default community beside its user, and a v2c target
is not sent the default v3 passphrases.

`credentialRef` on the effective settings says which of the three it was — `'default'`, a profile id, or `''` for
a target's own overrides — and `buildMonitorConnection` sends it as `MonitorConnection.Profile`, stored in
`storage.SessionConn.Profile`. An id and never a credential, so it is safe in a copied `monitoring.db`.

**Sealing is the custody above, unchanged.** `fields()` covers each profile's community and passphrases KEYED BY
THE PROFILE'S ID — never its position or its name — so a profile renamed, reordered, added or deleted while the
store was locked gets its own ciphertext back and never a neighbour's. `normaliseProfile` drops the credential of
the other kind and every passphrase above the security level, so nothing is sealed that no request will send.

**A session follows its profile.** A session is a SNAPSHOT of its connection — Go polls windowless with the
credentials in `pkg/secrets` — so rotating a profile's passphrase used to leave every session built from it
failing authentication until somebody rebound it. `pollingStore.followCredentials` runs when the settings dialog is
saved. `changedCredentialRefs` compares identities field by field, because `MonitorUpdateConnection` restarts a
running session and a restart throws away the samples its deltas and rates are derived from: key order or a rename
must not count as a change. Each affected session gets the new identity with its own transport kept, and the
version travels too (`MonitorUpdateConnection(id, version, conn)`), since a profile carries one; the defaults carry
none, so a session on them keeps its own. Nothing is followed unless the store is open — with a locked keychain the
passphrases are blank in memory, and following would write nothing over working credentials. A deleted profile is
not a change: its sessions keep what they have, the way deleting a preset stops nothing.

**`startPolling` polls each target with its own identifiers**, one Go session per group (`groupTargets`) when they
differ, since a session holds one connection; the split is announced. It used the global settings for every target,
so a device with a profile — or merely a community of its own — was asked with the default one, and every reading
was an error that looked exactly like an unreachable device. The version picked in the Monitor form applies to the
targets on the defaults.

**The trap listener accepts every SNMPv3 user at once** — the default v3 block and every v3 profile, each unless
it was opted out — through gosnmp's `SnmpV3SecurityParametersTable` (`pkg/snmp/usm.go`). It used to take one user, so a device sending as any
other was dropped with nothing on screen. Three facts about gosnmp v1.45.0 decide the shape:

- `listenUDP` asserts `Params.SecurityParameters` to `*UsmSecurityParameters` for EVERY v3 packet to compare engine
  IDs, logs when the assertion fails, and dereferences the result anyway. A table with no SecurityParameters beside
  it is a nil-pointer panic on gosnmp's own goroutine at the first v3 trap, which no recover of ours covers
  (`TestATableNeverTravelsWithoutSecurityParameters`).
- `Table.Add` localises keys and validates nothing, so `checkTrapUser` refuses what would be added and then
  authenticate nothing — an AuthNoPriv user with no protocol — and names what gosnmp would report as
  "hashPassword: password is empty". A refused user is reported by name, and the listener starts with the others:
  receiving nothing over one stale profile is worse than receiving from everyone else.
- The table can be ADDED to while the listener reads it and never removed from, so `UpdateTrapUsers` RESTARTS the
  listener on its port — and only when the accepted set changed, compared as a set of what receiving uses (`trapUser`
  drops the context and anything above the level). Updated in place, a deleted user would keep authenticating and a
  rotated passphrase would work in both forms. `trapLife` serialises start, stop and update, and a stop now WAITS for
  the listen goroutine to let go of the field; otherwise a start straight after it is refused as "already running".

The accepted users are remembered in `pkg/secrets` under `TrapUsersRef()` on every start and every update, because
the listener can be started at login with no window to hand them over: `internal/app/service.go` passed an empty `V3Params`
there, so a background listener dropped every v3 notification whatever the settings said. The renderer pushes the
set once the stored credentials are open (`settingsReady` — before that the store holds sealed strings) and again
whenever it changes.

**Who is heard is the operator's choice, and the Traps tab says so.** A v3 profile carries `acceptTraps` and the
default user `settings.traps.acceptDefaultUser`, ticked by default in the settings and switchable from a profile's
row. Both are absent-means-yes, so opting out is written on purpose, and neither is a credential change: excluding
a profile from traps restarts no session. A community profile has no such switch, because gosnmp does not check the
community of a v1 or v2c notification — there is nothing to accept or refuse, and the Traps tab says "any
community" rather than implying otherwise. That tab lists who is heard (`trapReception`), marks a user Go refused
with its reason, and opens Settings at the profiles through `settingsRequest`, the sibling of `tabRequest` that
carries a section and an anchor.

gosnmp re-localises the keys to the SENDER's engine ID on receipt, which is what lets one table entry serve every
device configured with that user. `pkg/snmp/usm_test.go` drives it end to end — a real sender with an engine of its
own, two users, an unknown one and a wrong passphrase in between — because a test of our table-building would pass
just as well if gosnmp tried only the first entry.

## Why a MIB did not load

`gosmi.LoadModule` returns `Could not load module at X` for a missing file, a PDF, a syntax error on line 412 and
an unsatisfiable IMPORTS clause alike — and it throws away the one thing that would tell them apart:
`smi.LoadModule` receives `internal.GetModule`'s real error (`Parse module: …X-MIB:2:1: unexpected "("`), prints
it with `fmt.Println`, and returns an empty string. `pkg/mib/diagnose.go` captures stdout to recover it
(`captureStdout`, reader on its own goroutine because a pipe holds ~64 KB and the output matters most when there
is a lot of it), and `shortenPaths` drops the directory so the position survives.

`Diagnose` reports a STAGE — read / content / parse / imports / build / semantic / loaded — plus located
diagnostics, an excerpt with a caret, the imported modules that cannot be satisfied and the symbols each was
needed for, and a chain followed to its root when a dependency fails too. It recognises the files people actually
download by mistake (HTML page, PDF, zip, UTF-16) because all four reach gosmi as the same sentence, and it warns
when a file's name does not match its module, which loads fine and is invisible until something imports it.

A module can load AND be broken: gosmi resolves imports lazily and returns nil rather than failing, so a MIB
importing a module you do not have reports success and resolves to nothing. That is why `Diagnose` is offered on
successes too, and why the missing import stays the headline rather than the unresolved references it causes.

`Diagnose` READS. It never calls `gosmi.LoadModule`, and neither does anything it calls: an explanation that
loads modules as a side effect re-enables MIBs the user switched off — they stay in the global node index while
absent from the tree, so `Translate`, `ResolveOid`, `Table` and `Symbols` all start answering from a module
nobody asked for. gosmi's discarded message is captured around the load in `LoadWithDiagnostics`, which is the
only moment it exists.

An import that is present but not loaded is reported as `notloaded`, not `failed`: it is usually disabled, not
broken. Only a dependency whose own diagnosis stops at read/content/parse is `failed`, with its cause.

`LoadWithDiagnostics` runs it automatically on failures only — it re-reads and re-parses, which is worth doing
once something is wrong and not for two hundred working vendor MIBs.

**An empty file list means empty**, in the service. It used to mean "everything in the directory", which
re-enabled every MIB the user had switched off; the policy lives in `App.LoadMibsWithDiagnostics`, which
enumerates explicitly. This only surfaced when the `.mib`/`.txt` filter was removed — the bundled MIBs are
extension-less, so the fallback quietly loaded nothing.

In tests, reset gosmi with `Exit()` **then** `Init()` and use `SetPath` rather than `AppendPath`: `Init` alone
finds the existing handle and returns with every previously loaded module still in it, and `AppendPath`
accumulates directories — a deleted one then aborts every lookup, because `GetModuleFile` returns the ReadDir
error instead of trying the next path.

## Conceptual tables

A walk is a flat list; a table is that list pivoted by column and split by INDEX. The pivot is
`frontend/src/operations/tableRows.js` (pure, unit-tested); the split is `pkg/mib/table.go`, and it has to be in
Go because the rules are RFC 2578 7.7 and they live in the MIB: how many sub-identifiers an index object consumes
depends on its SYNTAX, on whether its size is fixed, and on whether the row says IMPLIED. Before this the
instance was kept as the raw sub-OID, so `tcpConnTable` — INDEX of four objects — rendered one opaque
`10.0.0.5.161.192.168.1.9.50000` per row, a name-keyed table showed decimal bytes, and every table sorted 10
before 9.

`EncodeIndex` is the same rules read backwards, and deliberately NOT gosmi's `Type.IndexValue`: that one always
writes a length prefix for an OCTET STRING unless IMPLIED, and a fixed-SIZE index carries no length at all — an
`IpAddress` index encoded its way is five sub-identifiers and addresses a row that does not exist. A round-trip
test over ifTable, ipAddrTable, ipNetToMediaTable and tcpConnTable holds the two halves together.

Row creation writes every column **and** RowStatus in ONE `SnmpSetMultiple`: RFC 3416 makes a SET atomic across
its varbinds, so one-at-a-time asks the agent to accept a row that is incomplete at every step and leaves half of
one behind when it refuses. Index columns are never written — their value is carried by the instance. A table is
editable only if it has a `RowStatus` column, because RFC 2579 gives no other way to create or destroy a row.

`gosmi.GetNodeByOID` returns the closest known ANCESTOR with `err == nil` rather than failing, so an unknown OID
resolves to `iso`. `pkg/mib` checks `Kind` rather than trusting the lookup.

## MIB editor

The MIB editor tab (`MibEditorPanel.svelte`, `pkg/mib/editor.go`, `internal/app/mibeditor.go`) edits the MIBs in the
persistent directory, or opens one from anywhere and saves it in. Validation is two-tier because the two tiers
see different things: `parser.Parse` (gosmi's own sub-package) gives **line:column** syntax errors and is pure, so
it runs while you type; `gosmi.LoadModule` sees semantic errors but reports them as `Could not load module at X`
with no position at all — it discards the parser's positioned error with a `fmt.Println` in `smi/module.go`.

Editing is safe to attempt: a failed load leaves the previously loaded tree untouched (`IsLoaded` stays false),
verified rather than assumed. What is NOT safe is succeeding at saving a broken standard MIB, since nearly every
other MIB imports from `SNMPv2-SMI`/`SNMPv2-TC` — hence the bundled marker, the automatic backup, `MibEditorRestoreBundled`,
and the post-reload health probe.

Two rules that look like tidiness and are not: **backups go to a sibling `mib-backups/` directory**, because
`ListMibFiles` filters only dotfiles and `os.ReadDir` is alphabetical, so `IF-MIB.123.bak` beside `IF-MIB` would
load first as the same module and win; and **every path goes through `resolveMibPath`**, because these methods
write and `monitoring.db`, `service.json` and the secret store all sit one directory above `mibs/`.

The editor's buffer lives in `stores/mibEditorStore.js`, **not** in the panel component: the tab shell mounts
panels with `{#if activeTab === …}`, which destroys the component on every switch, and component state took the
user's edits with it. The buffer is also mirrored to a draft file under `mib-drafts/` (a sibling of `mibs/`, for
the same reason backups are) so it survives closing the window.

`pkg/mib/analyse.go` is the semantic pass, and it exists because of a measurement: a MIB declaring
`SYNTAX Integerr32` **and** assigning the same OID twice loads with `err=nil` and `IsLoaded=true`, then resolves
both objects to a nil type and an **empty OID**, with nothing anywhere saying a word. gosmi looks the type up,
gets nothing, breaks, and adds the object regardless. So `Analyse` checks what neither `parser.Parse` (syntax
only) nor `LoadModule` (a boolean that is not even true) will: unknown types, duplicate OIDs, unresolved parents,
missing modules in `FROM`, undefined `INDEX`, readable conceptual rows. The bar is zero errors on the 14 bundled
MIBs, and that is a test.

Row creation sends the **wire type** `pkg/mib` works out from the column's BASE type, not the SMI or textual-
convention name: the name went into a substring matcher that mapped Gauge32, TimeTicks and Counter32 onto
INTEGER, and a SET being atomic, one wrong tag refuses the whole row. `gosmi.Table.Implied` does NOT follow
AUGMENTS although `GetIndex` does, so `effectiveImplied` reads it from the row the INDEX actually came from —
otherwise an augmenting row's IMPLIED last index is encoded with a length prefix and addresses a row that does
not exist. An OCTET STRING index accepts the colon-separated hex `renderOctets` displays, or a MAC-keyed table
could be read and never written.

`AnalyseAll` is the entry point the bridge uses, and it exists for one reason: `parser.Parse` costs about 40 ms
on a 185 KB MIB, and calling `Validate`, `CheckImports` and `Analyse` in turn parsed the file three times on every
pause in typing. There are benchmarks in `bench_test.go`; run them before adding a check that parses.

The editor colours only the visible window, the way an IDE does. It cannot parse incrementally — participle
offers no way in — but the viewport half is free. `highlight` takes an initial `inString`, because the state
carries across lines and a window opening inside a multi-line DESCRIPTION would otherwise paint the rest of the
file as a string; `stringStateAt` answers that by scanning without producing anything (4 ms where tokenising is
29). Unrendered lines are stood in for by blank lines, so the mirror keeps the textarea's height and scroll sync
still works — the tokenise test checks the line count is preserved exactly.

An analysis result is applied only if the buffer it describes is still the buffer on screen. Analyses overlap
while the user types, nothing orders their answers, and diagnostics carry line numbers: showing the ones computed
two keystrokes ago points at lines that have moved.

`frontend/src/mibeditor/metrics.js` turns a (line, column) into pixels, which is what makes squiggles, hover
cards and caret-anchored completion possible **here and not in a plain textarea**: the mirror lays out identically
to the text (unit-tested) and both are monospace with a fixed line height, so a position is arithmetic once the
character width is measured once. Editing inserts through `execCommand('insertText')` — deprecated, but the only
way to write into a textarea AS AN EDIT, which is what the native undo stack records.

`CheckImports`/`FixImports` answer the question people actually have in front of a vendor MIB: which symbol is
used without being imported, and which module it comes from. Only names the loaded tree knows are reported, which
keeps false positives near zero, and the fix edits the IMPORTS clause as TEXT — a MIB carries comments and
alignment no AST printer would preserve.

`AnalyseAll` also returns an **outline** — what the buffer defines, with a position, a kind, a syntax, an access
and a status each (`pkg/mib/outline.go`). It comes off the parse that already happens, so it costs nothing and,
in particular, takes no gosmi lock; an outline built from the LOADED tree would describe the file as it was when
it was last loaded, which for the file being edited is the one description that is wrong. A file with a syntax
error still has an outline of everything above the error, which is when it is most useful.

The **symbol catalogue is cached** (`Symbols()`), and that is a measurement rather than a precaution. Building it
over a corpus the size a vendor folder really is — 135 modules, 24 227 symbols, built by copying the bundled MIBs
under new module names — cost 113 ms, superlinear in the symbol count, all of it holding the exclusive lock. The
editor asks for it through `AnalyseAll` 350 ms after every pause in typing: that round cost 122 ms, of which
1.35 ms was the analysis. Cached, 1.35 ms. `invalidateCatalogue` is called from every path that changes what
gosmi has loaded — a stale catalogue reports an import as MISSING that has just been satisfied, which reads as a
broken file — and `mib.InitPath`/`mib.LoadCore` exist so that the startup path goes through the lock too rather
than calling gosmi directly.

`pkg/mib` takes a package-level **exclusive** `Mutex` around gosmi — not an RWMutex, because gosmi has no read-only
operations: `internal.(*Object).GetSmiNode` is a getter that MEMOISES, writing `x.Oid` and `x.OidLen` on first
call. Two goroutines holding a read lock and resolving the same OID write the same fields at once, reproduced
under `-race`. gosmi's state is global and Wails dispatches each bound method on its own goroutine, so the lock
is held for a batch rather than per OID: a 300-varbind walk takes it once.

`Rebuild` holds that lock from the teardown through the health probe. Releasing it after the core modules let
readers into a world holding only `SNMPv2-SMI` and `SNMPv2-TC` — measured, 12 reads of sysDescr came back with a
DIFFERENT NAME, not an error — and let two rebuilds interleave, each `Exit`/`Init` destroying what the other had
loaded, so the probe reported a failure that was not real and `internal/app/mibeditor.go` routed it to every sink as a
"major" event. CI runs `-race` for exactly this class.

## The trap path

A trap arrives on gosnmp's UDP receive loop, and that loop is **strictly serial**: one goroutine,
`ReadFromUDP` then handler then the next read, with no goroutine per datagram (verified in gosnmp
v1.45.0 `trap.go`). Every millisecond the handler spends is a millisecond not reading the socket, and what
does not fit the socket buffer is dropped by the KERNEL before Go sees it — no error, no journal entry,
nothing to count. Three things follow, and each is load-bearing.

**The socket read buffer is raised to 8 MiB** (`pkg/snmp/trapbuf.go`). Measured with a burst of 2000
datagrams: 417 journalled with the system default, 2000 with 8 MiB. It is the single largest thing
deciding whether a storm is recorded and it dwarfs anything the handler's speed buys. gosnmp keeps its
socket in an unexported field and offers no accessor — `WithBufferSize` is the per-read message size,
which is different — so this reaches into the struct. That is acceptable only because it is FAIL-SOFT
(anything unexpected leaves the default) and PINNED BY A TEST, so a gosnmp upgrade that renames the field
fails CI rather than quietly halving the next storm. The durable fix is a `WithReadBuffer` upstream.

**The insert stays synchronous, the ROUTING does not.** gosnmp sends an INFORM's acknowledgement after the
handler returns, so acknowledging a confirmed notification before it is durably journalled would be a lie.
Routing was measured at 1.30 ms per event against 0.97 ms for the insert — essentially all of it a second
write transaction — and now goes to `eventRouter` (`internal/app/router.go`). Batching is not an optimisation on
top: unbatched the router costs the same 1.0 ms per event and cannot keep up with a producer it just made
twice as fast. At 50 events per transaction it costs 0.13 ms.

**`TrimEvents` runs on the caller's goroutine too**, every 256 inserts, and at the retention caps it was
freezing the trap loop for 130-330 ms. Moving it to a background goroutine does NOT help and was measured
making things worse — SQLite serialises writers, so `InsertEvent` blocks on the write lock instead (mean
2.5 → 8.5 ms, worst 62 → 335 ms). The cost had to come out of the QUERY: a `seq` cutoff and an indexed
range delete instead of `NOT IN`, payloads deleted while their events still name them, `NOT EXISTS`
against the unique id, and a partial index for the payload cap. 172.9 ms → 20.5 ms.

Measured end to end afterwards, as LOSS at an offered rate — never as one over a mean, since offering at
1/mean is a utilisation of 1.0 by definition: 100% journalled at 2000/s sustained, first loss at 5000/s.
Note the shape the buffer produced: rate alone decides nothing, because a burst that fits is absorbed
whatever rate it arrives at. What costs datagrams is sustained overload for longer than the buffer covers.

### The routing watermark

`notify_watermark` records how far routing has got through `events.seq`; anything above it is replayed at
startup. Replay is harmless because the outbox is `INSERT OR IGNORE` against `UNIQUE(event_id, sink_id)`.
Five rules, each of which is a lost alert if broken:

- **The deliveries and the watermark are ONE transaction** (`EnqueueRouted`). A watermark committing
  without them names events that are then never replayed and never delivered.
- **It is NOT `max(seq)` of the flushed batch.** `seq` is allocated by the insert and the event reaches the
  queue later — measured 2.4 ms, p95 7.5 ms — so two events can be handed over in the opposite order to
  their seqs, and taking the max strands a lower one still in flight. It is the lowest seq still owed,
  minus one, and it never retreats.
- **It is seeded in the SCHEMA, at `MAX(seq)`**, never lazily and never at 0. Created lazily, a crash
  before the first flush leaves the row absent and the "absent means MAX(seq)" rule then skips exactly the
  events it was meant to protect. Seeding at `MAX(seq)` also stops an upgrade re-delivering history.
- **One transaction is bounded** (`routeBatchSize`), not "everything queued": SQLite serialises writers, so
  its size is how long the trap listener's insert is blocked. 10 000 rows measured 392 ms; a replay of the
  full retention caps would be ~14 s, past the busy timeout, losing a trap while its INFORM is acked.
- **A failure is not silence.** `routedGroupsFor` returns an ERROR when the configuration cannot be read,
  because returning nil made that indistinguishable from "no rule matched" and the watermark then moved
  past the event. An event that fails to route is left in flight; replay STOPS at one rather than skipping
  it. For the same reason `flushBatch` now puts unwritten data points BACK — it is the batching precedent
  in this repository and it dropped its buffer before the write, losing a batch per `SQLITE_BUSY`.

**Waiting for a pooled database connection is reported, not eliminated.** `pkg/storage` uses the
context-free `db.Query`/`db.Exec`/`db.Begin` throughout, so a caller that cannot get one of the four
connections simply blocks — and `busy_timeout` does not cover it, because that governs SQLite's lock
rather than Go's pool. Measured with all four held: `InsertEvent` blocked 748 ms and returned `err=nil`,
with nothing in any log. The fix is NOT a bigger pool: measured under three writers and two readers, four
connections gave `InsertEvent` a 5.53 ms average with 5541 waits totalling 650 ms, and sixteen gave zero
waits and a 6.19 ms average — the waits vanish and the path gets slightly SLOWER, because the real
serialiser is SQLite's single writer. Nor a deadline on the trap insert, which would lose the event and
leave an INFORM unacknowledged. What was missing was a way to KNOW, so the router samples
`db.Stats()` every 30 s and records one system event when a window spends more than three seconds
waiting — edge-triggered, so a busy hour produces one event rather than one hundred and twenty.

Quiet hours are evaluated at the EVENT's timestamp, converted to the local zone. The conversion is not
cosmetic: every producer writes `Ts` in UTC while quiet hours are wall-clock times an operator typed, so
comparing them directly rotates every window by the machine's UTC offset. A `Ts` that does not parse falls
back to NOW, never the zero time — 00:00 is inside every window that wraps midnight, which would silence
the alert permanently.

The trap listener is stopped FIRST in `App.shutdown`, before any consumer. `Client.trapListener` is behind
a mutex (three goroutines wrote it), the listen goroutine clears the field only if it is still ITS
listener, and the stop WAITS for `Listening()` before closing: gosnmp's `Close` returns early doing nothing
while `listenCloser` is nil, having already set `finish`, so a stop landing in the bind window reported success and
left a listener nothing could stop — measured, still running 2.1 s after `Close` returned in 73 ms.

## The simulator

`pkg/simulator` is the agent the built-in device simulator stands on; value behaviours, traps, the device
catalogue and the interface build on it. An `Agent` answers GET, GETNEXT, GETBULK and SET in v1, v2c and v3 from a
sorted tree of `Object`s. gosnmp encodes and decodes; what the package owns is what an agent DECIDES.

**Loopback only, and checked twice.** `CheckListen` accepts 127.0.0.0/8 and `::1` and nothing else — a host name
is refused rather than resolved, `localhost` excepted and mapped without asking the resolver — and `Agent.Start`
checks the socket it actually bound (`checkBound`), because a zero `Agent` has a zero address and a zero address
binds EVERY interface. The rule is Go's, not a form default: a device file is JSON someone else may have written.

**The USM's checks come before gosnmp.** gosnmp's receive path was written for a trap receiver, and three of its
behaviours are wrong for an agent: it re-localises its keys to whatever engine ID a message names, so a message
addressed to another engine authenticates; asked to trust the message's parameters, it takes the security level
from the message too, so a noAuthNoPriv message naming an authPriv user is checked for nothing; and an empty user
with an empty engine ID skips authentication whatever the flags say. `peekV3` reads the header with a small BER
reader (fuzzed, `FuzzPeek`), and the engine ID, the user and the level are settled in RFC 3414 3.2's order before
gosnmp sees a byte. Each refusal is a Report naming its counter, sent only to a message that asked for one, and
unauthenticated except `notInTimeWindow` — signed, so a manager can trust the clock it resynchronises to.

**Authentication stays in gosnmp** (`UnmarshalTrap`, which despite its name decodes any PDU) rather than an HMAC
written here: `codeql.yml` runs `security-and-quality`, and HMAC-MD5 and HMAC-SHA-1 are what RFC 3414 mandates. The
cost is one ambiguity. gosnmp checks the digest and decrypts in one call, so `refuse` decodes again without the
digest to tell a wrong digest from a wrong privacy key, and a message with BOTH passphrases wrong is reported as a
decryption error where the RFC names the digest. Either way the manager is told about a passphrase that is wrong.

**Every encrypted message draws its own salt.** `MarshalMsg` sends whatever `PrivacyParameters` holds — gosnmp
draws a salt only on its own send path — and the salt is the only part of the IV that moves between two messages
sent in the same second.

**An Agent runs once.** A device that restarts is a new `Agent` with `EngineBoots` one higher, and the caller keeps
the count: that is what makes a manager still holding the old clock resynchronise, which
`TestAManagerFollowsARestartedDevice` drives through gosnmp. The engine ID must not change between runs (`EngineID`
builds the MAC format a Cisco reports), since managers cache it and localise their keys to it.

A request below the level its user is held to is answered `authorizationError` with nothing read, as a device
configured `rouser NAME priv` answers. Only the default context exists. SNMPv1 cannot see a Counter64 (RFC 3584):
it is stepped over on GETNEXT and `noSuchName` on GET.

**A SET writes the running device and nothing else** (`set.go`). The write community — a secret, kept beside the
community — or a v3 user with `Write` may SET. A manager that only reads is answered `noAccess`, as net-snmp answers
a `rocommunity` or a `rouser`, or `noSuchName` in v1, and a community used so is counted in
`snmpInBadCommunityUses`. What is written lives in the agent's tree until the device restarts, which is a new
`Agent` built from its model again: nothing is saved, as a running configuration is not until someone saves it.
Every varbind is checked before any is applied (RFC 3416 4.2.5), and the tree is REPLACED rather than edited — a
copy with the changes, swapped in through an `atomic.Pointer` — because notifications read it from their own
goroutines, and neither they nor a GETBULK may see a SET half-applied. The agent's own subtrees (the snmp group, the
engine, the USM, the VACM) and an object only a notification carries are `notWritable`. A varbind naming an instance
nothing answers CREATES it, if it would sit in the tree as an instance does: a leaf, under no instance and with none
under it. Errors are v2c's, mapped for v1 by `v1Status` (RFC 3584 4.4).

Rows need the MIB, and the agent has none: which column is a `RowStatus` is asked of `Config.RowStatus`, which
`internal/app/app.go` points at `mib.Service.RowStatusColumn`, so rows are created and destroyed as the LOADED MIBs describe
them. `createAndGo` makes the row `active`, `createAndWait` `notInService`, `destroy` removes every instance of the
row, and what RFC 2579 refuses is refused — a create on a row that exists, `active` on one that does not, and
`notReady`, which is the agent's to say. The callback takes gosmi's lock once per varbind, on the agent's receive
goroutine, holding nothing of the agent's while it waits.

**A device's data: parameters, values of its own, a preview** (`params.go`, `overrides.go`, `preview.go`). A model
may declare numbers a device can be given (`ModelParam`: the Catalyst's ports, the Synology's disks, the PDU's
outlets, the Linux server's processors), bounded by what the model could be and named in the interface by
`simulator.param.<name>`; the builder reads them through `Identity.count`, and a device given none is the model as
catalogued. Two rules keep a parameter from breaking a notification. A Catalyst given fewer ports keeps its uplinks
at the model's ifIndexes, and the bridge numbers its ports by their PLACE among the physical interfaces — the two
coincided only while the ports were contiguous, and the first test of a smaller switch indexed past the end of the
port list — so `buildCatalyst` converts ifIndexes to bridge ports (`portOf`). And the PDU never has fewer than the
eight outlets its `rPDUOutletOff` names. `TestEveryModelParameterBuildsAtItsBounds` builds every model at every
bound and fails on a notification object that goes missing.

A device's own values (`Override`) are written in a custom model's vocabulary — a type the SMI names, a value, hex
for octets — take the place of the model's object at their OID or join them, and are refused in the agent's
subtrees as a model file's objects are. Where one sits is checked at SAVE, by building the model for the device and
making the tree (`Device.Validate` → `objects` → `newTree`): an instance under another would otherwise surface as a
start that fails later, with a message nobody connects to the edit.

`PreviewRows` reads the very objects the agent would be given, so the Data tab shows what a walk finds, less the
agent's own counters — read as if the device had run since the tab was opened, and read again every five seconds,
so that what moves is SEEN moving. Each row says how its value behaves (`behaviourOf`, by the kind of reading
`values.go` made: static, counter, gauge, uptime, clock, computed), which is what "only what moves" keys on.
`SimulatorPreview` takes the device as the editor holds it — saved or not — with its secrets stripped on both sides
(`previewPayload` in the renderer, `WithoutSecrets` in Go). A filter of dotted numbers is a subtree; anything else
is looked for in the MIB names, so every object is named first (`mib.Service.NameOIDs`, one hold of gosmi's lock
for the batch) — and only a scalar or a column names an OID, because gosmi answers with the closest node it knows
and `SNMPv2-SMI::enterprises` followed by nine arcs is no name to search by. The renderer applies only the latest
answer: previews overlap while someone types, and nothing orders them.

The protocol names are `pkg/snmp`'s (`MD5` to `SHA512`, `DES`, `AES` to `AES256C`), mapped again here so the
package stays a leaf. `TestSnmpLensReadsTheSimulator` holds the two together by driving the SnmpLens client
against a user of every name: a name mapped differently fails as a digest or a decryption error.

**Devices live in `simulator.json`, and their secrets do not.** `internal/app/simulator.go` keeps the devices in a file
beside `monitoring.db`, and their communities and passphrases in `pkg/secrets` under `SimulatorDeviceRef(id)` —
the file is what a person copies to another machine. `Device.WithoutSecrets` runs on every write AND every read,
so a community typed into the file by hand is not one the application sends. The renderer's list is a
`SimulatedDevice`, a type with no field that could hold a secret, rather than a `Device` with them blanked: a
secret field added to `Device` later cannot reach the renderer by default. Secrets come back out through one call,
`SimulatorDeviceCredentials`, for the editor and for "Add as target", and `internal/app/simulator_test.go` searches the
file and the marshalled list for the secret values themselves. The two listing calls are `List…` for
`tools/genbridge.mjs`, which answers such a binding with an empty array and anything else with `null`.

**The backend gives the engine.** A new device's ID and engine ID are made in Go (`NewDeviceID`, and
`NewEngineID`: the model's vendor in the MAC format, the MAC drawn from the ID), and an edit keeps both, and the
boot count, whatever the renderer sends — the engine ID is what managers localise their keys to, and the count is
what keeps a previous run's messages out of the time window. Every start raises the count and WRITES it before
the device answers. A running device that is edited restarts; nothing starts by itself at launch unless it is set
to (`AutoStart`). The header's
status and the modal read `simulatorStore`, which Go refreshes with `simulator:changed`.

**A model is a vocabulary, not prose.** `simulator.Models()` serves an ID, a category and the notifications; the
name and the description of a built-in model are `simulator.model.<id>`, and the category
`simulator.category.<category>`, in the five locales, and `tests/simulator.test.mjs` requires `en.json` to answer
for every ID and category in `pkg/simulator`. A custom model is the exception, and says so (`Custom`): no locale
knows it, so it carries its own name, description and vendor. Values are pure functions of time (`values.go`):
counters that only go up and wrap at 32 bits as a real interface's do, gauges that swing, nothing ticking in the
background.

**The catalogue is what the presets poll.** Fourteen models: a Linux server, a Windows server, the iDRAC9 of a
PowerEdge R750, a Catalyst 2960 with 24 and with 48 ports, an ISR 4331 with two eBGP sessions, a MikroTik RB4011, a
FortiGate 60F, a UniFi U6 Pro, a Synology NAS, a three-phase APC Smart-UPS, an APC switched rack PDU, an HP LaserJet
and an ENTITY-SENSOR-MIB probe. Each says which bundled presets it feeds (`modelPresets`), and
`TestEveryModelFeedsItsPresets` expands those presets' widgets against it, discovery walks included — a model
cannot be added without saying what it answers, nor a preset grow a widget its models leave empty. Three choices
look odd without that. The Catalyst numbers its ports from 1, because "Switch drawing (24 ports)" polls
`ifOperStatus.1` to `.26`, where a real 2960 numbers them from 10001. The UPS is the three-phase model, because
"UPS (RFC 1628)" charts three output lines. And interfaces, host resources and the system group are shared
builders (`ifaces.go`, `hostres.go`, `addSystem`), so what a preset reads means the same on every model.
`TestTheCiscoPresetClaimsTheSimulatedCiscos` holds the identification: the "Cisco (IF-MIB)" preset claims the
Catalysts and the ISR by their CISCO-PRODUCTS-MIB OIDs, and nothing else. The NAS, the probe and the UniFi report
net-snmp's Linux sysObjectID because their agent is net-snmp, as most embedded devices' is; their own tables tell
them apart. The vendor OIDs were read from the MIBs (LibreNMS keeps them), not remembered.

**A model answers a whole agent's walk** (`stack.go`, `bridge.go`, `ucd.go`, `hostres.go`), from about 300 objects
for the probe to 5 600 for the 48-port Catalyst. A device is described ONCE — its interfaces, addresses, gateway and
routes, sockets, hardware, bridge ports and VLANs, LLDP neighbours — and every standard MIB is built from that one
description, so they agree the way a real agent's do: an address sits on an interface ifTable lists, a route leaves
through one, tcpCurrEstab counts the sessions tcpConnTable lists and hrSystemProcesses the rows of hrSWRunTable, a
bridge port is an interface and the stations learnt on it sit behind it, a port in ENTITY-MIB points at its interface,
the ISR's BGP-learnt routes go through the peer BGP4-MIB says is established, over the TCP session on port 179 its
tcpConnTable lists. IF-MIB is whole — every column of ifTable and ifXTable, a 32-bit counter and its 64-bit twin built
from the same arguments — and a figure a MIB states twice (memory used and available, a percentage and its parts,
the two halves of a 64-bit disk size, APC's run time in TimeTicks and UPS-MIB's in minutes) is DERIVED from one reading
at the same instant rather than drawn twice. `TestTheStandardMIBsAgree` holds the cross-references, and `pkg/mib`'s
`TestSimulatedTablesDecode` walks every model and decodes every table the bundled MIBs describe with SnmpLens's own
decoder: an index written any other way than RFC 2578 7.7 reads it back fails there rather than as a table of garbled
rows. LLDP-MIB lives under 1.0.8802, outside .1.3.6.1, as on a real device — a walk of .1.3.6.1 does not see it.

**What the agent counts is the agent's** (`agentobjects.go`). SNMPv2-MIB's snmp group, and for v3 snmpEngine, the MPD
and USM statistics and snmpUnknownContexts, are read live from the counters the agent keeps as it answers — the
request counted on ARRIVAL, as net-snmp does, so a GET of snmpInGetRequests counts itself — through the clock each
request carries. Every device has them beside its model's objects, so neither a built-in model nor a file somebody
wrote may answer them; a custom model that tries is refused at import, naming the OID.

**A package is a model spread over a folder** (`package.go`, `walk.go`): `model.json` — the lone-file format, its
`system` optional once a walk records one —, `oids.json` and `oids/*.json`, `traps.json`, and `walks/` holding what a
real device answered, as snmpsim's `.snmprec` or `snmpwalk -On` output, told apart by the first line and never by
the name. Three rules put the files together, each the answer to a question a lone file never raised. What is
WRITTEN wins over what was RECORDED, OID by OID — and IF-MIB as a whole when `model.json` says `interfaces`, since a
written ifTable mixed with recorded rows gives ifNumber two answers — while between written files an OID given twice
is still an error. The agent's own objects stay behind: the snmp group and all of `1.3.6.1.6.3`, which in a real
walk holds the engine ID, the USM user names, the VACM groups and the community strings; served, they would
contradict the live counters and publish the recorded device's configuration. The system group is MADE, never
replayed, so sysName is the device's own and sysUpTime its own uptime. A recorded counter grows at its value over
the recorded sysUpTime — the average since boot, the uptime floored at a minute so that a device recorded just after
booting does not count as carrying its whole traffic in seconds. A walk is read LENIENTLY where a model file is read
strictly: it is a recording, not something written line by line, so a value that cannot be read is counted and
located (`walkSkipped`) rather than refusing forty thousand good ones, and `FuzzReadWalk` holds that whatever IS
read is a value the tree accepts. The application keeps a package as `<id>.zip` of the files it reads — never as a
folder of the archive's own names — and a lone file as `<id>.json`. Importing one form removes the other AFTER
writing, and `loadModels` takes the newer when a stopped import leaves both: two kept forms of one ID would
otherwise fail `SetCustomModels` and take every custom model with them.

**Recording a device is walking it into a package** (`internal/app/simrecord.go`, `pkg/snmp/record.go`,
`pkg/simulator/record.go`). `Client.Record` walks one device under `.1.3.6.1` and `.1.0.8802`, keeping each varbind
as gosnmp DECODED it — the types are the point, and `Walk`'s formatted results lose them — and `simulator.Recording`
writes each as a `.snmprec` line: an OCTET STRING as text only when every octet is printable ASCII and in hex
otherwise, so that the octets read back are the octets sent (`TestARecordingReadsBackAsWhatWasSent` round-trips every
type through `readWalk`). The agent's own subtrees are never WRITTEN, not merely not served: a recording is a file
somebody may pass on, and in a real walk that subtree holds the device's user names and community table. The walk
runs outside the simulator's lock, which a large device would hold for minutes — one recording at a time, guarded
apart, and `SimulatorCancelRecording` stops it with nothing kept. The request is built in the renderer through
`getEffectiveSettings`, as every request is, so a device is recorded with its target's profile or overrides, and
`recordRequest` is checked against the Go struct's tags as the other request builders are. Export writes a kept
model as a package FOLDER (`repackUnder`), its icon under the name the model gives it, which imports back as itself:
that is how a recording is edited.

**A bench moves as a file** (`internal/app/simbench.go`, `pkg/simulator/devicefile.go`). A file of simulated devices holds one
device or several as `FileDevice` — what makes the device the one it is, never its ID, engine ID or boots, so two
imports of one file are two devices with two engines no manager confuses. The export ASKS every time whether to put
the passwords in (the renderer's choice panel), writes `"secrets": "included"` or `"omitted"` into the file so nobody
passes it on believing it holds none, and writes a file holding them 0600. The import is read as a model file is —
`kind` and `formatVersion` first, then strictly — and makes every device NEW here: an ID, an engine and, when the
address it names is taken, the one `suggestAddress` gives, said in a warning. A device that cannot be made here — a
model this machine lacks, an address off loopback — is refused ON ITS OWN, and the others are kept. A file that says
its passwords were left out has any it holds anyway ignored — what a file says of itself is what is believed — and
its devices are checked by `ValidateWithoutSecrets`, everything but the secrets, and KEPT without them: starting one
fails naming what it lacks until the editor gives it. Duplicating is the same making of a new device, from a device
and with its passwords; restarting is a stop and a start, so the boots rise and coldStart goes out as on any start.

**Faults are applied to the running agent, never by a restart** (`faults.go`). A test of reachability, of the
overload guardrail or of the rate across a wrap watches the uptime and counters a restart would reset, so
`SimulatorSetFaults` goes through `Fleet.SetFaults` to the live agent, and the editor's save KEEPS the faults it does
not show. Loss and mute drop the datagram before the agent counts anything, as a lossy link would; a mute device still
sends its notifications. Latency answers through `time.AfterFunc` rather than sleeping in the one serving goroutine,
so a slow device stays slow without becoming a stuck one. An error is injected in `process`, the path v1, v2c and v3
share, and counted in the snmp group like any answer. Counters sped up run through a WARP carried by the request's
clock: the counter-seconds at the moment the speed changed, and the new speed from there — so a speed change turns a
counter faster or slower and never makes it jump or go back, either of which a manager reads as a wrap
(`TestCountersChangeSpeedWithoutJumping`). Gauges keep real time. A device's own location and contact take the place
of its model's in `addSystem`, and `AutoStart` is opt-in per device, started once the secret store is open.

**What only a notification carries** (`Object.NotifyOnly`). An iDRAC alert carries eleven objects its MIB makes
accessible-for-notify (RFC 2578 7.3) — a message ID, the message, the service tag — and no request may read them.
They are kept beside the tree rather than in it: a GET answers `noSuchObject`, a walk passes them by, and
`objectsOf` finds them for the notification that names them. One value per OID, so two notifications carrying the
same object carry the same value; the iDRAC and the PDU send one such alert each for that reason.

**Custom models** (`custom.go`). A custom model is a JSON file someone else wrote — `"kind":
"snmplens-simulator-model"`, `"formatVersion": 1` — and it is read the way a preset is: a frozen vocabulary it
PICKS from and never describes. An SMI type by name, one behaviour among those `values.go` implements (`value`,
`hex`, `uptime`, `secondsUp`, `gauge`, `counter`), a `{#}` template expanded over `instances` (`"value": "{#}"` is
the index column), notifications naming objects the model answers. A field the format does not define is REFUSED
rather than ignored, since a misspelt `vaule` would otherwise be an object with no value, reported as something
else; every bound is checked before anything is built, the expansion counted first. Then the model is BUILT ONCE,
with the snmpEngine group beside it as a v3 device's tree has, so that an OID given twice, one under another or a
value its type cannot carry is refused at import, naming the OID, rather than at the first start. Its catalogue ID
is `custom:<id>`, so a custom model can never take a built-in's ID, today's or a later one's; its engine carries
the vendor its sysObjectID names under enterprises unless it gives one. `testdata/custom-model.json` is the example
the README shows, and a test holds it answering over the wire. The registry is process-wide (`SetCustomModels`),
because a device names its model by ID and `Validate`, `NewEngineID` and a start are functions of the device alone.

`internal/app/simmodels.go` keeps the models in `simulator-models/`, a sibling like `assets/`, each under its own id —
`<id>.json` and its icon `<id>.png`, `.jpg` or `.gif` by the format the DECODER named — and never under a name the
file or the archive chose: an entry called `../../x` is read as what it holds and nothing more. Only the dialog is
bound, because a method taking a path would read any file the renderer named and quote it back in a parse error. A
ZIP is read without trusting its central directory: entries are counted, each is read through a limit, and what is
unpacked is added up as it is read (archive/zip itself stops at the size an entry declares; the limit bounds what
it may declare). An icon passes `pkg/imagegate` and a picker's bounds, 256 KB and 512 px. A lone JSON cannot bring
an icon — the one it names would have to be read from wherever the file came from — and the import says so. A model
imported again replaces the one kept and restarts its running devices, as an edited device restarts; one imported
without an icon keeps the icon it had. A model a device is made from is not deleted: the device would not start
again, and nothing would say why until somebody tried. The renderer lists models and icons on `simulator:models`
and not with the devices it polls every two seconds, since an icon crosses the bridge as a data URI.

**A device becomes a target in the renderer** (`addDeviceAsTarget` in `utils/simulator.js`): the target in the
list, and an override with its identifiers in the most secure version it answers — the ones that WRITE when it has
any, since they read as well and a device on a bench is there to be written to: its first user with `Write` for v3,
else its first, and its write community before its community. The target is the device's address, with its port unless that is 161 (`targetOf`:
`127.0.0.1:1162`), because a target is what tells devices apart and on macOS — 127.0.0.1 alone unless aliases
were added — the port is all that does. A port the target names wins over every port field, in Go
(`netaddr.SplitTarget`) and in `getEffectiveSettings`, so the override leaves it out and `TargetOverrideForm`
shows it as the target's rather than offering a field that would be ignored. The test resolves the result through
`getEffectiveSettings`, which every request is built from, with two devices sharing 127.0.0.1.
`SimulatorSuggestAddress` still offers an address of its own first, finding by binding which ones exist: a
device's notifications to this machine leave from that address, and it is what tells them apart in the trap list.

**Notifications** (`notify.go`). A device sends traps and INFORMs in v1, v2c and v3 to up to eight destinations:
when it starts (`coldStart`), when a request is refused (`authenticationFailure`), on schedules, and on request
(`SimulatorSendTrap`). A destination may be anywhere — the loopback rule is about where a device ANSWERS, and one
that could only notify its own machine could not test a collector elsewhere — so what is bounded is the RATE. A
refused request is a datagram anything on the machine can send, and uncapped, each spoofed GET would become a
notification sent to the network: `authenticationFailure` goes out about once a second at most, a device sends 20
a second (burst 40), and what the cap holds back is counted (`Suppressed`), as is what a full queue drops. One
goroutine per destination, so an INFORM waiting out its timeout holds up that receiver and no other; stopping closes
the socket under it, because gosnmp looks at its context between two attempts and not during one.

What each version carries is what an agent's does, and the tests decode it. v2c and v3 put `sysUpTime.0` and
`snmpTrapOID.0` first — gosnmp otherwise PREPENDS a sysUpTime of its own, which is the Unix time — then the
notification's objects read from the device's tree at that instant, so linkDown says what a GET would, then for a
generic notification `snmpTrapEnterprise.0` with the sysObjectID, as net-snmp sends it. v1 is RFC 3584 3.2: the
generic-trap under the sysObjectID, or enterpriseSpecific with the enterprise cut before the last arc, and before
the zero ahead of it; a notification carrying a Counter64 has no v1 form and is not sent. A notification to this
machine leaves from the device's own address (`LocalAddr`); to anywhere else, from the system's choice.

In v3 the direction decides everything. A trap is authoritative at the SENDER, so it goes as the device's own
engine — the keys the agent localised, its boots, its time — and the test opens it with keys localised to the
device's engine ID. An INFORM is authoritative at the RECEIVER: it goes with the passphrases, which gosnmp localises
to the engine ID the destination gives, or to one it discovers when none is given. SnmpLens's own listener cannot be
discovered — gosnmp's table of users drops a message naming no user before anything could answer it — so
`snmp.Client.SetTrapEngineID` gives it one, the app keeps it in `trap-engine-id` beside `monitoring.db` (RFC 3411's
fifth format under enterprise 0, IANA's reserved: SnmpLens has no number of its own and must not borrow a
vendor's), the Traps panel shows it, and the editor fills it in for "SnmpLens on this machine". gosnmp does not hold
a sender to it: an INFORM localised to any other valid engine ID is read all the same. `TestSnmpLensHearsTheSimulator`
drives that whole path, a v2c trap journalled and a v3 INFORM acknowledged.

A destination's community is a secret like the device's, kept by destination ID in the same `DeviceSecrets`, and the
ID is given in Go as a device's is. Notifications are shown by their MIB names — `linkDown`, `nsNotifyShutdown` —
which no manager translates; `tests/simulator.test.mjs` requires `en.json` to answer for every problem key and every
delivery message the renderer builds.

One trap for whoever adds a model: a file named `model_linux.go` is compiled on Linux ONLY — `_linux` is a GOOS
suffix, and the build on Windows reported the model as undefined — so the Linux model lives in `linuxserver.go`.

## Background mode

Three preferences are read by `main()` **before** `wails.Run`, so they cannot live in localStorage: they sit in `service.json` next to `monitoring.db` (`pkg/service`). `HideWindowOnClose` is deliberately NOT used — it is fixed before we know whether a tray icon actually appeared, and an app that refuses to close with no tray to quit from is unusable. `OnBeforeClose` makes the same decision later, once `tray.Start` has answered. Everything about `pkg/tray` is fail-soft for that reason, including a readiness timeout: a desktop with no StatusNotifierItem host never calls back rather than returning an error.

All three sinks accept a **CA certificate in PEM** so an
internal collector or relay can be trusted without turning verification off — the email sink previously had only
`InsecureSkipVerify`, which pushed people to the insecure setting for the exact situation that has a secure
answer. `pkg/notify` has an in-process SMTP server (`smtpserver_test.go`) so the mail path is tested as a real
conversation: implicit TLS, STARTTLS, AUTH PLAIN and LOGIN, certificate verification, and the refusal to send
credentials before the connection is encrypted.

A webhook sends either the fixed SnmpLens envelope or, with `PayloadMode: "template"`, whatever its message
template renders — which is how you talk to Slack, Teams or Alertmanager. In that mode substituted values are
escaped as JSON string fragments (`RenderJSONTemplate`) while the template's own punctuation is left alone: a trap
arrives from the network, its OID reaches the body, and one quote would otherwise turn a hand-written payload into
a different document. An empty template in that mode renders `DefaultJSONPayload` rather than the plain-text default, so the mode is
valid before anyone writes anything — falling back to prose would post it to an endpoint expecting an object.
Every field in that default is a string: a numeric one becomes `"value": ` and stops being JSON the first time an
event arrives without a value. The preview renders through the same path the sink uses, so what is on screen is
what will be POSTed, with its size and its parse result.

The rendered result is checked with `json.Valid` before it is posted, and at save time
against a sample of each event kind — a template that looks like JSON with placeholders still in it can stop being
JSON the moment one expands.

The webhook sink **does not follow redirects**, on purpose. Go rewrites a redirected POST as a GET and drops the
body, so a receiver behind a 302 answers 200 having been sent nothing — and the delivery would be recorded as
successful. Errors returned by a receiver are scrubbed of the token before they reach `notify_outbox.last_error`,
because a debug endpoint that echoes request headers would otherwise write the credential into `monitoring.db`.
A custom header value **or the URL** may contain `{{secret}}` (`notify.SecretPlaceholder`) to draw on the stored
credential: both are persisted with the configuration, so a credential typed directly into one would not be. The
URL matters most — Slack, Teams and Discord authenticate by the URL alone, so for those receivers the address IS
the credential and there is otherwise no path into `pkg/secrets` at all. Substituted BEFORE the URL is parsed, or
the braces are percent-encoded and the placeholder is requested literally.

The **outbox** is what makes a notification survive a closed window; five of its rules are load-bearing and each
one was a lost alert. **Whether to retry is decided by the reply CODE, never the text** — `*textproto.Error` for
SMTP (RFC 5321 4.2.1: 4yz transient, 5yz permanent) and a typed `HTTPStatusError` for webhooks, because the error
text ends with what the peer wrote and a 503 reading "invalid upstream" was being thrown away. An error carrying
no code is RETRIED: six attempts cost half an hour of backoff, discarding one loses the incident. Scrubbing a
credential out of an error must therefore keep the chain (`scrubbedError` overrides `Error()` and keeps
`Unwrap()`), or the code disappears with it. **A dead letter is never routed back to the sink that produced it**
(`deadLetterSink` in `internal/app/app.go`), or one unreachable collector grows the journal without bound. **A disabled or
deleted sink is never queued for**, because the dispatcher's only answer is a dead letter and a dead letter is a
MAJOR event — switching a sink off used to alarm on every event. **The drain is one goroutine per DESTINATION,
serial within one**: parallel across sinks so an unreachable relay does not hold up a healthy webhook, serial
within a sink because twenty parallel SMTP conversations is how a sender gets blocked. Each delivery is
`recover`ed — this is a background goroutine, so a panic in one sink ended the process. `Stop` waits for a
delivery on the wire but BOUNDS the wait (`StopGrace`) and stops starting new ones, because an app that will not
close is worse than an interrupted POST. Delivered rows are trimmed periodically (`OutboxRetention`); pending and
dead rows never are, since a pending row is an alert still owed and a dead letter is the only record that one was
lost — and the delivery log in the settings page is where the operator sees them.

Sinks may carry a **message template** (`pkg/notify/template.go`): `{{variable}}`, `{{variable|default}}` and
`{{#variable}}…{{/variable}}`, over a frozen vocabulary that `TemplateVariables()` also serves to the settings UI
so the two cannot drift. Deliberately NOT `text/template` — a fixed vocabulary can be listed, validated at save,
and cannot reach a field nobody chose to expose. Two rules carry the safety: substituted text is **never
re-scanned** (a trap OID reading `{{secret}}` comes out as those characters), and `RedactEvent` runs **before**
templating, because a template can name fields the built-in rendering never showed. An empty template falls back
to `renderDefault`, byte for byte.

Event text is not all ours: a trap arrives from the network unauthenticated and its trap-OID value reaches the
rendered body, so anything written into a protocol where a newline or a dot changes meaning is escaped.
Dot-stuffing (RFC5321 4.5.2) is **net/smtp's**: `client.Data()` returns a dataCloser wrapping textproto's
`DotWriter`. We only normalise line endings (`normaliseLines`) — doing it ourselves as well put two dots on the
wire and left one in the mailbox, so the dot tests drive a real connection, which is the only layer where the
property holds. `headerValue` escapes the raw mail headers, `foldAddressList` keeps `To:` inside the RFC 5322
998-octet line limit, and the RFC5424 header sanitiser does the syslog side. The mail subject relies on
`mime.QEncoding` encoding every byte below 0x20; that is standard library behaviour we depend on rather than
implement, so `injection_test.go` pins it. `capEncodedSubject` binary-searches the rune count rather than
stepping, because encoding is monotonic in runes kept and a stepping cut overshoots and is never walked back — a
non-ASCII subject over ~300 characters used to arrive as a bare ellipsis. Redaction is applied to the
event when the delivery is QUEUED, not only to the rendered text, because the webhook embeds the whole event as
JSON.

A sink has exactly one secret slot (`SinkConfig.Secret`, write-only), and each kind decides what it holds: the
SMTP password, the webhook bearer token, or the mutual-TLS **client private key** for syslog. The matching
certificate is public and stays in the config.

Session credentials follow the same rule as sink credentials: `storage.SessionConn` holds only what is safe to read in a copied `monitoring.db`, and the community and v3 passphrases go to `pkg/secrets` under `SessionRef(id)`.


## Dashboard presets

A **preset** is a JSON file somebody else wrote, bound to one equipment when that equipment is added. It says
three things: what to poll (numeric OIDs), how often, and how to show it. The first is why the format is careful
at all — a preset makes this application emit SNMP traffic to the operator's own devices, at OIDs the preset
chose.

The answer to "how do we read a stranger's description safely" is not new here. `pkg/notify/template.go` faced it
for message templates and answered with a **frozen vocabulary** rather than a language, and `pkg/preset` does the
same: a preset PICKS a widget kind from a list the package owns and cannot describe one. `WidgetKinds()` serves
that list to the UI as **i18n key suffixes** (`preset.widget.<kind>`) rather than prose, the way
`notify.VariableDoc` does — and `frontend/tests/presetkeys.test.mjs` reads the Go source and requires `en.json`
to answer for every kind and every `errf` message, with the same placeholders the `Args` map supplies. Without it
a message Go emits and no locale defines renders as the literal string `preset.err.oidTooDeep` in a dialog, under
a field path, next to real sentences: svelte-i18n falls back silently.

**OIDs are numeric, never names.** A name would have to be resolved, which means taking `pkg/mib`'s exclusive
gosmi lock to validate a FILE, and would make what a preset polls depend on which MIBs happen to be loaded.

**There is no `table` kind, and that is a correction rather than a gap.** A conceptual table is a WALK, and the
poll path a preset feeds is GET-only — `PollOIDs` → `snmp.GetMany` → `g.Get`, and a table's OID GET'd answers
`noSuchObject`. `grid` is what that want reduces to here: one widget, one cell per OID, one label map — a
switch's port panel. Which instances it has is settled BEFORE the first poll, by hand or by one walk at bind
time (see **Discovery** below), and that is what keeps the poll path GET-only and the cost knowable.

**The cost is stated over a DAY.** `MaxIntervalSec` is 86400, so an hourly base is integer-divided to zero for
every cadence past 3600 s — a third of the legal range reported "0 requests, 0 varbinds" on the one screen whose
job is to say what a preset will cost. There is no request count in `preset.Cost`: how many requests a round
takes depends on `snmp.MaxVarbindsPerGet`, which is `pkg/snmp`'s to know, so `internal/app/preset.go` computes it where
both packages are in scope.

**The bounds in `pkg/preset` are sanity bounds against a malformed file, not the guardrail.** Sixty OIDs against
a chassis on the same switch cost less than five over a satellite link, so no count predicts the cost. The
guardrail is the measured cycle time and lives in `pkg/monitor/overrun.go` — see below.

### The library, and what is allowed into it

Presets live in a **sibling** of `mibs/`, never inside it: `ListMibFiles` enumerates the MIB directory and feeds
what it finds to gosmi, so a JSON file in there would be loaded as a module and reported as a broken MIB.
`mib-backups/`, `mib-drafts/` and `mib-temp/` are siblings for the same reason.

`ImportPresetFiles` reads any absolute path the renderer names and `ReadPreset` hands back the content of
anything in the destination, so the two together are an **arbitrary-file-read primitive over the bridge** — the
same pair the MIB import gate is written about. The gate is POSITIVE rather than a blacklist: a file gets in only
if it parses as JSON and declares a `formatVersion`. A preset that fails VALIDATION still imports, with its
problem count, exactly as a MIB with a syntax error does — the error list with its field paths is what the
library is for, and you cannot fix a file you were not allowed to keep. What it cannot do is bind.

`ListPresets`, not `PresetList`: `tools/genbridge.mjs` gives anything matching `/^(List|Load|…)/` an empty ARRAY
as its screenshot fixture and everything else `null`, and `null` throws on the first `.map` in a generated file
nobody reads. For the same class of reason `Validate` returning `nil` on success is normalised to `[]` before it
crosses — otherwise the VALID path is the one that breaks.

### Discovery, and why the walk happens exactly once

The five presets that shipped first enumerated `…2.2.1.8.1` through `.24` BY HAND, and their descriptions said
"edit the instance numbers if yours does not index from 1". That is an admission rather than a note: a preset
written for a 24-port switch is a preset for exactly one shape of switch, and a library of those is not
shareable. So a widget may say WHERE its instances come from instead of listing them — `"discover": {"walk":
"1.3.6.1.2.1.2.2.1.2"}` — with `{#}` in its OIDs where the instance goes (`preset.InstancePlaceholder`).

**The walk happens at BIND TIME and nowhere else** (`internal/app/preset.go`, `discoverFor`). What is stored afterwards is
a plain preset with concrete OIDs, so the poll path stays GET-only, the cost is arithmetic before the first tick,
and `pkg/monitor`'s guardrail sees no difference at all — nothing about discovery survives into polling. One walk
per COLUMN, not per widget: a grid of port states and a chart of port counters both discover from ifDescr, and
that is one walk (`DiscoveryWalks` deduplicates).

**The walked column is the LABEL source as well as the instance source**, which is half of why it is worth its
round trip: walking ifDescr answers "which interfaces exist" and "what the equipment calls them" together, so a
discovered grid reads `Gi0/1` rather than `8`. That is `Widget.OIDLabels`, filled by `ExpandWidget`.

**A walk that fails refuses the bind.** An empty instance list expands every template to nothing, and the result
would be a session that looks bound, polls whatever scalars the preset also had, and draws empty widgets — which
is the one failure mode a monitoring tool must not have. The error names the column, because "bind failed" sends
the operator to read the preset when the problem is the community or an ACL.

**The expanded preset is validated AGAIN**, which is not belt-and-braces: the walk decides how many OIDs there
are, so a device with four hundred interfaces pushes a preset past a sanity bound its author never came close to.

**Two bounds meet on each widget and the SMALLER wins** (`discoverInstanceLimit`): the walk bound
(`MaxDiscoveredPerWidget`, 128, or the preset's own `max`), which stops a forty-thousand-row table from becoming
a dashboard; and the KIND's `MaxOIDs`, which is what the widget can actually draw. Discovering 128 states for a
grid that renders 96 is polling 32 readings nobody sees. The consequence worth knowing is that a chart is capped
at eight series by the colour plan, so a chart discovering an in and an out counter shows FOUR ports — a real
limitation, stated rather than worked around.

**`preset.Cost` is a CEILING once `Discovered` is non-zero**, and the settings screen says so
(`preset.costAtMost`). A figure presented as a count that is really an upper bound is worse than no figure.

Templates expand INSTANCE-MAJOR — every OID of instance 1, then every OID of instance 2 — which keeps a port's in
and out counters beside each other instead of interleaving twenty-four ins with twenty-four outs. Order is the
walk's, which is the agent's own order for the column: the order the ports are in on the device.

### The arrangement a preset asks for

Without a layout a preset describes what to poll and says nothing about where any of it goes, so every dashboard
is the same reflowed list of cards in declaration order. That is fine for four widgets and wrong for the thing a
preset is FOR: an author who knows the equipment knows the port wall belongs across the top and the two uplink
counters belong side by side under it, and had no way to say so.

A widget may carry `"layout": {"x": 0, "y": 1, "w": 6, "h": 2}` — zero-based cells of a **twelve-column** grid,
with spans for the width and the height. Twelve because it divides into halves, thirds and quarters, which is
what a dashboard is made of. Rows size themselves to their content, so `h` is a minimum rather than a pixel
count that a font size breaks. `h` defaults to 1: reading an omitted height as 0 would emit `span 0`, which is
invalid, and a browser answers an invalid span by dropping the whole declaration and auto-placing the widget
somewhere else entirely.

Deliberately NOT pixels and NOT a free canvas, for the reason the widget vocabulary is frozen: a coordinate
system a stranger's file can place things at exactly is one it can place things OUTSIDE, or on top of the
footnote that says where the data came from.

**Placement is per widget and optional.** A preset that places nothing keeps the arrangement it has always had —
switching every existing dashboard to a twelve-column grid to make room for a feature they do not use would
change what they look like for nothing. A preset that places SOME widgets lets the browser find room for the
rest, which is what an author hits the moment they add a widget to a laid-out preset; CSS grid's auto-placement
skips cells that are already claimed, so mixing the two cannot collide.

**Two things are refused, and both are silent otherwise.** `x + w` past the twelfth column: two legal numbers and
one widget hanging off the side, which a browser answers by growing an implicit thirteenth column so every row
below moves. And an OVERLAP: CSS grid stacks them, so the widget written second covers the one written first and
the dashboard is missing something that is right there in the file. The overlap is reported against the LATER
widget — the earlier one is where the author started, and blaming both says nothing about which to move.

**The panel places nothing inline.** `DashboardPanel.svelte` sets `--x`/`--w`/`--y`/`--h` and lets the stylesheet
do the placement, because an inline `grid-column` wins over every rule — including the media query that collapses
the dashboard to one column below 900 px, which would then silently keep its twelve columns at 320 px.
`frontend/tests/presetlayout.test.mjs` pins that, and pins the column count in its three places: `LayoutColumns`
validates against it, `presetLayout.js` converts coordinates with it, and the stylesheet draws it.

**Collapsed, the order is the author's**, top to bottom then left to right (`readingOrder`, applied as CSS
`order`). Falling back to declaration order is a different list whenever an author added a widget at the end and
placed it at the top — which is how a preset is ordinarily edited.

Every shipped preset arranges itself, and that is a test rather than a habit: these are the files somebody copies
to write their own, and the reflowing fallback looks identical to a layout that failed to load.

### What the preset thinks is too much

`PresetBind` passed `nil` where the session's thresholds go, with a comment saying a preset describes what to
WATCH and that what counts as too much is the operator's judgement about their own network. That is true of a
percentage on a link the author has never seen and false of most of what a preset is written for: a UPS running
on anything but mains, a battery reporting low, a disk at 95%. Those are properties of the MIB, and the author
knows them better than whoever binds the file.

So a widget may carry `"threshold": {"min": 30, "max": 90, "forSeconds": 300}`, and binding MATERIALISES it into
the session's own threshold map — the same column `MonitorCreateSession` fills, read by the same evaluator.
Nothing new evaluates anything; a preset fills in a form the operator would otherwise fill in by hand and can
edit afterwards. The band is per WIDGET, not per OID, because that is what makes it writeable for a discovering
preset: "no port may be anything but up" is one statement about however many ports the walk turns up.

Three facts decide the shape, and all three are read out of the code rather than assumed.

**The evaluator compares the RAW reading.** `Sample.Value` is what was polled; `Rate` is derived for the chart
and the tile and is never evaluated. So a band on a counter is a band on a number that only goes up — it fires on
the first sample and never resolves. A `rate` widget SAYS its OID is a counter, so that one kind is refused
outright; a chart of counters cannot be detected and is left to the author.

**`alertEnabled` is not "notify", it is "evaluate at all".** `classify` returns nothing without it, in
`pkg/monitor/breach.go` and in `utils/thresholdAlerts.js` alike: no episode, no journal entry, nothing. The band
is still drawn on the chart as a reference line, and that is its whole effect. Since `encoding/json` decodes an
absent bool to false, a plain `bool` would make every threshold an author writes the obvious way INERT, with the
dashboard looking exactly as though it were armed. Hence `AlertEnabled *bool` and `Alerts()`: **absent means
yes**, and false has to be written on purpose.

**A reading can carry one band.** The session holds one map keyed by OID, so two widgets watching the same OID
with different bands is a question with no answer — the later one would win silently and the earlier widget would
be drawn under a rule not applied to it. Identical bands are one band and are fine; that is the ordinary case of
a tile and a chart showing the same reading.

`Cost` counts `Watched` and `Alerting`, and the settings screen states them before binding beside the request
count. That is the same gate the traffic goes through: an evaluated band raises an incident, which the operator's
own notification rules may route to a sink, so the file reaching outside the machine is said out loud first.

Two of the five shipped presets watch something, and the three interface ones deliberately do not. `ifOperStatus`
looks like the obvious candidate and is the wrong one: most ports of a real access switch are legitimately down,
so a band there opens an episode per unused port the moment the preset is bound — and everything those presets
chart is a counter.

### The map, and why it is not SVG

The want is Zabbix's: a picture of the rack or of the site, with the ports and the links on it coloured by what
they are actually doing. SVG is the obvious answer and it is a script-execution vector — an `<svg>` carries
`<script>`, event-handler attributes, `<foreignObject>` and external references, and sanitising it means shipping
a sanitiser that has to be right forever against a format designed to be extensible. A preset is a file somebody
else wrote and this application renders it in a WebView with a bridge to the operating system on the other side.

So the same answer `pkg/notify/template.go` gave for message templates: a **frozen vocabulary**. Four shapes —
`rect`, `line`, `label`, `dot` — placed in PERCENTAGES of the widget's own box (`pkg/preset/map.go`). A preset
picks from that list, cannot describe a shape, and there is no path by which what it writes becomes markup. The
lines are drawn as an `<svg>` that `MapTile.svelte` writes, from numbers Go has already bounded.

`Map.Aspect` is the drawing's width over its height, and it is not decoration: the shapes are percentages of the
BOX, so the box's shape decides what the drawing looks like. A rack front panel is about eight to one and a site
plan about four to three, and neither survives being given the other's frame.

A shape may bind only to one of its own WIDGET's OIDs. Without that a map could name any OID it liked and this
application would look it up against a session that never polls it: a shape permanently grey, with the preset
looking correct and the cost screen never mentioning the reading it appears to show.

**The background is the operator's, never the preset's.** A preset names a FILE NAME; the file is resolved inside
a sibling `assets/` directory (`internal/app/assets.go`) and nowhere else. A preset carrying image bytes would be an
arbitrary blob this application decodes, and one carrying a PATH would be an arbitrary-file-read primitive over
the bridge — the pair the preset import gate is written about. `pkg/imagegate` is the gate, and it is a DECODE:
a file gets in only if the standard library reads it as PNG, JPEG or GIF and its dimensions are sane, with the
four things people download by mistake named rather than reported as "unknown format". `image.DecodeConfig` reads
the header only, which is what makes refusing a decompression bomb cheap — both bounds are needed, since 30000x4
passes an area check and 8000x8000 passes a per-side one. The media type served is what the DECODER said, so the
browser is never asked to sniff a data URI.

### Two rendering rules the captures found

Both were green in every test and wrong on screen, which is why the dashboard work is photographed rather than
reasoned about.

**A class attribute that is ONLY an expression replaces Svelte's scoping class.** `class={kindOf(...)}` on the
map's `<line>` compiled to a bare element, and the emitted selector is `line:where(.svelte-xxx)` — so it matched
nothing and every link was drawn with no stroke at all. `class="link {kindOf(...)}"`, with a static part, passes
the scope through. Measured in the capture: 577 pixels of stroke with a literal colour, zero with the class
missing.

**`--bg-primary`, `--bg-secondary`, `--bg-tertiary`, `--text-primary` and `--text-secondary` are defined
nowhere**, and six components were reading them. An unresolvable `var()` is invalid at computed-value time, so
the property takes its INHERITED value: a `color` inherits the text around it and a `background-color` stays
transparent, which is close enough to the intent that nobody looks twice. It stopped being cosmetic on `stroke`,
whose inherited value is `none`. The real names are in `style.css` (`--bg-color`, `--bg-light-color`,
`--bg-lighter-color`, `--text-color`, `--text-muted`), and `frontend/tests/themevars.test.mjs` now fails on a
property nothing defines — a `var()` WITH a fallback is a component saying "this may not exist" and is allowed.

### Binding, and the snapshot rule

`PresetBind` creates **one session per target**, never one session across several: the cost was stated per
equipment, the credentials are per equipment, and the overrun guardrail is per session, so one slow device must
not back off the healthy ones with it. The connection comes from that target's EFFECTIVE settings
(`getEffectiveSettings`) — its credential profile, its overrides or the defaults — never the global ones.
`startPolling` follows the same rule for a session over several equipments by splitting it, one session per
set of identifiers (see **Credential profiles**).

What to poll is **materialised into the session's own columns** (`oid`, `interval_ms`), which is what `specFor`
reads; the poll clock never opens the snapshot. A session whose layout cannot be decoded keeps polling and loses
only its dashboard — the other way round it would keep its dashboard and poll nothing, which looks correct and is
not.

`storage.SessionPreset` is a **SNAPSHOT, not a reference**. Editing a preset afterwards changes nothing already
bound, deleting it stops nothing, and rebinding is how an edit is adopted. The dashboard says so in a footnote,
because the opposite is what a reader assumes. This is `notify_outbox`'s rule — self-contained, never joining
back to what produced it — applied to the other place a stranger's file reaches durable state.

The `preset` column is added by `ensureColumn`, which logs and continues when an ALTER fails. That is right for
an optional column and fatal for a SELECT that names it anyway, so `Init` records whether the column is really
there and both the INSERT and the SELECT are built from that answer: without it a session is still created, still
listed and still polling, and simply has no layout.

### The guardrail (`pkg/monitor/overrun.go`)

A session that cannot finish a round inside its own period is not polling at the period it promises: Go's ticker
COALESCES the ticks it missed, so the loop is never idle — it finishes a round and the next is already due. The
device is asked continuously and nothing says a word.

Three rules carry it, and each is a defect in the version without it:

- **The measurement is the CYCLE**, end to end (fetch, persist, evaluate, emit), not any count declared in a file.
- **A cycle spent waiting for a device that is down is not evidence of load.** Measured against a silent agent:
  30 OIDs cost 4.0 s and 60 cost 8.0 s at the default two-second timeout with one retry — past any cadence a
  preset may declare. Backing off there would slow reachability detection at the exact moment monitoring matters,
  so a round where half the readings or more are ERRORS counts neither way. An error, not a nil value: a string
  OID answers with no number and no error at all.
- **Recovery is judged against the REQUESTED interval, never the effective one.** Once backed off every cycle
  fits the widened period trivially; judging against it recovers on the next round, overruns again, and flaps
  forever at one report per tick. Hysteresis is the other half.

The default is to widen to twice the cycle, capped at eight times the interval, and to report the edge ONCE per
episode — the same edge-triggering the pool-contention sampler in `internal/app/router.go` uses. `MonitorAcceptSlow` is
the operator saying "keep the cadence I chose": it reaches the RUNNING session rather than restarting it, because
a restart throws away the samples every delta and rate is derived from. It is deliberately not persisted.

The poll's context now reaches the wire (`snmp.GetMany` takes one and checks it between chunks, and sets gosnmp's
own `Context`). Measured: 90 OIDs against a silent device took **12.0 s** to stop and now returns in **1.0 s**,
bounded by one request timeout. That fix creates a defect of its own, fixed with it: what a cancelled round
returns is one error per OID, and persisting those breaks every series while the evaluator reads them as a device
that stopped answering — pressing Stop would have raised an unreachability alert on a healthy switch. A cancelled
round is not a measurement and is not stored.

### The dashboard tab

`DashboardPanel.svelte` draws the SNAPSHOT the session carries, never the file. Its state lives in
`stores/dashboardStore.js` and not in the component, because `{#if activeTab === …}` destroys the panel on every
tab switch — the same shell `mibEditorStore` exists because of.

Three of the six kinds are drawn by what already existed (`MetricTiles` for `value` and `rate`, `MonitorChart`
for `chart`, one sync group per session). `StatusTile` is the one new renderer, and it takes the label map from
the PRESET rather than from the MIB tree: it names the states in the author's words and works on a device whose
MIB is not loaded. A cell shows the INSTANCE, because the name is identical on every cell of a grid by
construction and was the part that survived the ellipsis. A kind this version cannot draw is SAID rather than
skipped, and `frontend/tests/dashboard.test.mjs` reads the kinds out of `pkg/preset` and requires the dispatch to
name every one — that failure has no other symptom at all.

**Several equipments are drawn together, and binding is unchanged.** `PresetBind` still creates one session per
target — the cost is per equipment, the credentials are per equipment, the overrun guardrail is per session — and
`utils/dashboardGroup.js` is a VIEW over several of them: the picker offers the group, `mergeGroup` builds one
pseudo-session in the shape every renderer already takes, and nothing about polling knows.

What may be merged is the interesting rule. The file NAME is not enough, because the snapshot rule means a
session carries the preset as it was when IT was bound: a file edited between two binds leaves two sessions
naming one file and drawing different things. The key is the file plus a signature of the widget vocabulary —
kinds, titles, units, label maps, bands, placement — and deliberately NOT the OIDs, because two switches bound
from one file having different OIDs is the case discovery exists for. A widget's OIDs are then UNIONED across the
group; a reading a given equipment does not have simply has no points, which is the shape the renderers already
handle for an OID whose first sample has not landed.

`StatusTile` is the one renderer that had to change, and it is a correctness fix rather than presentation: a cell
is one OID, and every switch bound from one preset answers `1.3.6.1.2.1.2.2.1.8.1`. Keyed by OID alone the wall
showed whichever equipment answered most recently, in one cell, with nothing saying so. `statusBlocks` splits by
target — and scopes to the WIDGET's own OIDs first, which a capture caught and the tests did not: `points` is the
whole session's results, so an equipment that had answered the uptime scalar and none of the ports counted as
"has answered", had its wall filtered down to nothing, and drew an empty card beside a full one.

`preset.Match` had no data source until `IdentifyDevice`: nothing in the application read `sysObjectID`. It
matches on that and not on `sysDescr`, which is prose that differs between two firmware revisions of the same
switch. Matching ORDERS the library and never filters it — `Match` is advice to the person binding, and hiding
the rest would turn advice into a decision. `preset.MatchDepth` compares arc by arc, never as text:
`1.3.6.1.4.1.9` is Cisco and `1.3.6.1.4.1.911` is somebody else, and `strings.HasPrefix` says they are the same
vendor.


## Releases

Tagging `v*` triggers `.github/workflows/release.yml`, which builds all three platforms and produces: Windows portable zip + NSIS installer (`build/windows/installer/project.nsi`, version string substituted at build time), macOS `.zip` + `.dmg`, Linux `.tar.gz` + `.deb`.

Four things about that workflow are security properties rather than packaging, and each answers a different
question.

**The build jobs cannot write to the repository.** `permissions: contents: read` at the workflow level;
`contents: write` is granted to the `release` job alone. The three build jobs run npm packages, Go modules and
the Wails CLI — third-party code — and a compromised one of those must not be able to rewrite the repository or
tamper with an existing release.

**Signing is required, not best-effort.** `tools/updatersign` prints a notice and exits 0 when
`UPDATER_PRIVATE_KEY` is absent, which is right for a local build and wrong in the workflow: a key that quietly
failed to reach the runner would publish an unsigned release, and the app — which has a public key embedded and
therefore ENFORCES the signature — would refuse every update from it. A broken release nobody notices until
someone tries to update. The step fails on an empty secret and asserts the `.sig` is non-empty.

**The manifest is bound to its tag.** Its first line is `version v1.2.3`, and `checkManifestVersion` in
`pkg/updater` refuses one naming a different release. The signature answers "did we sign this?" and nothing
else — an OLD manifest satisfies it just as well, so anyone able to serve what the app fetches could hand back a
previous release's manifest and binaries and have every check pass while an older, possibly vulnerable, version
is installed. A manifest with no version line is REFUSED rather than tolerated, because tolerating it is the
replay.

**Provenance is attested** (`actions/attest-build-provenance`), which answers what the Ed25519 signature cannot:
that these exact bytes were built from this repository by this workflow, rather than signed by whoever holds the
release key. Verifiable without trusting anything here:
`gh attestation verify SnmpLens-windows-amd64.exe --repo SnmpLens/SnmpLens`.

## The project site (`docs/`)

Hand-written static HTML served by GitHub Pages from `main` + `/docs`. No generator and no
build step for the pages themselves; three commands generate what they show, and all three
drive the REAL application rather than a mock-up, because a picture drawn separately drifts
from the product the first time anything changes.

```bash
node tools/screenshots.mjs          # 42 stills (21 scenes x dark/light) + their WebP
node tools/record.mjs               # 8 clips (4 x dark/light), MP4
node tools/demo.mjs                 # the browser demo, into docs/demo/
node tools/changelog-snapshot.mjs   # bake the release list into changelog.html
```

All four use `frontend/screenshots/bridge/`, a stubbed Wails bridge generated by
`tools/genbridge.mjs` from `tools/screenshot-spec.json`. Three answer sources, in order: a
scene override, a computed answer in `dynamic.js`, the static fixture — and a computed
answer returning `undefined` means "no opinion" and falls through, which is how a resolver
that knows some OIDs declines the rest. Arguments ARE forwarded: they were not, and
`GetOidDetails` answered the same fixture to all six varbinds of a trap, putting `linkDown`
in the Name column beside six plainly different OIDs.

`frontend/screenshots/scenes.js` is the catalogue: each entry is declared once and emitted
twice, dark and light. A scene may seed localStorage, override bindings, declare per-binding
`latency` (invisible to a still under virtual time; the only way a clip can film an in-flight
state), script `events`, declare a repeating `feed` (`bridge/feed.js` — what makes the
monitoring charts move), and `act` on the interface by label, CSS selector (`sel:`), typed
text (`type:`) or shortcut (`key:`). Anonymous Mode CANNOT be seeded — `settingsStore.js`
forces it off on load, deliberately — so its scene presses Ctrl+Shift+A.

Two harness rules that were bugs first. The capture waits on the FILE, not the process:
headless Chrome writes the screenshot and then, about one run in three, does not exit. And
it deletes the previous file before launching, because "wait for a file at a stable size"
is satisfied instantly by last run's file — a re-run then reported success and changed
nothing.

`tools/record.mjs` drives Chrome over the DevTools protocol (Node 22's global WebSocket, no
dependency). No `--virtual-time-budget`: it would spend the animation before the first frame.
Frames arrive only on CHANGE, so they are resampled onto a fixed grid, and the grid runs to
the moment the camera stopped rather than to the last frame — otherwise the tail held on the
end state, which is what makes a loop readable, is thrown away. H.264 only and no GIF, both
measured: on flat interface text x264 at crf 24 gave 164 KB where VP9 needed crf 48 to match
the size and 256 KB to match the quality. `--gif` remains for a README, which cannot autoplay
a video.

`docs/demo/` is the same bundle again, built by `vite.demo.config.js`, and it is committed
because Pages serves what is in the repository. What differs from the screenshot build is in
`screenshots/demo.js`: it seeds WITHOUT `localStorage.clear()` — the demo shares an origin
with the site, so clearing would throw away the visitor's theme choice — it does not pin the
language, it adopts the site's theme on first run, and it sets `__SNMPLENS_DEMO__`. That flag
is what makes the bridge REFUSE the calls that reach for the operating system (autostart, the
service, the updater, file dialogs, writing a MIB, a real test notification) instead of
answering "ok" to a request to install something. `OpenURL` is the one native call with a real
browser equivalent, so it gets one. The simulator's lists are the exception to the generated
fixtures, which answer a `List…` with an empty array and left its dialog with no model and no
device: `screenshots/demoBindings.js` answers them with the catalogue and the bench of the
scenes that photograph it. What a device does — starting, stopping, saving, the preview — is
refused, and `tests/simulator.test.mjs` requires every simulator binding to be answered or
refused, and the scenes' catalogue to be Go's, model for model.

The captured PNGs are intermediates and are NOT committed: the pages serve WebP at two widths
(`tools/webp.mjs`, `-preset text` because the photo presets smear the 1 px stems of a 12 px
monospace font). The exceptions are the logo and the 1200x630 social card, since card
renderers crop 1.6:1 from the middle — on these images, the middle of a table.

## .gitignore notes

`frontend/wailsjs/` (regenerated), `frontend/dist/` (embedded at build), `frontend/screenshots/dist/` and the generated `bridge/App.js`/`bridge/runtime.js`, `docs/assets/img/*.png` (intermediates — the WebP beside them is what the site serves), `build/bin/`, and `*.mib` (proprietary vendor MIBs are not committed — the bundled standard MIBs in `mibs/` are extension-less files like `SNMPv2-SMI`) are all ignored.
