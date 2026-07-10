// Command tynet-unifi-clients syncs per-client VLAN overrides from Ansible
// inventory (the source of truth) to a UniFi Network controller. Inventory
// lives at inventory/clients/<mac>.yml in the tynet-infra repo, one YAML
// file per client keyed by MAC address.
//
// Usage:
//
//	UNIFI_API_KEY=<key> tynet-unifi-clients <subcommand> [flags]
//
// Subcommands:
//
//	plan       Diff inventory vs controller; exit 2 if drift.
//	apply      Push inventory -> controller. Prompts unless --yes.
//	bootstrap  Seed inventory/clients/ from the controller's client list.
//	pull       Read-only drift report suitable for CI cron.
//
// See README.md for the full surface.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zoullx/unifi-go/unifi"
)

// exit is a variable so tests can replace os.Exit to capture the exit code.
// Same pattern as radius-user-importer.
var exit = os.Exit

// stdout/stderr/stdin are variables so tests can capture output and feed
// scripted input. Typed as interfaces (not *os.File) so a strings.Builder /
// strings.Reader works.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	stdin  io.Reader = os.Stdin
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(stderr, err)
		// Distinguish "drift detected" (2) from "real error" (1).
		var de driftError
		if errors.As(err, &de) {
			exit(2)
			return
		}
		exit(1)
	}
}

// driftError is returned by plan/pull when the only "error" is that the
// controller diverges from inventory. main() maps this to exit code 2.
type driftError struct{ summary string }

func (d driftError) Error() string { return d.summary }

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("%w: (none); pass plan|apply|bootstrap|pull", errUnknownSubcommand)
	}
	switch args[0] {
	case "plan":
		return runPlan(args[1:])
	case "apply":
		return runApply(args[1:])
	case "bootstrap":
		return runBootstrap(args[1:])
	case "pull":
		return runPull(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("%w: %q", errUnknownSubcommand, args[0])
	}
}

func printUsage() {
	fmt.Fprint(stderr, `Usage: tynet-unifi-clients <subcommand> [flags]

Subcommands:
  plan       Diff inventory vs controller; exit 2 if drift.
  apply      Push inventory -> controller. Prompts unless --yes.
  bootstrap  Seed inventory/clients/ from the controller's client list.
  pull       Read-only drift report suitable for CI cron.

Common flags:
  --inventory-path PATH   Directory holding clients/<mac>.yml; default: $UNIFI_ANSIBLE_INVENTORY,
                          or walk up from cwd to find ansible.cfg's sibling
                          inventory/clients dir, or
                          $HOME/src/tynet-infra/inventory/clients if it exists.
  --unifi-base URL        UniFi controller base URL (no /api suffix).
                          Default: https://unifi.tynet.us
  --insecure              Skip TLS verification. Default: true (matches the
                          existing playbook's validate_certs: false).

Environment:
  UNIFI_API_KEY           Required for all subcommands except --help.
`)
}

// commonFlags returns a FlagSet with the flags every subcommand shares.
// Returns the parsed values via closure-captured pointers.
type common struct {
	inventoryPath string
	unifiBase     string
	insecure      bool
}

func registerCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.inventoryPath, "inventory-path", "", "directory holding clients/<mac>.yml (default: $UNIFI_ANSIBLE_INVENTORY or auto-discover)")
	fs.StringVar(&c.unifiBase, "unifi-base", "https://unifi.tynet.us", "UniFi controller base URL")
	fs.BoolVar(&c.insecure, "insecure", true, "skip TLS verification (matches the existing playbook)")
	return c
}

// devDefaultInventory is the last-resort path tried during local development
// when no flag, env var, or walk-up discovery finds an inventory. Surfaced
// only if the directory actually exists, so production hosts (where
// tynet-infra is not checked out under $HOME) still error cleanly and force
// the operator to set the env var via Ansible / /etc/default.
const devDefaultInventory = "src/tynet-infra/inventory/clients"

// resolveInventoryPath returns the final inventory directory to use.
// Precedence: --inventory-path flag → $UNIFI_ANSIBLE_INVENTORY env →
// walk up from cwd looking for ansible.cfg + inventory/clients sibling →
// $HOME/src/tynet-infra/inventory/clients if it exists (dev convenience).
func (c *common) resolveInventoryPath() (string, error) {
	if c.inventoryPath != "" {
		return c.inventoryPath, nil
	}
	if env := os.Getenv("UNIFI_ANSIBLE_INVENTORY"); env != "" {
		return env, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "ansible.cfg")); err == nil {
			candidate := filepath.Join(dir, "inventory", "clients")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, devDefaultInventory)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: pass --inventory-path or set UNIFI_ANSIBLE_INVENTORY", errInventoryNotFound)
}

// requireAPIKey reads UNIFI_API_KEY or fails with a clear error.
func requireAPIKey() (string, error) {
	k := os.Getenv("UNIFI_API_KEY")
	if k == "" {
		return "", errMissingAPIKey
	}
	return k, nil
}

func runPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	plan, _, err := buildPlanFromLive(c)
	if err != nil {
		return err
	}
	plan.Render(stdout)
	fmt.Fprintln(stdout, plan.Summary(false))
	if plan.HasDrift() {
		return driftError{summary: "drift detected (re-run with `apply` to reconcile)"}
	}
	return nil
}

func runApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	c := registerCommon(fs)
	yes := fs.Bool("yes", false, "skip the interactive confirmation prompt")
	allowLarge := fs.Bool("allow-large", false, "allow applying changes to more than 25% of clients")
	var limits stringSlice
	fs.Var(&limits, "limit", "limit to this MAC (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	plan, api, err := buildPlanFromLive(c)
	if err != nil {
		return err
	}
	if len(limits) > 0 {
		plan = filterPlan(plan, limits)
	}
	plan.Render(stdout)
	fmt.Fprintln(stdout, plan.Summary(false))

	if !plan.HasDrift() {
		fmt.Fprintln(stdout, "nothing to apply")
		return nil
	}

	// Hard cap: refuse to write if a single run would change more than 25%
	// of known clients unless explicitly allowed. Protects against an
	// inventory rename storm taking down the network.
	_, create, update, _ := plan.Counts()
	changing := create + update
	total := len(plan.Steps)
	if total > 0 && !*allowLarge && changing*100 > 25*total {
		return fmt.Errorf("%w: %d of %d would change", errAllowLargeRequired, changing, total)
	}

	if !*yes {
		fmt.Fprintf(stdout, "Apply %d changes? [y/N] ", changing)
		r := bufio.NewReader(stdin)
		line, _ := r.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			fmt.Fprintln(stdout, "aborted")
			return nil
		}
	}

	ctx := context.Background()
	for _, s := range plan.Steps {
		switch s.Action {
		case Create:
			d := s.Desired
			if _, err := api.CreateUser(ctx, unifiSite, &d); err != nil {
				return fmt.Errorf("create %s: %w", s.MAC, err)
			}
			fmt.Fprintf(stdout, "  created %s\n", s.MAC)
		case Update:
			d := s.Desired
			d.ID = s.Existing.ID
			if _, err := api.UpdateUser(ctx, unifiSite, &d); err != nil {
				return fmt.Errorf("update %s: %w", s.MAC, err)
			}
			fmt.Fprintf(stdout, "  updated %s\n", s.MAC)
		}
	}
	fmt.Fprintln(stdout, plan.Summary(true))
	return nil
}

func runBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	c := registerCommon(fs)
	write := fs.Bool("write", false, "actually write inventory/clients/<mac>.yml files (default: dry-run)")
	hostVarsDir := fs.String("host-vars-dir", "", "tynet-infra inventory/host_vars/ (to skip SSH-managed MACs); empty disables the skip")
	groupVarsAll := fs.String("group-vars-all", "", "tynet-infra inventory/group_vars/all.yml (for kickstart_mac); empty disables the skip")
	verbose := fs.Bool("verbose", false, "list every skipped MAC by bucket (guest/managed/no-vlan-signal) on stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	apiKey, err := requireAPIKey()
	if err != nil {
		return err
	}
	api, err := newUnifiClient(c.unifiBase, apiKey, c.insecure)
	if err != nil {
		return err
	}

	ctx := context.Background()
	networks, err := api.ListNetwork(ctx, unifiSite)
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	users, err := api.ListUser(ctx, unifiSite)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	guestIDs := guestNetworkIDs(networks)
	netByID := map[string]*unifi.Network{}
	for i := range networks {
		netByID[networks[i].ID] = &networks[i]
	}

	skipMACs := map[string]struct{}{}
	if *hostVarsDir != "" {
		skipMACs, err = loadHostVarMACs(*hostVarsDir, *groupVarsAll)
		if err != nil {
			return fmt.Errorf("load host_vars MACs: %w", err)
		}
	}

	dir, err := c.resolveInventoryPath()
	if err != nil && !*write {
		// Without --write we don't strictly need the dir, but we want to
		// show where files would land. Print the error as a note and
		// continue with a placeholder.
		fmt.Fprintf(stderr, "note: %v (dry-run continuing; files would land in inventory/clients/)\n", err)
		dir = "inventory/clients"
	} else if err != nil {
		return err
	}

	if *write {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	candidates, skipped := bootstrapCandidates(users, netByID, guestIDs, skipMACs)

	fmt.Fprintf(stderr, "controller has %d users; skipping %d (%d guest, %d host_vars-managed, %d no-vlan-signal)\n",
		len(users), skipped.total, skipped.guest, skipped.managed, skipped.noVLAN)
	if *verbose {
		printSkippedMACs(stderr, "guest", skipped.guestMACs)
		printSkippedMACs(stderr, "host_vars-managed", skipped.managedMACs)
		printSkippedMACs(stderr, "no-vlan-signal", skipped.noVLANMACs)
	}

	if len(candidates) == 0 {
		fmt.Fprintln(stdout, "no new clients to bootstrap")
		return nil
	}

	for _, c := range candidates {
		path := filepath.Join(dir, macFilename(c.MAC))
		if *write {
			written, werr := writeClientFile(dir, c)
			if werr != nil {
				fmt.Fprintf(stderr, "  skip %s: %v\n", c.MAC, werr)
				continue
			}
			fmt.Fprintf(stdout, "  wrote %s\n", written)
		} else {
			fmt.Fprintf(stdout, "  would write %s  (name=%q vlan=%d ip=%q)\n",
				path, c.Name, c.VLANID, c.IP)
		}
	}
	if !*write {
		fmt.Fprintln(stdout, "(dry-run: re-run with --write to actually create files)")
	}
	return nil
}

type bootstrapSkipped struct {
	total, guest, managed, noVLAN      int
	guestMACs, managedMACs, noVLANMACs []string
}

// printSkippedMACs emits the MACs in one skip bucket to w, one per line,
// prefixed with the bucket label so output stays grep-friendly. No-op when
// the slice is empty.
func printSkippedMACs(w io.Writer, label string, macs []string) {
	if len(macs) == 0 {
		return
	}
	sort.Strings(macs)
	for _, m := range macs {
		fmt.Fprintf(w, "  skip-%s %s\n", label, m)
	}
}

// bootstrapCandidates turns the controller's raw user list into Client
// records suitable for writing to inventory/clients/.
//
// VLAN derivation, in order:
//  1. Network.VLAN from the user's network_id (when both are set and VLAN > 0)
//  2. third octet of FixedIP (per "third octet = VLAN" convention)
//  3. third octet of observed IP
//
// Why the fallback exists: UniFi accumulates stale client records whose
// network_id is null (legacy entries, wifi-only clients that never had a
// fixed network profile). They have an observed IP from their last
// association, which under our convention is enough to recover the VLAN.
//
// Skip rules:
//   - invalid MAC → silent (never counted; usually decommissioned vendor
//     garbage that doesn't deserve attention)
//   - host_vars-managed MAC → s.managed (pi2/pi3/kickstart, owned by the
//     existing unifi-reservations.yml playbook)
//   - on a network whose purpose is "guest" → s.guest
//   - derived VLAN matches a guest network's VLAN → s.guest (catches stale
//     clients whose last-seen IP was on guest, even though their
//     network_id is null)
//   - no VLAN signal from any source → s.noVLAN (rare; would mean no
//     network, no FixedIP, no observed IP)
func bootstrapCandidates(users []unifi.User, netByID map[string]*unifi.Network, guestIDs map[string]struct{}, skipMACs map[string]struct{}) ([]Client, bootstrapSkipped) {
	// Build the set of VLAN IDs assigned to guest networks so we can also
	// reject by derived-VLAN, not just by network_id membership.
	guestVLANs := map[int]struct{}{}
	for _, n := range netByID {
		if _, isGuest := guestIDs[n.ID]; isGuest && n.VLAN > 0 {
			guestVLANs[n.VLAN] = struct{}{}
		}
	}

	var out []Client
	var s bootstrapSkipped
	for _, u := range users {
		mac, err := canonicalMAC(u.MAC)
		if err != nil {
			continue
		}
		if _, managed := skipMACs[mac]; managed {
			s.total++
			s.managed++
			s.managedMACs = append(s.managedMACs, mac)
			continue
		}
		if _, isGuest := guestIDs[u.NetworkID]; isGuest {
			s.total++
			s.guest++
			s.guestMACs = append(s.guestMACs, mac)
			continue
		}

		vlanID := 0
		if net, ok := netByID[u.NetworkID]; ok {
			vlanID = net.VLAN
		}
		if vlanID == 0 {
			if v, ok := vlanFromIP(u.FixedIP); ok {
				vlanID = v
			} else if v, ok := vlanFromIP(u.IP); ok {
				vlanID = v
			}
		}
		if vlanID == 0 {
			s.total++
			s.noVLAN++
			s.noVLANMACs = append(s.noVLANMACs, mac)
			continue
		}
		// After derivation, also reject if the derived VLAN matches a
		// guest network's VLAN — covers stale clients whose network_id is
		// null but whose last-seen IP is on the guest subnet.
		if _, isGuest := guestVLANs[vlanID]; isGuest {
			s.total++
			s.guest++
			s.guestMACs = append(s.guestMACs, mac)
			continue
		}

		name := u.Name
		if name == "" {
			name = u.Hostname
		}
		ip := u.FixedIP
		// Only set ip if it agrees with the derived VLAN's third octet,
		// otherwise the YAML would fail loadClients validation. Let the
		// human fill in a fixed IP later if they want one.
		if ip != "" && !ipMatchesVLAN(ip, vlanID) {
			ip = ""
		}
		out = append(out, Client{
			MAC:    mac,
			Name:   name,
			IP:     ip,
			VLANID: vlanID,
		})
	}
	return out, s
}

// vlanFromIP extracts a VLAN ID from the third octet of a dotted-quad IPv4
// address per the "third octet = VLAN" convention. Returns (0, false) for
// anything unparseable or out of the [1, 4094] range.
func vlanFromIP(ip string) (int, bool) {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var third int
	if _, err := fmt.Sscanf(parts[2], "%d", &third); err != nil {
		return 0, false
	}
	if third < 1 || third > 4094 {
		return 0, false
	}
	return third, true
}

func ipMatchesVLAN(ip string, vlan int) bool {
	v, ok := vlanFromIP(ip)
	return ok && v == vlan
}

func runPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	apiKey, err := requireAPIKey()
	if err != nil {
		return err
	}
	api, err := newUnifiClient(c.unifiBase, apiKey, c.insecure)
	if err != nil {
		return err
	}

	dir, err := c.resolveInventoryPath()
	if err != nil {
		return err
	}
	clients, err := loadClients(dir)
	if err != nil {
		return err
	}

	ctx := context.Background()
	networks, err := api.ListNetwork(ctx, unifiSite)
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	users, err := api.ListUser(ctx, unifiSite)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	// Build sets for set-difference.
	invSet := map[string]struct{}{}
	for _, c := range clients {
		invSet[c.MAC] = struct{}{}
	}
	guestIDs := guestNetworkIDs(networks)
	ctrlSet := map[string]struct{}{}
	for _, u := range users {
		canon, err := canonicalMAC(u.MAC)
		if err != nil {
			continue
		}
		if _, isGuest := guestIDs[u.NetworkID]; isGuest {
			continue
		}
		ctrlSet[canon] = struct{}{}
	}

	var invOnly, ctrlOnly []string
	for m := range invSet {
		if _, ok := ctrlSet[m]; !ok {
			invOnly = append(invOnly, m)
		}
	}
	for m := range ctrlSet {
		if _, ok := invSet[m]; !ok {
			ctrlOnly = append(ctrlOnly, m)
		}
	}

	for _, m := range invOnly {
		fmt.Fprintf(stdout, "inventory-only: %s\n", m)
	}
	for _, m := range ctrlOnly {
		fmt.Fprintf(stdout, "controller-only: %s\n", m)
	}

	plan := Build(clients, users, networks)
	fmt.Fprintln(stdout, plan.Summary(false))

	if len(invOnly)+len(ctrlOnly) > 0 || plan.HasDrift() {
		return driftError{summary: fmt.Sprintf("drift: %d inv-only, %d ctrl-only, %t plan-drift",
			len(invOnly), len(ctrlOnly), plan.HasDrift())}
	}
	return nil
}

// buildPlanFromLive is the shared "fetch + reconcile" prelude used by plan
// and apply. Returns the plan plus the live API client (apply needs it for
// writes).
func buildPlanFromLive(c *common) (Plan, unifiAPI, error) {
	apiKey, err := requireAPIKey()
	if err != nil {
		return Plan{}, nil, err
	}
	api, err := newUnifiClient(c.unifiBase, apiKey, c.insecure)
	if err != nil {
		return Plan{}, nil, err
	}
	dir, err := c.resolveInventoryPath()
	if err != nil {
		return Plan{}, nil, err
	}
	clients, err := loadClients(dir)
	if err != nil {
		return Plan{}, nil, err
	}

	ctx := context.Background()
	networks, err := api.ListNetwork(ctx, unifiSite)
	if err != nil {
		return Plan{}, nil, fmt.Errorf("list networks: %w", err)
	}
	users, err := api.ListUser(ctx, unifiSite)
	if err != nil {
		return Plan{}, nil, fmt.Errorf("list users: %w", err)
	}
	return Build(clients, users, networks), api, nil
}

// filterPlan returns a new Plan containing only steps whose MAC is in limits.
// Used by apply --limit.
func filterPlan(p Plan, limits []string) Plan {
	want := map[string]struct{}{}
	for _, m := range limits {
		canon, err := canonicalMAC(m)
		if err != nil {
			continue
		}
		want[canon] = struct{}{}
	}
	var out []Step
	for _, s := range p.Steps {
		if _, ok := want[s.MAC]; ok {
			out = append(out, s)
		}
	}
	return Plan{Steps: out}
}

// stringSlice is a flag.Value backing for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }
