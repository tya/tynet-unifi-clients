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
	// Get, Put, and Post are the SDK's generic REST escape hatches. Used
	// directly (rather than typed wrappers) in several places where the
	// typed path silently drops data or outright fails to decode:
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
	//  - Get and Post, by patchedClient's ListUser/CreateUser overrides:
	//    both the typed ListUser and CreateUser fail outright decoding any
	//    response containing a client whose dev_id_override is a JSON
	//    string instead of the int the SDK expects. See patchedClient's
	//    doc comment.
	Get(ctx context.Context, apiPath string, reqBody, respBody interface{}) error
	Put(ctx context.Context, apiPath string, reqBody, respBody interface{}) error
	Post(ctx context.Context, apiPath string, reqBody, respBody interface{}) error
}

// decodeUserRecord unmarshals a single raw controller user record (as
// decoded into a generic map, from any /rest/user- or /group/user-shaped
// response) into unifi.User, stripping dev_id_override first. Shared by
// every place this tool decodes a raw user record from the controller --
// see patchedClient's doc comment for why that field needs stripping.
func decodeUserRecord(m map[string]any) (unifi.User, error) {
	delete(m, "dev_id_override")
	b, err := json.Marshal(m)
	if err != nil {
		return unifi.User{}, fmt.Errorf("remarshal user record: %w", err)
	}
	var u unifi.User
	if err := json.Unmarshal(b, &u); err != nil {
		return unifi.User{}, fmt.Errorf("unmarshal user record: %w", err)
	}
	return u, nil
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
//
// The response is decoded via decodeUserRecord, not json.Unmarshal into
// []unifi.User directly, for a second, unrelated reason: the controller's
// PUT response for a given client includes that client's own
// dev_id_override, which fails the SDK's typed decode exactly like
// patchedClient's ListUser/CreateUser overrides -- confirmed live: this
// broke the very first real Update this tool attempted after ListUser was
// fixed.
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
		Data []map[string]any `json:"data"`
	}
	if err := api.Put(ctx, fmt.Sprintf("s/%s/rest/user/%s", site, d.ID), body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("update user %s: unexpected response shape", d.MAC)
	}
	u, err := decodeUserRecord(resp.Data[0])
	if err != nil {
		return nil, err
	}
	return &u, nil
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
// Wrapped in patchedClient (see its doc comment) to work around a live
// controller/SDK incompatibility in ListUser.
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
	return &patchedClient{unifiAPI: c}, nil
}

// patchedClient wraps a real unifiAPI client and overrides ListUser and
// CreateUser to route around a live controller/SDK incompatibility: as of
// some recent UniFi Network app update, a client's dev_id_override now
// sometimes comes back as a JSON string (e.g. "2749") instead of the int
// github.com/zoullx/unifi-go v0.0.8 (the latest release; no newer version
// exists) expects. Both the typed ListUser and CreateUser decode their
// response as a batch that includes this field, so ONE such record in
// either response fails the whole call -- confirmed live affecting 62 of
// 159 clients for ListUser, meaning it was failing on essentially every
// real call, and confirmed live for CreateUser too: the controller echoes
// the new record's dev_id_override back in the creation response.
// (updateUser has the same problem for its own PUT response, but since it
// already bypasses the typed UpdateUser for an unrelated omitempty reason,
// it decodes via the shared decodeUserRecord directly rather than needing
// a patchedClient override.)
//
// dev_id_override isn't used anywhere in this tool, so the fix is to fetch
// each response via the generic Get/Post escape hatch and decode every
// record via decodeUserRecord, which strips the field before unmarshaling.
// Every other unifiAPI method (ListNetwork, Get, Put) is unaffected and
// passes straight through via the embedded real client.
type patchedClient struct {
	unifiAPI
}

func (p *patchedClient) ListUser(ctx context.Context, site string) ([]unifi.User, error) {
	var raw struct {
		Data []map[string]any `json:"data"`
	}
	if err := p.unifiAPI.Get(ctx, fmt.Sprintf("s/%s/rest/user", site), nil, &raw); err != nil {
		return nil, err
	}
	users := make([]unifi.User, 0, len(raw.Data))
	for _, m := range raw.Data {
		u, err := decodeUserRecord(m)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// CreateUser mirrors the SDK's own request shape (see unifi-go's
// user.generated.go CreateUser) but decodes the response via
// decodeUserRecord instead of unmarshaling into []unifi.User directly.
func (p *patchedClient) CreateUser(ctx context.Context, site string, d *unifi.User) (*unifi.User, error) {
	reqBody := struct {
		Objects []struct {
			Data *unifi.User `json:"data"`
		} `json:"objects"`
	}{
		Objects: []struct {
			Data *unifi.User `json:"data"`
		}{{Data: d}},
	}
	var respBody struct {
		Data []struct {
			Data []map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := p.unifiAPI.Post(ctx, fmt.Sprintf("s/%s/group/user", site), reqBody, &respBody); err != nil {
		return nil, err
	}
	if len(respBody.Data) != 1 || len(respBody.Data[0].Data) != 1 {
		return nil, errors.New("malformed group response")
	}
	u, err := decodeUserRecord(respBody.Data[0].Data[0])
	if err != nil {
		return nil, err
	}
	return &u, nil
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
