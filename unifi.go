package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zoullx/unifi-go/unifi"
)

// unifiSite is the controller site we operate against. tynet uses the default
// site ("default"). If we ever multi-site this becomes a flag.
const unifiSite = "default"

// unifiAPI is the small subset of github.com/zoullx/unifi-go/unifi.Client that
// this tool uses. Keeping it an interface lets the reconciler tests swap in a
// fake without spinning a real HTTP server.
type unifiAPI interface {
	ListNetwork(ctx context.Context, site string) ([]unifi.Network, error)
	ListUser(ctx context.Context, site string) ([]unifi.User, error)
	CreateUser(ctx context.Context, site string, d *unifi.User) (*unifi.User, error)
	// Get and Put are the SDK's generic REST escape hatches. Used directly
	// (rather than typed wrappers) in two places where the typed path
	// silently drops data:
	//  - Get, by observedIP: /stat/user/<mac> carries a last_ip field that
	//    survives disconnection, but the SDK's typed unifi.User struct has
	//    no field for it, so going through e.g. GetUserByMAC silently
	//    drops it for any currently-offline client.
	//  - Put, by updateUser: User.Name, User.FixedIP, and User.Note are all
	//    tagged `omitempty`, so unifi.Client's typed UpdateUser can never
	//    send an explicit empty string to *clear* any of them -- json.Marshal
	//    drops the key entirely, and the controller's PUT handler merges
	//    by JSON key presence, so an omitted key just leaves the existing
	//    value in place. See updateUser's comment for how this was found.
	Get(ctx context.Context, apiPath string, reqBody, respBody interface{}) error
	Put(ctx context.Context, apiPath string, reqBody, respBody interface{}) error
}

// updateUser pushes d (an Update step's desired state, with d.ID already
// set to the existing controller record's ID) to /rest/user/<id> via the
// generic Put rather than unifi.Client's typed UpdateUser. Needed because
// Name, FixedIP, and Note are all tagged `omitempty` on the SDK's User
// struct, so a *desired* empty value for any of them -- exactly what
// diffUser produces when inventory clears a name, drops a previously-applied
// ip:, or drops notes: -- never makes it into the request body; the
// controller just leaves the stale value in place. Confirmed live against
// the real controller: a reported-successful apply left a stale fixed_ip
// untouched, while every non-empty-string / bool field in the same Update
// landed correctly.
//
// Re-adding name/fixed_ip/note to the marshaled body (even when empty) is
// both necessary and sufficient to make a clear stick: the controller's PUT
// merges by JSON key presence rather than replacing the whole record --
// confirmed by the fact that fields this reconciler never sets at all
// (hostname, last_seen, ...) survive a real Update untouched.
func updateUser(ctx context.Context, api unifiAPI, site string, d *unifi.User) (*unifi.User, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["name"] = d.Name
	body["fixed_ip"] = d.FixedIP
	body["note"] = d.Note

	var resp struct {
		Data []unifi.User `json:"data"`
	}
	if err := api.Put(ctx, fmt.Sprintf("s/%s/rest/user/%s", site, d.ID), body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("update user %s: unexpected response shape", d.MAC)
	}
	return &resp.Data[0], nil
}

// observedIP returns the best available current-or-last-known IP for mac
// from /stat/user/<mac>: "ip" when the client is online/recently cached,
// falling back to "last_ip" (which UniFi keeps long after disconnection) --
// neither of which the SDK's typed User struct exposes. Returns "" on any
// lookup failure (unknown MAC, never-seen client, transient error); callers
// treat that the same as "no signal available".
func observedIP(ctx context.Context, api unifiAPI, site, mac string) string {
	var resp struct {
		Data []struct {
			IP     string `json:"ip"`
			LastIP string `json:"last_ip"`
		} `json:"data"`
	}
	if err := api.Get(ctx, fmt.Sprintf("s/%s/stat/user/%s", site, mac), nil, &resp); err != nil {
		return ""
	}
	if len(resp.Data) == 0 {
		return ""
	}
	if resp.Data[0].IP != "" {
		return resp.Data[0].IP
	}
	return resp.Data[0].LastIP
}

// newUnifiClient constructs the upstream zoullx client. APIKey auth only;
// VerifySSL=false matches the existing playbook's validate_certs: false.
// ValidationMode=Disable matches radius-user-importer's choice.
//
// Declared as a var so tests can swap in a fake unifiAPI without standing up
// an HTTP server.
var newUnifiClient = func(baseURL, apiKey string, insecure bool) (unifiAPI, error) {
	c, err := unifi.NewClient(&unifi.ClientConfig{
		URL:            baseURL,
		APIKey:         apiKey,
		VerifySSL:      !insecure,
		ValidationMode: unifi.DisableValidation,
	})
	if err != nil {
		return nil, fmt.Errorf("create unifi client: %w", err)
	}
	return c, nil
}

// guestNetworkIDs returns the set of network IDs whose Purpose is "guest".
// Used to skip guest clients during bootstrap and sync.
func guestNetworkIDs(networks []unifi.Network) map[string]struct{} {
	out := map[string]struct{}{}
	for _, n := range networks {
		if n.Purpose == "guest" {
			out[n.ID] = struct{}{}
		}
	}
	return out
}

// networkBySubnet returns the network whose IPSubnet, normalized as a
// CIDR network address (not gateway IP), matches the given subnet. The
// existing unifi-reservations.yml playbook does the same normalization in
// Jinja — see playbooks/unifi-reservations.yml:52-69 for the pattern.
// Returns ErrNotFound if no network matches.
func networkBySubnet(networks []unifi.Network, subnet string) (*unifi.Network, error) {
	want, err := parseCIDRNetwork(subnet)
	if err != nil {
		return nil, fmt.Errorf("subnet %q: %w", subnet, err)
	}
	for i := range networks {
		if networks[i].IPSubnet == "" {
			continue
		}
		got, err := parseCIDRNetwork(networks[i].IPSubnet)
		if err != nil {
			continue
		}
		if got == want {
			return &networks[i], nil
		}
	}
	return nil, errors.New("no matching network")
}
