package main

import (
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

func TestNetworkBySubnet_NotFound(t *testing.T) {
	nets := []unifi.Network{
		{ID: "n1", Name: "Main", IPSubnet: "10.0.60.1/24"},
	}
	_, err := networkBySubnet(nets, "10.0.70.0/24")
	if err == nil {
		t.Fatal("want error, got nil")
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
