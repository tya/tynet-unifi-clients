package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadClients_Happy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml"), `---
mac: aa:bb:cc:dd:ee:ff
name: alice
ip: 10.0.70.42
vlan_id: 70
connection: wireless
`)
	writeFile(t, filepath.Join(dir, "11-22-33-44-55-66.yml"), `---
mac: 11:22:33:44:55:66
name: bob
vlan_id: 60
connection: wired
switch_mac: 18:e8:29:23:bf:22
switch_port: 14
`)
	clients, err := loadClients(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 {
		t.Fatalf("want 2 clients, got %d", len(clients))
	}
	if clients[0].MAC != "11:22:33:44:55:66" {
		t.Errorf("want sorted by MAC; got first = %q", clients[0].MAC)
	}
	if clients[1].IP != "10.0.70.42" {
		t.Errorf("ip not loaded: %+v", clients[1])
	}
	if clients[0].SwitchPort != 14 {
		t.Errorf("switch_port not loaded: %+v", clients[0])
	}
}

func TestLoadClients_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		target  error
	}{
		{
			"filename mismatch",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: 11:22:33:44:55:66\nname: x\nvlan_id: 60\n",
			errFilenameMismatch,
		},
		{
			"vlan out of range",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: aa:bb:cc:dd:ee:ff\nname: x\nvlan_id: 5000\n",
			errInvalidVLAN,
		},
		{
			"vlan zero",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: aa:bb:cc:dd:ee:ff\nname: x\nvlan_id: 0\n",
			errInvalidVLAN,
		},
		{
			"ip third octet mismatches vlan",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: aa:bb:cc:dd:ee:ff\nname: x\nvlan_id: 70\nip: 10.0.60.42\n",
			errVLANIPMismatch,
		},
		{
			"invalid ip",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: aa:bb:cc:dd:ee:ff\nname: x\nvlan_id: 70\nip: not-an-ip\n",
			errInvalidIP,
		},
		{
			"invalid mac",
			"zz-zz-zz-zz-zz-zz.yml",
			"mac: zz:zz:zz:zz:zz:zz\nname: x\nvlan_id: 70\n",
			errInvalidMAC,
		},
		{
			"invalid connection",
			"aa-bb-cc-dd-ee-ff.yml",
			"mac: aa:bb:cc:dd:ee:ff\nname: x\nvlan_id: 70\nconnection: ether\n",
			errInvalidConnection,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.content)
			_, err := loadClients(dir)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("want errors.Is(%v); got %v", tc.target, err)
			}
		})
	}
}

func TestLoadClients_MissingDir(t *testing.T) {
	_, err := loadClients(filepath.Join(t.TempDir(), "does-not-exist"))
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestLoadClients_IPOptional(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml"), `---
mac: aa:bb:cc:dd:ee:ff
name: x
vlan_id: 70
`)
	clients, err := loadClients(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].IP != "" {
		t.Errorf("optional ip not handled: %+v", clients)
	}
}

func TestWriteClientFile_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	c := Client{MAC: "aa:bb:cc:dd:ee:ff", Name: "x", VLANID: 70}
	if _, err := writeClientFile(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, err := writeClientFile(dir, c); err == nil {
		t.Fatal("want overwrite error, got nil")
	}
}

func TestWriteClientFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Client{
		MAC:        "aa:bb:cc:dd:ee:ff",
		Name:       "alice",
		IP:         "10.0.70.42",
		VLANID:     70,
		Connection: "wireless",
		Notes:      "round-trip me",
	}
	path, err := writeClientFile(dir, want)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "aa-bb-cc-dd-ee-ff.yml" {
		t.Errorf("unexpected filename: %s", path)
	}
	got, err := loadClientFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got.SourceFile = "" // not preserved across write/read; we set it on load only
	want.SourceFile = ""
	if got != want {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadClients_NotADirectory(t *testing.T) {
	// Pointing inventory at a regular file should fail cleanly with the
	// inventory-not-found sentinel.
	dir := t.TempDir()
	path := filepath.Join(dir, "file-not-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadClients(path)
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestLoadClients_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml"),
		"mac: [unterminated\n")
	_, err := loadClients(dir)
	if err == nil {
		t.Fatal("want yaml error, got nil")
	}
}

func TestLoadClients_InvalidSwitchMAC(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml"), `---
mac: aa:bb:cc:dd:ee:ff
name: x
vlan_id: 70
connection: wired
switch_mac: not-a-mac
switch_port: 14
`)
	_, err := loadClients(dir)
	if !errors.Is(err, errInvalidMAC) {
		t.Fatalf("want errInvalidMAC for switch_mac, got %v", err)
	}
}

func TestLoadClients_IgnoresNonYAMLFiles(t *testing.T) {
	// README, .DS_Store, *.txt etc. in the inventory dir should be silently
	// skipped — only *.yml files are loaded. Covers the suffix-skip branch.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README"), "just notes\n")
	writeFile(t, filepath.Join(dir, ".DS_Store"), "")
	writeFile(t, filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml"), `---
mac: aa:bb:cc:dd:ee:ff
name: x
vlan_id: 70
`)
	clients, err := loadClients(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Errorf("want 1 client, got %d", len(clients))
	}
}

func TestWriteClientFile_InvalidMAC(t *testing.T) {
	// Path-level guard: writeClientFile re-validates the MAC before computing
	// the on-disk filename so a caller can't trick it into writing
	// "not-a-mac.yml".
	_, err := writeClientFile(t.TempDir(), Client{MAC: "not-a-mac", VLANID: 70})
	if !errors.Is(err, errInvalidMAC) {
		t.Fatalf("want errInvalidMAC, got %v", err)
	}
}

func TestLoadHostVarMACs_MissingDir(t *testing.T) {
	// host_vars dir is optional — a missing path should produce an empty map,
	// not an error (bootstrap's --host-vars-dir is similarly optional).
	macs, err := loadHostVarMACs(filepath.Join(t.TempDir(), "nope"), "")
	if err != nil {
		t.Fatalf("want nil error for missing dir, got %v", err)
	}
	if len(macs) != 0 {
		t.Errorf("want empty map, got %v", macs)
	}
}

func TestLoadHostVarMACs_MalformedFilesSkipped(t *testing.T) {
	// host_vars contains many non-MAC files (certbot, etc.). Malformed YAML
	// and missing-MAC files should be silently skipped; only valid node_mac
	// entries land in the result.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bad.yml"), "mac: [unterminated\n")
	writeFile(t, filepath.Join(dir, "no-mac.yml"), "certbot_email: foo@example.com\n")
	writeFile(t, filepath.Join(dir, "bad-mac.yml"), "node_mac: zz:zz:zz:zz:zz:zz\n")
	// Nested directory: loadHostVarMACs uses ReadDir, not WalkDir, but the
	// dir entry should still be skipped via the IsDir branch.
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "ok.yml"), "node_mac: dc-a6-32-8d-f3-ca\n")
	macs, err := loadHostVarMACs(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(macs) != 1 {
		t.Errorf("want 1 mac (only ok.yml), got %v", macs)
	}
	if _, ok := macs["dc:a6:32:8d:f3:ca"]; !ok {
		t.Errorf("expected mac missing from %v", macs)
	}
}

func TestLoadHostVarMACs_BogusKickstart(t *testing.T) {
	// A non-MAC kickstart_mac value in group_vars/all.yml is dropped silently
	// rather than failing the whole load.
	dir := t.TempDir()
	groupVarsAll := filepath.Join(dir, "all.yml")
	writeFile(t, groupVarsAll, "kickstart_mac: not-a-mac\n")
	macs, err := loadHostVarMACs(filepath.Join(t.TempDir(), "no-host-vars"), groupVarsAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(macs) != 0 {
		t.Errorf("bogus kickstart_mac should be dropped, got %v", macs)
	}
}

func TestLoadHostVarMACs_MalformedGroupVars(t *testing.T) {
	// Malformed group_vars/all.yml is silently skipped — no error.
	dir := t.TempDir()
	groupVarsAll := filepath.Join(dir, "all.yml")
	writeFile(t, groupVarsAll, "kickstart_mac: [unterminated\n")
	macs, err := loadHostVarMACs(filepath.Join(t.TempDir(), "no-host-vars"), groupVarsAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(macs) != 0 {
		t.Errorf("malformed group_vars should yield empty map, got %v", macs)
	}
}

func TestLoadHostVarMACs(t *testing.T) {
	dir := t.TempDir()
	hostVars := filepath.Join(dir, "host_vars")
	writeFile(t, filepath.Join(hostVars, "pi2.tynet.us.yml"), `---
node_serial: 244634d3
node_ip: 10.0.60.202
node_mac: dc-a6-32-8d-f3-ca
unifi_port_idx: 6
`)
	writeFile(t, filepath.Join(hostVars, "vpn.tynet.us.yml"), `---
certbot_email: foo@example.com
`)
	groupVarsAll := filepath.Join(dir, "all.yml")
	writeFile(t, groupVarsAll, `---
subnet: 10.0.60.0/24
kickstart_mac: dca632807952
`)
	macs, err := loadHostVarMACs(hostVars, groupVarsAll)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dc:a6:32:8d:f3:ca", "dc:a6:32:80:79:52"}
	if len(macs) != len(want) {
		t.Fatalf("want %d macs, got %d: %v", len(want), len(macs), macs)
	}
	for _, m := range want {
		if _, ok := macs[m]; !ok {
			t.Errorf("missing expected MAC %q in %v", m, macs)
		}
	}
}
