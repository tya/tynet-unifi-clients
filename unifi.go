package main

import (
	"context"
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
	UpdateUser(ctx context.Context, site string, d *unifi.User) (*unifi.User, error)
}

// newUnifiClient constructs the upstream zoullx client. APIKey auth only;
// VerifySSL=false matches the existing playbook's validate_certs: false.
// ValidationMode=Disable matches radius-user-importer's choice.
func newUnifiClient(baseURL, apiKey string, insecure bool) (unifiAPI, error) {
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
