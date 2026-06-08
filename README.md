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
`$HOME/src/tynet-infra/inventory/clients` if it exists.

`apply`-only: `--yes`, `--limit <mac>` (repeatable), `--allow-large`.
`bootstrap`-only: `--write` (without it, prints what would be written).

## Convention

Third octet of a client IP equals its VLAN ID — `10.0.70.42` is VLAN 70.
Enforced at parse time (hard fail) and re-checked at plan time against the
controller's actual `fixed_ip` (warn on drift).

## Run locally

```sh
git clone git@github.com:tya/tynet-unifi-clients.git ~/src/tynet-unifi-clients
cd ~/src/tynet-unifi-clients
make build                                                       # → bin/tynet-unifi-clients
UNIFI_API_KEY=$(op read "op://Private/radius-user-importer/password") \
  ./bin/tynet-unifi-clients plan
```

A `.env` template (gitignored) sets `UNIFI_ANSIBLE_INVENTORY` so the binary
runs cleanly from any cwd:

```sh
source .env
```

## Develop

```
make test          # go test -race ./...
make build         # → bin/tynet-unifi-clients
make lint          # vet + fmt
```

## Conventions inherited from `radius-user-importer`

- Single `package main`, no subpackages.
- Sentinel errors at package scope (`errMissingArg`, etc.).
- `var exit = os.Exit` test seam.
- `run(args []string) error` separate from `main()`.
- Tests fake the UniFi API via a `var newUnifiClient = func(...)` seam +
  a `fakeUnifiAPI` impl of the `unifiAPI` interface — no HTTP fixture.
