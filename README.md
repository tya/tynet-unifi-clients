# tynet-unifi-clients

Syncs per-client VLAN overrides from Ansible inventory (the source of truth) to
a UniFi Network controller. Inventory lives at
`inventory/clients/<mac>.yml` in the `tynet-infra` repo, one YAML file per
client keyed by MAC address.

## Caveat: wireless enforcement

UniFi's `/rest/user` API accepts the per-client VLAN override field for any MAC,
but on associate the AP only honors it for SSIDs configured with PPSK or
WPA-Enterprise (RADIUS). **Wired clients via UniFi switches will enforce; pure
WPA-PSK wireless clients may not** until a follow-up PPSK or RADIUS rollout.

## Usage

```
UNIFI_API_KEY=... tynet-unifi-clients <subcommand>

  plan       Diff inventory vs controller; exit 2 if drift.  (default)
  apply      Push inventory → controller.  Prompts unless --yes.
  bootstrap  Seed inventory/clients/ from the controller's current client list.
  pull       Read-only drift report suitable for CI cron.
```

Common flags: `--inventory-path PATH`, `--unifi-base URL` (default
`https://unifi.tynet.us`), `--insecure` (default true, matches `tynet-infra`
playbook's `validate_certs: false`).

If `--inventory-path` and `$UNIFI_ANSIBLE_INVENTORY` are both unset, the tool
walks up from `cwd` looking for an `ansible.cfg` whose sibling
`inventory/clients/` exists, then falls back to
`$HOME/src/tynet-infra/inventory/clients` if it exists — so local development
out of `~/src/tynet-unifi-clients` works without extra config. Production
hosts (where tynet-infra isn't checked out under `$HOME`) skip the fallback
and require the env var, which `roles/nodes` sets via Ansible.

`apply`-only: `--yes`, `--limit <mac>` (repeatable), `--allow-large`.
`bootstrap`-only: `--write` (without it, prints what would be written).

## Convention

Third octet of a client IP equals its VLAN ID — `10.0.70.42` is VLAN 70.
Enforced at parse time (hard fail) and re-checked at plan time against the
controller's actual `fixed_ip` (warn on drift).

## Install on a Pi (Ubuntu 26.04, arm64)

Packaged as a `.deb` in [`tya/tynet-apt`](https://github.com/tya/tynet-apt) and
served at `https://tya.github.io/tynet-apt`. Any host with the tynet-apt source
configured (already wired by `roles/nodes` in `tya/tynet-infra`) can:

```sh
sudo apt update
sudo apt install tynet-unifi-clients
```

The binary lands at `/usr/bin/tynet-unifi-clients`. Provide `UNIFI_API_KEY` via
env (the cycle-node setup on `kickstart.tynet.us` already sources
`/home/ty/.config/tynet/credentials` for this).

## Develop

```
make test          # go test -race ./...
make build         # → bin/tynet-unifi-clients     (host arch)
make build-linux   # → ./tynet-unifi-clients       (linux/arm64, for Pi)
make deb           # → dist/*.deb                  (needs nfpm)
make lint          # vet + fmt
```

One-off install on a Pi without the apt source (e.g. for testing a branch):

```sh
make deb VERSION=0.1.0-dev
scp dist/tynet-unifi-clients_0.1.0-dev_arm64.deb pi3.tynet.us:/tmp/
ssh pi3.tynet.us "sudo dpkg -i /tmp/tynet-unifi-clients_*.deb"
```

## Release

Versions are git tags; `Makefile` derives the Debian version from
`git describe --tags --dirty` (with `-` → `~` for Debian-version validity).

To cut a release:

```sh
git switch main
git pull
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` triggers on `v*` tags. It:

1. Cross-compiles for `linux/arm64`.
2. Builds the `.deb` with `nfpm` (config at `packaging/nfpm.yaml`).
3. Publishes a GitHub Release with the `.deb` as an asset.
4. Dispatches a `new-release` event to `tya/tynet-apt`, which regenerates
   `Packages.gz`, signs `Release` / `InRelease` (GPG `47AA8444945F450A`), and
   commits to the `gh-pages` branch. Served by Pages within a couple of
   minutes; `apt-get install tynet-unifi-clients` on any tynet-apt-enabled
   host gets the new version.

### Required secret

`APT_DISPATCH_TOKEN` — a PAT with `repo` scope for `tya/tynet-apt`. Same value
the other tynet-* repos use to dispatch ingest events. Set with:

```sh
gh secret set APT_DISPATCH_TOKEN -R tya/tynet-unifi-clients
```

## Conventions inherited from `radius-user-importer`

- Single `package main`, no subpackages.
- Sentinel errors at package scope (`errMissingArg`, etc.).
- `var exit = os.Exit` test seam.
- `run(args []string) error` separate from `main()`.
- Tests use `httptest.NewTLSServer`.
