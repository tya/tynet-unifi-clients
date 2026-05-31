package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zoullx/unifi-go/unifi"
)

var testNetworks = []unifi.Network{
	{ID: "net-60", Name: "Trusted", VLAN: 60, IPSubnet: "10.0.60.1/24", Purpose: "corporate"},
	{ID: "net-70", Name: "Personal", VLAN: 70, IPSubnet: "10.0.70.1/24", Purpose: "corporate"},
	{ID: "net-99", Name: "Guests", VLAN: 99, IPSubnet: "10.0.99.1/24", Purpose: "guest"},
}

func TestBuild_PureCreate(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", IP: "10.0.70.42", VLANID: 70},
	}
	plan := Build(desired, nil, testNetworks)
	if len(plan.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(plan.Steps))
	}
	s := plan.Steps[0]
	if s.Action != Create {
		t.Errorf("want Create, got %s", s.Action)
	}
	if s.Desired.VirtualNetworkOverrideID != "net-70" || !s.Desired.VirtualNetworkOverrideEnabled {
		t.Errorf("VLAN override not set on desired: %+v", s.Desired)
	}
	if s.Desired.FixedIP != "10.0.70.42" || !s.Desired.UseFixedIP {
		t.Errorf("fixed IP not set on desired: %+v", s.Desired)
	}
}

func TestBuild_Noop(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", IP: "10.0.70.42", VLANID: 70},
	}
	actual := []unifi.User{{
		ID:                            "u1",
		MAC:                           "aa:bb:cc:dd:ee:ff",
		Name:                          "alice",
		NetworkID:                     "net-70",
		FixedIP:                       "10.0.70.42",
		UseFixedIP:                    true,
		VirtualNetworkOverrideEnabled: true,
		VirtualNetworkOverrideID:      "net-70",
	}}
	plan := Build(desired, actual, testNetworks)
	if plan.Steps[0].Action != Noop {
		t.Errorf("want Noop, got %s; reasons=%v", plan.Steps[0].Action, plan.Steps[0].Reasons)
	}
	if plan.HasDrift() {
		t.Error("HasDrift should be false on pure noop")
	}
}

func TestBuild_Update(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", IP: "10.0.70.42", VLANID: 70},
	}
	actual := []unifi.User{{
		ID:        "u1",
		MAC:       "aa:bb:cc:dd:ee:ff",
		Name:      "alice-old", // name drifted
		NetworkID: "net-60",    // wrong network
		FixedIP:   "10.0.60.42",
	}}
	plan := Build(desired, actual, testNetworks)
	s := plan.Steps[0]
	if s.Action != Update {
		t.Fatalf("want Update, got %s", s.Action)
	}
	if len(s.Reasons) == 0 {
		t.Error("Update step has no reasons")
	}
	if !plan.HasDrift() {
		t.Error("HasDrift should be true on update")
	}
}

func TestBuild_SkipGuest(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "burner-phone", IP: "10.0.99.42", VLANID: 99},
	}
	plan := Build(desired, nil, testNetworks)
	if plan.Steps[0].Action != SkipGuest {
		t.Errorf("want SkipGuest, got %s", plan.Steps[0].Action)
	}
	if plan.HasDrift() {
		t.Error("guest skip should not count as drift")
	}
}

func TestBuild_SkipUnknownNetwork(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "x", VLANID: 200},
	}
	plan := Build(desired, nil, testNetworks)
	if plan.Steps[0].Action != SkipUnknownNetwork {
		t.Errorf("want SkipUnknownNetwork, got %s", plan.Steps[0].Action)
	}
}

func TestBuild_FixedIPDriftWarning(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", IP: "10.0.70.42", VLANID: 70},
	}
	actual := []unifi.User{{
		ID:                            "u1",
		MAC:                           "aa:bb:cc:dd:ee:ff",
		Name:                          "alice",
		NetworkID:                     "net-70",
		FixedIP:                       "10.0.70.99", // drift!
		UseFixedIP:                    true,
		VirtualNetworkOverrideEnabled: true,
		VirtualNetworkOverrideID:      "net-70",
	}}
	plan := Build(desired, actual, testNetworks)
	s := plan.Steps[0]
	if s.Action != Update {
		t.Errorf("want Update (fixed_ip change), got %s", s.Action)
	}
	if len(s.Warnings) == 0 {
		t.Error("want at least one warning about fixed_ip drift")
	}
}

func TestBuild_MACNormalizationOnExisting(t *testing.T) {
	// Controller may return MAC in any case; our matcher normalizes.
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", VLANID: 70},
	}
	actual := []unifi.User{{
		ID:                            "u1",
		MAC:                           "AA-BB-CC-DD-EE-FF",
		Name:                          "alice",
		NetworkID:                     "net-70",
		VirtualNetworkOverrideEnabled: true,
		VirtualNetworkOverrideID:      "net-70",
	}}
	plan := Build(desired, actual, testNetworks)
	if plan.Steps[0].Action != Noop {
		t.Errorf("want Noop (matched via canonicalization), got %s; reasons=%v",
			plan.Steps[0].Action, plan.Steps[0].Reasons)
	}
}

func TestBuild_StableSort(t *testing.T) {
	desired := []Client{
		{MAC: "ff:ff:ff:ff:ff:ff", Name: "z", VLANID: 70},
		{MAC: "00:00:00:00:00:01", Name: "a", VLANID: 70},
		{MAC: "11:11:11:11:11:11", Name: "m", VLANID: 70},
	}
	plan := Build(desired, nil, testNetworks)
	wantOrder := []string{"00:00:00:00:00:01", "11:11:11:11:11:11", "ff:ff:ff:ff:ff:ff"}
	for i, s := range plan.Steps {
		if s.MAC != wantOrder[i] {
			t.Errorf("step %d: want %s, got %s", i, wantOrder[i], s.MAC)
		}
	}
}

func TestRenderAndSummary(t *testing.T) {
	desired := []Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Name: "alice", VLANID: 70},
		{MAC: "11:22:33:44:55:66", Name: "bob", VLANID: 60},
	}
	plan := Build(desired, nil, testNetworks)

	var buf bytes.Buffer
	plan.Render(&buf)
	if !strings.Contains(buf.String(), "aa:bb:cc:dd:ee:ff") {
		t.Errorf("render missing MAC: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "create") {
		t.Errorf("render missing action: %q", buf.String())
	}

	dryRun := plan.Summary(false)
	if !strings.Contains(dryRun, "would create") {
		t.Errorf("dry-run summary should say 'would create': %q", dryRun)
	}

	applied := plan.Summary(true)
	if !strings.Contains(applied, "created") || strings.Contains(applied, "would") {
		t.Errorf("applied summary wrong: %q", applied)
	}
}
