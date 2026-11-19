package main

import (
	"context"
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

func TestPatchedClient_ListUserToleratesStringDevIDOverride(t *testing.T) {
	// Regression test: confirmed live that the controller sometimes
	// returns dev_id_override as a JSON string ("2749") rather than the
	// int the SDK's generated User struct expects, and since
	// User.UnmarshalJSON decodes the whole /rest/user batch at once, ONE
	// such record breaks ListUser entirely -- seen live affecting 62 of
	// 159 clients. patchedClient.ListUser must tolerate this (we never
	// read dev_id_override) and still decode every record, including the
	// fields we actually care about.
	fake := &fakeUnifiAPI{getRespByPath: map[string]string{
		"s/default/rest/user": `{"data":[
			{"mac":"aa:bb:cc:dd:ee:ff","name":"alice","dev_id_override":"2749"},
			{"mac":"11:22:33:44:55:66","name":"bob","dev_id_override":2791}
		]}`,
	}}
	p := &patchedClient{unifiAPI: fake}
	users, err := p.ListUser(context.Background(), "default")
	if err != nil {
		t.Fatalf("ListUser: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	if users[0].MAC != "aa:bb:cc:dd:ee:ff" || users[0].Name != "alice" {
		t.Errorf("string dev_id_override record: want aa:bb:cc:dd:ee:ff/alice, got %+v", users[0])
	}
	if users[1].MAC != "11:22:33:44:55:66" || users[1].Name != "bob" {
		t.Errorf("int dev_id_override record: want 11:22:33:44:55:66/bob, got %+v", users[1])
	}
}

func TestPatchedClient_CreateUserToleratesStringDevIDOverride(t *testing.T) {
	// Same bug, second call site: confirmed live that the controller
	// echoes the new record's dev_id_override back as a string in the
	// create response too, breaking the SDK's typed CreateUser the exact
	// same way. patchedClient.CreateUser mirrors the SDK's own nested
	// objects/data response shape but decodes via decodeUserRecord.
	fake := &fakeUnifiAPI{postRespByPath: map[string]string{
		"s/default/group/user": `{"data":[
			{"data":[{"mac":"aa:bb:cc:dd:ee:ff","name":"alice","dev_id_override":"2749"}]}
		]}`,
	}}
	p := &patchedClient{unifiAPI: fake}
	got, err := p.CreateUser(context.Background(), "default", &unifi.User{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got.MAC != "aa:bb:cc:dd:ee:ff" || got.Name != "alice" {
		t.Errorf("want aa:bb:cc:dd:ee:ff/alice, got %+v", got)
	}
}

func TestPatchedClient_CreateUserMalformedResponse(t *testing.T) {
	fake := &fakeUnifiAPI{postRespByPath: map[string]string{
		"s/default/group/user": `{"data":[]}`,
	}}
	p := &patchedClient{unifiAPI: fake}
	if _, err := p.CreateUser(context.Background(), "default", &unifi.User{MAC: "aa:bb:cc:dd:ee:ff"}); err == nil {
		t.Fatal("want error for empty data, got nil")
	}
}

func TestPatchedClient_CreateUserSurfacesServerError(t *testing.T) {
	// HTTP 200 batch envelope, but the per-item Meta reports a real failure
	// (duplicate MAC, validation error, license limit, etc.) and data is
	// empty -- exactly how the real SDK's CreateUser surfaces this case via
	// Meta.error(). Before this fix, patchedClient.CreateUser had no field
	// for Meta at all, so this fell through to a bare "malformed group
	// response" with no hint of the actual server-reported reason.
	fake := &fakeUnifiAPI{postRespByPath: map[string]string{
		"s/default/group/user": `{"data":[
			{"Meta":{"rc":"error","msg":"api.err.MacUsed"},"data":[]}
		]}`,
	}}
	p := &patchedClient{unifiAPI: fake}
	_, err := p.CreateUser(context.Background(), "default", &unifi.User{MAC: "aa:bb:cc:dd:ee:ff"})
	if err == nil {
		t.Fatal("want error for Meta.rc != ok, got nil")
	}
	if !strings.Contains(err.Error(), "api.err.MacUsed") {
		t.Errorf("want the server's actual message surfaced, got: %v", err)
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
