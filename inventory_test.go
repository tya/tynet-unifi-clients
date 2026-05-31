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
