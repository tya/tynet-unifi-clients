package main

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Client is a single record in inventory/clients/<mac>.yml. It is the desired
// state for one device that the reconciler will push to UniFi.
type Client struct {
	MAC        string // canonical lowercase colon-separated
	Name       string
	IP         string // optional fixed IP; if set, third octet must equal VLANID
	VLANID     int
	Connection string // wired | wireless | ""
	SwitchMAC  string // wired-only, informational
	SwitchPort int    // wired-only, informational
	Notes      string
	SourceFile string // absolute path of the YAML file this came from
}

// clientYAML is the on-disk shape. Separate from Client so we can validate
// before exposing the typed value. Unknown keys do not error (yaml.v3 default).
type clientYAML struct {
	MAC        string `yaml:"mac"`
	Name       string `yaml:"name"`
	IP         string `yaml:"ip,omitempty"`
	VLANID     int    `yaml:"vlan_id"`
	Connection string `yaml:"connection,omitempty"`
	SwitchMAC  string `yaml:"switch_mac,omitempty"`
	SwitchPort int    `yaml:"switch_port,omitempty"`
	Notes      string `yaml:"notes,omitempty"`
}

// loadClients walks dir for *.yml files, parses each as a Client, validates
// each against the schema rules, and returns the slice sorted by canonical
// MAC. A single invalid file aborts the whole load — partial inventories are
// worse than no inventory.
func loadClients(dir string) ([]Client, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", errInventoryNotFound, dir)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: not a directory: %s", errInventoryNotFound, dir)
	}

	var out []Client
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".yml") && !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		c, err := loadClientFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, c)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out, nil
}

func loadClientFile(path string) (Client, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Client{}, err
	}
	var y clientYAML
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(false) // forward-compat: unknown keys are warnings, not errors
	if err := dec.Decode(&y); err != nil {
		return Client{}, fmt.Errorf("yaml: %w", err)
	}

	mac, err := canonicalMAC(y.MAC)
	if err != nil {
		return Client{}, err
	}

	// Filename basename must match the canonical MAC (dashed). Catches
	// rename mistakes where someone edits mac: in the file but forgets to
	// rename the file (or vice versa).
	wantName := macFilename(mac)
	gotName := filepath.Base(path)
	if gotName != wantName {
		return Client{}, fmt.Errorf("%w: file %q has mac %q (expected filename %q)",
			errFilenameMismatch, gotName, mac, wantName)
	}

	if y.VLANID < 1 || y.VLANID > 4094 {
		return Client{}, fmt.Errorf("%w: %d (must be 1..4094)", errInvalidVLAN, y.VLANID)
	}

	if y.IP != "" {
		parsed := net.ParseIP(y.IP).To4()
		if parsed == nil {
			return Client{}, fmt.Errorf("%w: %q is not a valid IPv4 address", errInvalidIP, y.IP)
		}
		if int(parsed[2]) != y.VLANID {
			return Client{}, fmt.Errorf("%w: ip %s third octet is %d, vlan_id is %d",
				errVLANIPMismatch, y.IP, parsed[2], y.VLANID)
		}
	}

	switch y.Connection {
	case "", "wired", "wireless":
	default:
		return Client{}, fmt.Errorf("%w: %q", errInvalidConnection, y.Connection)
	}

	switchMAC := ""
	if y.SwitchMAC != "" {
		sm, err := canonicalMAC(y.SwitchMAC)
		if err != nil {
			return Client{}, fmt.Errorf("switch_mac: %w", err)
		}
		switchMAC = sm
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	return Client{
		MAC:        mac,
		Name:       y.Name,
		IP:         y.IP,
		VLANID:     y.VLANID,
		Connection: y.Connection,
		SwitchMAC:  switchMAC,
		SwitchPort: y.SwitchPort,
		Notes:      y.Notes,
		SourceFile: abs,
	}, nil
}

// writeClientFile renders a Client to YAML at the canonical path under dir.
// Used by `bootstrap --write`. Refuses to overwrite an existing file so the
// human's local edits are never silently clobbered.
func writeClientFile(dir string, c Client) (string, error) {
	if _, err := canonicalMAC(c.MAC); err != nil {
		return "", err
	}
	path := filepath.Join(dir, macFilename(c.MAC))
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("refusing to overwrite existing file: %s", path)
	}

	y := clientYAML{
		MAC:        c.MAC,
		Name:       c.Name,
		IP:         c.IP,
		VLANID:     c.VLANID,
		Connection: c.Connection,
		SwitchMAC:  c.SwitchMAC,
		SwitchPort: c.SwitchPort,
		Notes:      c.Notes,
	}
	buf, err := yaml.Marshal(&y)
	if err != nil {
		return "", err
	}
	// Add a leading "---\n" for ansible-lint friendliness; matches tynet-infra
	// style in inventory/host_vars/*.yml.
	out := append([]byte("---\n"), buf...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// loadHostVarMACs parses inventory/host_vars/*.yml in the tynet-infra repo
// and returns the canonical MACs of all SSH-managed hosts. bootstrap calls
// this so it never proposes a clients/ file for a host that already lives
// in host_vars/ (those are owned by playbooks/unifi-reservations.yml).
//
// Both `node_mac` (per-host) and `kickstart_mac` (group_vars) shapes are
// accepted. Files we can't parse are skipped silently — host_vars contains
// many YAMLs without a MAC at all (certbot, kickstart.vm, etc.).
func loadHostVarMACs(hostVarsDir, groupVarsAllFile string) (map[string]struct{}, error) {
	out := map[string]struct{}{}

	entries, err := os.ReadDir(hostVarsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(hostVarsDir, e.Name()))
		if err != nil {
			continue
		}
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			continue
		}
		if v, ok := m["node_mac"].(string); ok {
			if canon, err := canonicalMAC(v); err == nil {
				out[canon] = struct{}{}
			}
		}
	}

	if groupVarsAllFile != "" {
		if raw, err := os.ReadFile(groupVarsAllFile); err == nil {
			var m map[string]any
			if err := yaml.Unmarshal(raw, &m); err == nil {
				if v, ok := m["kickstart_mac"].(string); ok {
					if canon, err := canonicalMAC(v); err == nil {
						out[canon] = struct{}{}
					}
				}
			}
		}
	}

	return out, nil
}
