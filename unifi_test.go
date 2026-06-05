package main

import (
	"strings"
	"testing"

	"github.com/zoullx/unifi-go/unifi"
)

func TestGuestNetworkIDs(t *testing.T) {
	nets := []unifi.Network{
		{ID: "n1", Name: "Main", Purpose: "corporate"},
		{ID: "n2", Name: "Guest", Purpose: "guest"},
		{ID: "n3", Name: "IoT", Purpose: "corporate"},
		{ID: "n4", Name: "Visitors", Purpose: "guest"},
	}
	got := guestNetworkIDs(nets)
	if len(got) != 2 {
		t.Fatalf("want 2 guest IDs, got %d: %v", len(got), got)
	}
	for _, id := range []string{"n2", "n4"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %q in %v", id, got)
		}
	}
}

func TestNetworkBySubnet(t *testing.T) {
	// UniFi reports gateway-IP/prefix ("10.0.60.1/24"); we ask for
	// network-address/prefix ("10.0.60.0/24"). Both normalize to the same
	// network identity.
	nets := []unifi.Network{
		{ID: "n1", Name: "Main", IPSubnet: "10.0.60.1/24"},
		{ID: "n2", Name: "IoT", IPSubnet: "10.0.50.1/24"},
		{ID: "n3", Name: "skip-no-subnet"},
	}
	got, err := networkBySubnet(nets, "10.0.60.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "n1" {
		t.Errorf("want n1, got %s", got.ID)
	}
}

func TestNetworkBySubnet_SkipsEmptyAndMalformed(t *testing.T) {
	// Order matters: the searched subnet matches "n3", so the loop has to
	// pass over the empty-IPSubnet entry (n1) and the malformed-IPSubnet
	// entry (n2) before finding it. Covers both continue arms.
	nets := []unifi.Network{
		{ID: "n1", Name: "no-subnet"},
		{ID: "n2", Name: "garbage-subnet", IPSubnet: "not-a-cidr"},
		{ID: "n3", Name: "Main", IPSubnet: "10.0.60.1/24"},
	}
	got, err := networkBySubnet(nets, "10.0.60.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "n3" {
		t.Errorf("want n3, got %s", got.ID)
	}
}

func TestNetworkBySubnet_BadInput(t *testing.T) {
	// Bad input subnet → wrapped parse error.
	_, err := networkBySubnet(nil, "not-a-cidr")
	if err == nil || !strings.Contains(err.Error(), "subnet \"not-a-cidr\"") {
		t.Fatalf("want subnet-parse wrap, got %v", err)
	}
}

func TestNetworkBySubnet_NotFound(t *testing.T) {
	nets := []unifi.Network{
		{ID: "n1", Name: "Main", IPSubnet: "10.0.60.1/24"},
	}
	_, err := networkBySubnet(nets, "10.0.70.0/24")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestNewUnifiClient_InvalidConfig(t *testing.T) {
	// Empty URL fails the `validate:"required,http_url"` constraint on
	// ClientConfig.URL before any HTTP is attempted. Covers the error-wrap
	// branch in newUnifiClient. The success branch dials the controller
	// (NewClient does a login + system-info fetch), so we deliberately do
	// not exercise it here — see the plan's out-of-scope list.
	_, err := newUnifiClient("", "any-key", true)
	if err == nil {
		t.Fatal("want error for empty URL, got nil")
	}
	if !strings.Contains(err.Error(), "create unifi client") {
		t.Errorf("want wrap prefix %q, got %v", "create unifi client", err)
	}
}

func TestParseCIDRNetwork(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"10.0.60.1/24", "10.0.60.0/24", false},
		{"10.0.60.0/24", "10.0.60.0/24", false},
		{"10.0.70.42/16", "10.0.0.0/16", false},
		{"not-a-cidr", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseCIDRNetwork(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
