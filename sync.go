package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zoullx/unifi-go/unifi"
)

// Action enumerates the per-MAC outcomes the reconciler produces.
type Action int

const (
	Noop Action = iota
	Create
	Update
	SkipGuest          // inventory client lives on a guest network -- never touch
	SkipUnknownNetwork // desired vlan_id doesn't resolve to any controller network
)

func (a Action) String() string {
	switch a {
	case Noop:
		return "noop"
	case Create:
		return "create"
	case Update:
		return "update"
	case SkipGuest:
		return "skip-guest"
	case SkipUnknownNetwork:
		return "skip-unknown-network"
	}
	return fmt.Sprintf("Action(%d)", a)
}

// Step is one MAC's reconciliation outcome.
type Step struct {
	MAC      string
	Action   Action
	Existing *unifi.User // nil for pure creates
	Desired  unifi.User  // what the reconciler wants to write
	Reasons  []string    // human-readable diff lines for Update; empty otherwise
	Warnings []string    // non-fatal observations (e.g. IP-vs-VLAN drift in controller state)
}

// Plan is the ordered output of Build. Steps are sorted by MAC for stable
// diffs across runs.
type Plan struct {
	Steps []Step
}

// Counts summarizes how many steps fall into each action class. Order
// matches the playbook's summary line for visual parity.
func (p Plan) Counts() (noop, create, update, skip int) {
	for _, s := range p.Steps {
		switch s.Action {
		case Noop:
			noop++
		case Create:
			create++
		case Update:
			update++
		case SkipGuest, SkipUnknownNetwork:
			skip++
		}
	}
	return
}

// HasDrift returns true if any step would change controller state.
func (p Plan) HasDrift() bool {
	_, create, update, _ := p.Counts()
	return create+update > 0
}

// Build is the pure reconciler. Given the desired inventory, the controller's
// current users, and the controller's networks, it returns an ordered Plan.
// No I/O.
func Build(desired []Client, actual []unifi.User, networks []unifi.Network) Plan {
	// Index networks by VLAN ID and by network ID so per-client lookups
	// are O(1).
	netByVLAN := map[int]*unifi.Network{}
	for i := range networks {
		n := &networks[i]
		if n.VLAN > 0 {
			netByVLAN[n.VLAN] = n
		}
	}
	guestIDs := guestNetworkIDs(networks)

	// Index existing users by canonical MAC; tolerate the (rare) case of
	// the same MAC appearing twice in the controller's user list by taking
	// the first occurrence.
	existing := map[string]*unifi.User{}
	for i := range actual {
		canon, err := canonicalMAC(actual[i].MAC)
		if err != nil {
			continue
		}
		if _, dup := existing[canon]; dup {
			continue
		}
		existing[canon] = &actual[i]
	}

	var steps []Step
	for _, c := range desired {
		s := stepFor(c, existing, netByVLAN, guestIDs)
		steps = append(steps, s)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].MAC < steps[j].MAC })
	return Plan{Steps: steps}
}

func stepFor(c Client, existing map[string]*unifi.User, netByVLAN map[int]*unifi.Network, guestIDs map[string]struct{}) Step {
	net, ok := netByVLAN[c.VLANID]
	if !ok {
		return Step{
			MAC:     c.MAC,
			Action:  SkipUnknownNetwork,
			Reasons: []string{fmt.Sprintf("no controller network with vlan=%d", c.VLANID)},
		}
	}
	if _, isGuest := guestIDs[net.ID]; isGuest {
		return Step{
			MAC:     c.MAC,
			Action:  SkipGuest,
			Reasons: []string{fmt.Sprintf("vlan %d resolves to guest network %q", c.VLANID, net.Name)},
		}
	}

	desired := unifi.User{
		MAC:                           c.MAC,
		Name:                          c.Name,
		NetworkID:                     net.ID, // primary "home" network
		VirtualNetworkOverrideEnabled: true,
		VirtualNetworkOverrideID:      net.ID, // the per-client VLAN-override target
	}
	if c.IP != "" {
		desired.FixedIP = c.IP
		desired.UseFixedIP = true
	}
	if c.Notes != "" {
		desired.Note = c.Notes
	}

	cur, found := existing[c.MAC]
	if !found {
		return Step{
			MAC:     c.MAC,
			Action:  Create,
			Desired: desired,
		}
	}

	reasons := diffUser(cur, &desired)
	step := Step{
		MAC:      c.MAC,
		Existing: cur,
		Desired:  desired,
	}

	// IP-vs-VLAN drift in controller state (not inventory state) is a
	// warning, not an error: inventory could be right, controller could
	// have a stale fixed_ip. We surface it so the human notices.
	if cur.FixedIP != "" && c.IP != "" && cur.FixedIP != c.IP {
		step.Warnings = append(step.Warnings,
			fmt.Sprintf("controller fixed_ip=%s differs from inventory ip=%s", cur.FixedIP, c.IP))
	}

	if len(reasons) == 0 {
		step.Action = Noop
	} else {
		step.Action = Update
		step.Reasons = reasons
	}
	return step
}

// diffUser returns one human-readable line per field that differs between cur
// and desired. Empty slice means the controller is already in the desired
// state (modulo fields we don't manage like LastSeen, Hostname, IP-observed,
// SiteID).
func diffUser(cur, desired *unifi.User) []string {
	var out []string
	if cur.Name != desired.Name {
		out = append(out, fmt.Sprintf("name: %q → %q", cur.Name, desired.Name))
	}
	if cur.NetworkID != desired.NetworkID {
		out = append(out, fmt.Sprintf("network_id: %q → %q", cur.NetworkID, desired.NetworkID))
	}
	if cur.VirtualNetworkOverrideEnabled != desired.VirtualNetworkOverrideEnabled {
		out = append(out, fmt.Sprintf("virtual_network_override_enabled: %t → %t",
			cur.VirtualNetworkOverrideEnabled, desired.VirtualNetworkOverrideEnabled))
	}
	if cur.VirtualNetworkOverrideID != desired.VirtualNetworkOverrideID {
		out = append(out, fmt.Sprintf("virtual_network_override_id: %q → %q",
			cur.VirtualNetworkOverrideID, desired.VirtualNetworkOverrideID))
	}
	if desired.UseFixedIP {
		if cur.FixedIP != desired.FixedIP {
			out = append(out, fmt.Sprintf("fixed_ip: %q → %q", cur.FixedIP, desired.FixedIP))
		}
		if cur.UseFixedIP != desired.UseFixedIP {
			out = append(out, fmt.Sprintf("use_fixedip: %t → %t", cur.UseFixedIP, desired.UseFixedIP))
		}
	}
	if desired.Note != "" && cur.Note != desired.Note {
		out = append(out, fmt.Sprintf("note: %q → %q", cur.Note, desired.Note))
	}
	return out
}

// Render writes a human-readable plan to w. Format mirrors the existing
// unifi-reservations.yml summary shape so the two tools feel familiar.
func (p Plan) Render(w io.Writer) {
	for _, s := range p.Steps {
		fmt.Fprintf(w, "%s (%s): %s\n", s.MAC, s.Desired.Name, s.Action)
		for _, r := range s.Reasons {
			fmt.Fprintf(w, "  → %s\n", r)
		}
		for _, warn := range s.Warnings {
			fmt.Fprintf(w, "  ! %s\n", warn)
		}
	}
}

// Summary returns the one-line "N ok, X would update, Y would create, Z skip"
// shape used at the bottom of plan/apply output.
func (p Plan) Summary(applied bool) string {
	noop, create, update, skip := p.Counts()
	if applied {
		return fmt.Sprintf("%d ok, %d updated, %d created, %d skipped", noop, update, create, skip)
	}
	parts := []string{
		fmt.Sprintf("%d ok", noop),
		fmt.Sprintf("%d would update", update),
		fmt.Sprintf("%d would create", create),
		fmt.Sprintf("%d skipped", skip),
	}
	return strings.Join(parts, ", ")
}
