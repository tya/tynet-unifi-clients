# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this tool does

`tynet-unifi-clients` reconciles per-client VLAN overrides from an **Ansible
inventory** (`inventory/clients/<mac>.yml` in the sibling `tynet-infra` repo,
one file per MAC) into a **UniFi Network controller**. Inventory is the source
of truth; the controller is the target.

Four subcommands, all sharing the same fetch-and-reconcile prelude:

- `plan` — diff inventory vs controller; exit 2 on drift.
- `apply` — push inventory → controller; interactive unless `--yes`; refuses if
  >25% of clients would change unless `--allow-large`.
- `bootstrap` — seed `inventory/clients/` from the controller's current users
  (dry-run unless `--write`).
- `pull` — read-only drift report intended for a CI cron.

`UNIFI_API_KEY` is required for everything except `--help`.

## Build / test / lint

```
make test          # go test -race ./...
make test-cover    # writes coverage.out + coverage.html
make build         # → bin/tynet-unifi-clients (host arch)
make build-linux   # → ./tynet-unifi-clients   (linux/arm64, for the Pi)
make deb           # → dist/*.deb              (needs nfpm in PATH)
make lint          # go vet + go fmt
```

Run a single test: `go test -race -run TestBuild_Update ./...` (single package
— there are no subpackages, see below).

`.golangci.yml` enables `errcheck`, `govet`, `ineffassign`, `staticcheck`,
`unused`, `gofmt`, `misspell`.

## Architecture

**Single `package main`, no subpackages.** Carried over from
`radius-user-importer` along with a few other conventions:

- Sentinel errors live in `errors.go` at package scope (`errInvalidMAC`,
  `errMissingAPIKey`, etc.) so tests classify with `errors.Is` instead of
  message matching.
- `main()` calls `run(args []string) error` and translates the returned error
  to an exit code. `driftError` is a typed error that maps to exit code 2 (so
  CI cron can distinguish "drift" from "broken").
- `var exit = os.Exit`, `var stdout/stderr/stdin = os.Std*`, and
  `var newUnifiClient = func(...)` are test seams. `main_test.go`'s
  `fakeUnifiAPI` (impl of the `unifiAPI` interface in `unifi.go`) is the
  canonical stub — no `httptest` fixture; the real `newUnifiClient` body
  dials the controller, so it's only exercised on its error path.

**Reconciler is pure.** `sync.go:Build(desired, actual, networks) Plan` does no
I/O. It takes inventory clients + controller state and returns an ordered
`Plan` of `Step` values, each with an `Action` (`Noop` / `Create` / `Update` /
`SkipGuest` / `SkipUnknownNetwork`). The `plan` / `apply` / `pull` commands
share a `buildPlanFromLive` prelude that does the network fetches and hands
the results to `Build`.

**File map:**

- `main.go` — flag parsing, subcommand dispatch, the prelude, and the apply
  loop (only place that calls `CreateUser`/`UpdateUser`).
- `sync.go` — `Build`, `Step`, `Plan`, `diffUser` — the pure reconciler.
- `inventory.go` — YAML load/write for `inventory/clients/<mac>.yml`, plus
  `loadHostVarMACs` (reads `tynet-infra` host_vars to skip SSH-managed nodes
  during bootstrap).
- `unifi.go` — thin wrapper around `github.com/zoullx/unifi-go/unifi`, the
  `unifiAPI` test interface, and `guestNetworkIDs` / `networkBySubnet`.
- `macnorm.go` — `canonicalMAC` (any form → `aa:bb:cc:dd:ee:ff`) and
  `macFilename` (canonical → `aa-bb-cc-dd-ee-ff.yml`).
- `cidr.go` — `parseCIDRNetwork`; UniFi reports gateway-IP/prefix while
  inventory uses network-address/prefix, so both sides are normalized via
  `net.ParseCIDR` before comparison.
- `errors.go` — sentinel errors only.

## Load-bearing conventions

- **Third octet of a client IP equals its VLAN ID** (`10.0.70.42` → VLAN 70).
  Enforced as a hard fail at YAML parse time (`errVLANIPMismatch`), and again
  as a warning at plan time against the controller's `fixed_ip`. `bootstrap`
  drops the IP rather than emit YAML that wouldn't re-load.
- **Filename must match the MAC.** `aa-bb-cc-dd-ee-ff.yml` is enforced;
  `errFilenameMismatch` catches "edited `mac:` but forgot to rename the file"
  (and vice versa).
- **`bootstrap --write` refuses to overwrite** existing files. Human edits are
  never silently clobbered.
- **`apply` refuses >25% churn** unless `--allow-large`. Protects against an
  inventory rename storm taking down the network.
- **Guest networks (`Purpose == "guest"`) are skipped** in both directions —
  bootstrap won't import them, sync won't write them.
- **`--insecure` defaults to true.** Matches the existing `tynet-infra`
  playbook's `validate_certs: false`. Don't flip this default casually.

## Wireless caveat (real)

UniFi's `/rest/user` API accepts the per-client VLAN-override field for any
MAC, but on associate the AP only enforces it for **PPSK / WPA-Enterprise
(RADIUS) SSIDs**. Wired clients enforce; pure WPA-PSK wireless clients won't
until PPSK/RADIUS lands. This is documented in the README and is the reason
the tool surfaces `Connection: wired | wireless | ""` but doesn't gate on it.

## Release flow (in case you touch it)

Tag-driven. `Makefile` derives the Debian version from `git describe
--tags --dirty` with `-` → `~` to keep it a valid Debian version.
`.github/workflows/release.yml` fires on `v*` tags:

1. Cross-compile linux/arm64.
2. `nfpm package` using `packaging/nfpm.yaml` → `dist/*.deb`.
3. Publish a GitHub Release with the deb attached.
4. Dispatch a `new-release` repo-event to `tya/tynet-apt`, which regenerates
   `Packages.gz`, signs `Release`/`InRelease` (GPG `47AA8444945F450A`), and
   commits to `gh-pages`. Pages serves it within a couple of minutes.

`APT_DISPATCH_TOKEN` is the required secret — a PAT with `repo` scope on
`tya/tynet-apt`, the same one other `tynet-*` repos use to dispatch ingest
events.

For an off-cycle Pi test build:

```
make deb VERSION=0.1.0-dev
scp dist/tynet-unifi-clients_0.1.0-dev_arm64.deb pi3.tynet.us:/tmp/
ssh pi3.tynet.us "sudo dpkg -i /tmp/tynet-unifi-clients_*.deb"
```
