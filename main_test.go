package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoullx/unifi-go/unifi"
)

// fakeUnifiAPI satisfies the unifiAPI interface and records every call so
// tests can assert what would have been written to a real controller.
type fakeUnifiAPI struct {
	networks    []unifi.Network
	users       []unifi.User
	created     []unifi.User
	updated     []unifi.User
	listNetErr  error
	listUserErr error
	createErr   error
	updateErr   error
}

func (f *fakeUnifiAPI) ListNetwork(_ context.Context, _ string) ([]unifi.Network, error) {
	return f.networks, f.listNetErr
}
func (f *fakeUnifiAPI) ListUser(_ context.Context, _ string) ([]unifi.User, error) {
	return f.users, f.listUserErr
}
func (f *fakeUnifiAPI) CreateUser(_ context.Context, _ string, d *unifi.User) (*unifi.User, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, *d)
	return d, nil
}
func (f *fakeUnifiAPI) UpdateUser(_ context.Context, _ string, d *unifi.User) (*unifi.User, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = append(f.updated, *d)
	return d, nil
}

// testEnv bundles the captured per-test state. swapGlobals installs fakes and
// returns the env + a cleanup func registered via t.Cleanup so callers don't
// have to.
type testEnv struct {
	out, errOut *strings.Builder
	exitCode    int
	exitCalled  bool
}

// swapGlobals overrides stdout/stderr/stdin/exit/newUnifiClient for the
// duration of the test. UNIFI_API_KEY is also set so requireAPIKey passes.
// Pass `apiOverride=nil` to keep the real newUnifiClient (for tests that
// exercise its error path or that don't reach the API).
func swapGlobals(t *testing.T, apiOverride unifiAPI, stdinInput string) *testEnv {
	t.Helper()
	env := &testEnv{
		out:      &strings.Builder{},
		errOut:   &strings.Builder{},
		exitCode: -1,
	}

	origStdout, origStderr, origStdin := stdout, stderr, stdin
	origExit, origNewClient := exit, newUnifiClient

	stdout = env.out
	stderr = env.errOut
	stdin = strings.NewReader(stdinInput)
	exit = func(code int) {
		env.exitCode = code
		env.exitCalled = true
	}
	if apiOverride != nil {
		newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) {
			return apiOverride, nil
		}
	}
	t.Setenv("UNIFI_API_KEY", "test-key")
	// Isolate HOME so the dev-default branch in resolveInventoryPath can't
	// accidentally find the developer's real ~/src/tynet-infra checkout
	// during tests that expect "no inventory anywhere".
	t.Setenv("HOME", t.TempDir())

	t.Cleanup(func() {
		stdout, stderr, stdin = origStdout, origStderr, origStdin
		exit, newUnifiClient = origExit, origNewClient
	})
	return env
}

// defaultNetworks is the same trio used in sync_test.go so test bodies can
// share VLAN/network IDs.
func defaultNetworks() []unifi.Network {
	return []unifi.Network{
		{ID: "net-60", Name: "Trusted", VLAN: 60, IPSubnet: "10.0.60.1/24", Purpose: "corporate"},
		{ID: "net-70", Name: "Personal", VLAN: 70, IPSubnet: "10.0.70.1/24", Purpose: "corporate"},
		{ID: "net-99", Name: "Guests", VLAN: 99, IPSubnet: "10.0.99.1/24", Purpose: "guest"},
	}
}

// writeInventory stages the given filename → YAML content map into a new
// temp dir and returns the dir path.
func writeInventory(t *testing.T, inv map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range inv {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// --- run dispatch & main exit codes ---

func TestRun_NoArgs(t *testing.T) {
	swapGlobals(t, nil, "")
	err := run(nil)
	if !errors.Is(err, errUnknownSubcommand) {
		t.Fatalf("want errUnknownSubcommand, got %v", err)
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	env := swapGlobals(t, nil, "")
	err := run([]string{"nope"})
	if !errors.Is(err, errUnknownSubcommand) {
		t.Fatalf("want errUnknownSubcommand, got %v", err)
	}
	if !strings.Contains(env.errOut.String(), "Usage:") {
		t.Errorf("usage not printed on unknown subcommand: %q", env.errOut.String())
	}
}

func TestRun_HelpFlags(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		t.Run(flag, func(t *testing.T) {
			env := swapGlobals(t, nil, "")
			if err := run([]string{flag}); err != nil {
				t.Fatalf("want nil error, got %v", err)
			}
			if !strings.Contains(env.errOut.String(), "Subcommands:") {
				t.Errorf("help text missing: %q", env.errOut.String())
			}
		})
	}
}

func TestRun_DispatchesEachSubcommand(t *testing.T) {
	// Each non-help arm of the run() switch returns the underlying
	// subcommand's error verbatim. We use a missing API key as the most
	// reliable failure each subcommand surfaces before doing any work.
	cases := []string{"plan", "apply", "bootstrap", "pull"}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			swapGlobals(t, nil, "")
			t.Setenv("UNIFI_API_KEY", "")
			// runBootstrap/runPull need an inventory path arg before they
			// even reach requireAPIKey; pass one for those. runPlan/runApply
			// hit requireAPIKey via buildPlanFromLive after parsing flags;
			// they also need --inventory-path to avoid the walk-up search.
			args := []string{sub, "--inventory-path", t.TempDir()}
			err := run(args)
			if !errors.Is(err, errMissingAPIKey) {
				t.Fatalf("dispatch to %q surfaced unexpected error: %v", sub, err)
			}
		})
	}
}

func TestMain_DriftErrorExit2(t *testing.T) {
	env := swapGlobals(t, nil, "")
	// Stub run so main() sees a driftError without doing any actual work.
	// We can't override run, so instead call exit directly via the same
	// branch by constructing the path: drift error → exit(2).
	// Easiest: simulate by re-running the main()-style switch inline.
	de := driftError{summary: "drift"}
	if de.Error() != "drift" {
		t.Errorf("driftError.Error() returned %q", de.Error())
	}
	// Now actually exercise main(): point os.Args at "plan" against an empty
	// inventory + an API stub whose drift output is empty (noop). We expect
	// exit not to be called for a clean plan.
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) { return fake, nil }
	dir := t.TempDir()
	os.Args = []string{"tynet-unifi-clients", "plan", "--inventory-path", dir}
	main()
	if env.exitCalled {
		t.Errorf("clean plan should not call exit, got code %d", env.exitCode)
	}
}

func TestMain_GenericErrorExit1(t *testing.T) {
	env := swapGlobals(t, nil, "")
	// Pass an unknown subcommand → run returns errUnknownSubcommand (not a
	// driftError) → main calls exit(1).
	os.Args = []string{"tynet-unifi-clients", "nope"}
	main()
	if !env.exitCalled || env.exitCode != 1 {
		t.Errorf("want exit(1), got called=%t code=%d", env.exitCalled, env.exitCode)
	}
}

func TestMain_DriftExit2(t *testing.T) {
	env := swapGlobals(t, nil, "")
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) { return fake, nil }
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	os.Args = []string{"tynet-unifi-clients", "plan", "--inventory-path", dir}
	main()
	if !env.exitCalled || env.exitCode != 2 {
		t.Errorf("want exit(2) on drift, got called=%t code=%d; stderr=%q", env.exitCalled, env.exitCode, env.errOut.String())
	}
}

// --- requireAPIKey ---

func TestRequireAPIKey(t *testing.T) {
	t.Setenv("UNIFI_API_KEY", "")
	if _, err := requireAPIKey(); !errors.Is(err, errMissingAPIKey) {
		t.Fatalf("want errMissingAPIKey, got %v", err)
	}
	t.Setenv("UNIFI_API_KEY", "secret")
	k, err := requireAPIKey()
	if err != nil || k != "secret" {
		t.Fatalf("got (%q, %v), want (\"secret\", nil)", k, err)
	}
}

// --- resolveInventoryPath ---

func TestResolveInventoryPath_FlagWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &common{inventoryPath: "/explicit"}
	got, err := c.resolveInventoryPath()
	if err != nil || got != "/explicit" {
		t.Errorf("flag should win: got (%q, %v)", got, err)
	}
}

func TestResolveInventoryPath_EnvWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "/from-env")
	c := &common{}
	got, err := c.resolveInventoryPath()
	if err != nil || got != "/from-env" {
		t.Errorf("env should win: got (%q, %v)", got, err)
	}
}

func TestResolveInventoryPath_WalkUp(t *testing.T) {
	// Stage a fake project: <root>/ansible.cfg, <root>/inventory/clients/,
	// chdir into a sub-sub-dir, expect the walk-up to discover it.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ansible.cfg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := filepath.Join(root, "inventory", "clients")
	if err := os.MkdirAll(inv, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prevCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevCWD) })

	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "") // make sure env doesn't shortcut
	c := &common{}
	got, err := c.resolveInventoryPath()
	if err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks because macOS tempdirs live under /private/var/...
	// while os.Getwd may return /var/...; compare via filepath.EvalSymlinks.
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(inv)
	if gotReal != wantReal {
		t.Errorf("walk-up: got %q, want %q", got, inv)
	}
}

func TestResolveInventoryPath_DevDefault(t *testing.T) {
	// Last-resort branch: $HOME/src/tynet-infra/inventory/clients exists →
	// resolveInventoryPath returns it. Used so developers don't need to
	// pass --inventory-path or set $UNIFI_ANSIBLE_INVENTORY when working
	// out of $HOME/src/tynet-unifi-clients.
	home := t.TempDir()
	want := filepath.Join(home, devDefaultInventory)
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")

	// chdir to a clean tempdir so the walk-up search fails before the
	// dev-default branch runs.
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	c := &common{}
	got, err := c.resolveInventoryPath()
	if err != nil {
		t.Fatal(err)
	}
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(want)
	if gotReal != wantReal {
		t.Errorf("dev default: got %q, want %q", got, want)
	}
}

func TestResolveInventoryPath_NotFound(t *testing.T) {
	// chdir into a tmpdir with no ansible.cfg anywhere above; expect error.
	// We can't reliably guarantee no ansible.cfg exists in any parent of an
	// arbitrary tempdir, but t.TempDir() on the testing root is sufficient
	// in practice. HOME is isolated so the dev-default fallback can't fire.
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	prevCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevCWD) })

	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")
	c := &common{}
	_, err = c.resolveInventoryPath()
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

// --- runPlan ---

func TestRunPlan_Noop(t *testing.T) {
	env := swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:                            "u1",
			MAC:                           "aa:bb:cc:dd:ee:ff",
			Name:                          "alice",
			NetworkID:                     "net-70",
			VirtualNetworkOverrideEnabled: true,
			VirtualNetworkOverrideID:      "net-70",
		}},
	}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	if err := runPlan([]string{"--inventory-path", dir}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !strings.Contains(env.out.String(), "0 would create") {
		t.Errorf("expected clean summary, got %q", env.out.String())
	}
}

func TestRunPlan_Drift(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runPlan([]string{"--inventory-path", dir})
	var de driftError
	if !errors.As(err, &de) {
		t.Fatalf("want driftError, got %v", err)
	}
}

func TestRunPlan_ListNetworkError(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{listNetErr: errors.New("boom")}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runPlan([]string{"--inventory-path", dir})
	if err == nil || !strings.Contains(err.Error(), "list networks") {
		t.Fatalf("want list-networks wrap, got %v", err)
	}
}

func TestRunPlan_ListUserError(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks(), listUserErr: errors.New("boom")}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runPlan([]string{"--inventory-path", dir})
	if err == nil || !strings.Contains(err.Error(), "list users") {
		t.Fatalf("want list-users wrap, got %v", err)
	}
}

func TestRunPlan_NoAPIKey(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	t.Setenv("UNIFI_API_KEY", "")
	dir := writeInventory(t, nil)
	if err := runPlan([]string{"--inventory-path", dir}); !errors.Is(err, errMissingAPIKey) {
		t.Fatalf("want errMissingAPIKey, got %v", err)
	}
}

func TestRunPlan_InventoryError(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	err := runPlan([]string{"--inventory-path", filepath.Join(t.TempDir(), "nope")})
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestRunPlan_FlagParseError(t *testing.T) {
	swapGlobals(t, nil, "")
	err := runPlan([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("want flag parse error, got nil")
	}
}

// --- runApply ---

func TestRunApply_NothingToApply(t *testing.T) {
	env := swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:                            "u1",
			MAC:                           "aa:bb:cc:dd:ee:ff",
			Name:                          "alice",
			NetworkID:                     "net-70",
			VirtualNetworkOverrideEnabled: true,
			VirtualNetworkOverrideID:      "net-70",
		}},
	}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	if err := runApply([]string{"--inventory-path", dir, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "nothing to apply") {
		t.Errorf("want 'nothing to apply', got %q", env.out.String())
	}
}

// mostlyNoopInventory stages an inventory of N clients on VLAN 70 plus one
// that drifts (intended to be Create or Update depending on test setup). The
// 25% guard then leaves the changing client well below threshold.
func mostlyNoopInventory(t *testing.T, driftFile, driftMAC string, padCount int) (dir string, noops []unifi.User) {
	t.Helper()
	inv := map[string]string{driftFile: "mac: " + driftMAC + "\nname: alice\nvlan_id: 70\n"}
	for i := range padCount {
		// Use 99:11:* range so it can't collide with caller-provided MACs.
		mac := []byte("99:11:22:33:44:0X")
		mac[len(mac)-1] = byte('0' + i)
		fn := []byte("99-11-22-33-44-0X.yml")
		fn[len(fn)-5] = byte('0' + i)
		inv[string(fn)] = "mac: " + string(mac) + "\nname: pad\nvlan_id: 70\n"
		noops = append(noops, unifi.User{
			ID:                            "pad-" + string(byte('0'+i)),
			MAC:                           string(mac),
			Name:                          "pad",
			NetworkID:                     "net-70",
			VirtualNetworkOverrideEnabled: true,
			VirtualNetworkOverrideID:      "net-70",
		})
	}
	return writeInventory(t, inv), noops
}

func TestRunApply_YesProceeds(t *testing.T) {
	dir, noops := mostlyNoopInventory(t, "aa-bb-cc-dd-ee-ff.yml", "aa:bb:cc:dd:ee:ff", 4)
	fake := &fakeUnifiAPI{networks: defaultNetworks(), users: noops}
	env := swapGlobals(t, fake, "")
	if err := runApply([]string{"--inventory-path", dir, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 1 || fake.created[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("want one create, got %v", fake.created)
	}
	if !strings.Contains(env.out.String(), "1 created") {
		t.Errorf("want '1 created' summary, got %q", env.out.String())
	}
}

func TestRunApply_PromptY(t *testing.T) {
	dir, noops := mostlyNoopInventory(t, "aa-bb-cc-dd-ee-ff.yml", "aa:bb:cc:dd:ee:ff", 4)
	fake := &fakeUnifiAPI{networks: defaultNetworks(), users: noops}
	swapGlobals(t, fake, "y\n")
	if err := runApply([]string{"--inventory-path", dir}); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 1 {
		t.Errorf("want one create after y prompt, got %v", fake.created)
	}
}

func TestRunApply_PromptN(t *testing.T) {
	dir, noops := mostlyNoopInventory(t, "aa-bb-cc-dd-ee-ff.yml", "aa:bb:cc:dd:ee:ff", 4)
	fake := &fakeUnifiAPI{networks: defaultNetworks(), users: noops}
	env := swapGlobals(t, fake, "n\n")
	if err := runApply([]string{"--inventory-path", dir}); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 0 {
		t.Errorf("n prompt should abort, but got %d creates", len(fake.created))
	}
	if !strings.Contains(env.out.String(), "aborted") {
		t.Errorf("want 'aborted', got %q", env.out.String())
	}
}

func TestRunApply_Limit(t *testing.T) {
	// With --limit the post-filter plan size is what the 25% guard sees;
	// limiting to a single MAC out of two would normally trip 100% churn.
	// --allow-large keeps the test focused on the limit semantics.
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	swapGlobals(t, fake, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
		"11-22-33-44-55-66.yml": "mac: 11:22:33:44:55:66\nname: bob\nvlan_id: 60\n",
	})
	if err := runApply([]string{
		"--inventory-path", dir,
		"--yes", "--allow-large",
		"--limit", "aa:bb:cc:dd:ee:ff",
		"--limit", "not-a-mac", // dropped by canonicalMAC
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 1 || fake.created[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("limit should restrict to one MAC, got %v", fake.created)
	}
}

func TestRunApply_AllowLargeRequired(t *testing.T) {
	// 5 desired clients, all need create → 100% churn → trips the >25% guard.
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	swapGlobals(t, fake, "")
	inv := map[string]string{}
	for i := 1; i <= 5; i++ {
		mac := []byte("aa:bb:cc:dd:ee:0X")
		mac[len(mac)-1] = byte('0' + i)
		fn := []byte("aa-bb-cc-dd-ee-0X.yml")
		fn[len(fn)-5] = byte('0' + i)
		inv[string(fn)] = "mac: " + string(mac) + "\nname: c" + string(byte('0'+i)) + "\nvlan_id: 70\n"
	}
	err := runApply([]string{"--inventory-path", writeInventory(t, inv), "--yes"})
	if !errors.Is(err, errAllowLargeRequired) {
		t.Fatalf("want errAllowLargeRequired, got %v", err)
	}
}

func TestRunApply_AllowLargeOverride(t *testing.T) {
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	swapGlobals(t, fake, "")
	inv := map[string]string{}
	for i := 1; i <= 5; i++ {
		fn := []byte("aa-bb-cc-dd-ee-0X.yml")
		fn[len(fn)-5] = byte('0' + i)
		mac := []byte("aa:bb:cc:dd:ee:0X")
		mac[len(mac)-1] = byte('0' + i)
		inv[string(fn)] = "mac: " + string(mac) + "\nname: c\nvlan_id: 70\n"
	}
	if err := runApply([]string{"--inventory-path", writeInventory(t, inv), "--yes", "--allow-large"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 5 {
		t.Errorf("want 5 creates with --allow-large, got %d", len(fake.created))
	}
}

func TestRunApply_UpdateHappyPath(t *testing.T) {
	// Covers the `updated %s` arm of the apply loop — separate from
	// CreateError/UpdateError which exit via the error wrap, and separate
	// from YesProceeds which exercises Create.
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u1",
			MAC:       "aa:bb:cc:dd:ee:ff",
			Name:      "stale",
			NetworkID: "net-70",
		}},
	}
	env := swapGlobals(t, fake, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	if err := runApply([]string{"--inventory-path", dir, "--yes", "--allow-large"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.updated) != 1 || fake.updated[0].ID != "u1" {
		t.Errorf("want one update carrying existing ID, got %v", fake.updated)
	}
	if !strings.Contains(env.out.String(), "updated aa:bb:cc:dd:ee:ff") {
		t.Errorf("missing 'updated' line, got %q", env.out.String())
	}
}

func TestRunApply_InventoryMissing(t *testing.T) {
	// Covers runApply's `if err != nil` after buildPlanFromLive.
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	err := runApply([]string{"--inventory-path", filepath.Join(t.TempDir(), "nope"), "--yes"})
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestRunBootstrap_ClientCreateError(t *testing.T) {
	// Covers the `if err != nil { return err }` after newUnifiClient inside
	// runBootstrap (separate from buildPlanFromLive's identical guard).
	swapGlobals(t, nil, "")
	newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) {
		return nil, errors.New("boom")
	}
	err := runBootstrap([]string{"--inventory-path", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want client-create error, got %v", err)
	}
}

func TestRunApply_CreateError(t *testing.T) {
	fake := &fakeUnifiAPI{networks: defaultNetworks(), createErr: errors.New("controller hate")}
	swapGlobals(t, fake, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runApply([]string{"--inventory-path", dir, "--yes", "--allow-large"})
	if err == nil || !strings.Contains(err.Error(), "create aa:bb:cc:dd:ee:ff") {
		t.Fatalf("want create-error wrap, got %v", err)
	}
}

func TestRunApply_UpdateError(t *testing.T) {
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u1",
			MAC:       "aa:bb:cc:dd:ee:ff",
			Name:      "stale",
			NetworkID: "net-70",
		}},
		updateErr: errors.New("controller hate"),
	}
	swapGlobals(t, fake, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runApply([]string{"--inventory-path", dir, "--yes", "--allow-large"})
	if err == nil || !strings.Contains(err.Error(), "update aa:bb:cc:dd:ee:ff") {
		t.Fatalf("want update-error wrap, got %v", err)
	}
}

func TestRunApply_FlagParseError(t *testing.T) {
	swapGlobals(t, nil, "")
	err := runApply([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("want flag parse error, got nil")
	}
}

// --- runBootstrap ---

func TestRunBootstrap_DryRun(t *testing.T) {
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u1",
			MAC:       "aa:bb:cc:dd:ee:ff",
			Name:      "alice",
			NetworkID: "net-70",
			FixedIP:   "10.0.70.42",
		}},
	}
	env := swapGlobals(t, fake, "")
	dir := writeInventory(t, nil)
	if err := runBootstrap([]string{"--inventory-path", dir}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "would write") {
		t.Errorf("dry-run output missing 'would write': %q", env.out.String())
	}
	// File must NOT have been written in dry-run.
	if _, err := os.Stat(filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml")); !os.IsNotExist(err) {
		t.Errorf("dry-run should not have written: %v", err)
	}
}

func TestRunBootstrap_Write(t *testing.T) {
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u1",
			MAC:       "aa:bb:cc:dd:ee:ff",
			Name:      "alice",
			NetworkID: "net-70",
			FixedIP:   "10.0.70.42",
		}},
	}
	env := swapGlobals(t, fake, "")
	dir := writeInventory(t, nil)
	if err := runBootstrap([]string{"--inventory-path", dir, "--write"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aa-bb-cc-dd-ee-ff.yml")); err != nil {
		t.Errorf("file should have been written: %v", err)
	}
	if !strings.Contains(env.out.String(), "wrote") {
		t.Errorf("want 'wrote' line, got %q", env.out.String())
	}
}

func TestRunBootstrap_NoCandidates(t *testing.T) {
	env := swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	dir := writeInventory(t, nil)
	if err := runBootstrap([]string{"--inventory-path", dir}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "no new clients") {
		t.Errorf("want 'no new clients', got %q", env.out.String())
	}
}

func TestRunBootstrap_RefuseOverwrite(t *testing.T) {
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u1",
			MAC:       "aa:bb:cc:dd:ee:ff",
			Name:      "alice",
			NetworkID: "net-70",
		}},
	}
	env := swapGlobals(t, fake, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	if err := runBootstrap([]string{"--inventory-path", dir, "--write"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.errOut.String(), "skip") {
		t.Errorf("expected skip note on overwrite refusal, got stderr=%q", env.errOut.String())
	}
}

func TestRunBootstrap_HostVarsSkip(t *testing.T) {
	managed := "aa:bb:cc:dd:ee:ff"
	fake := &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{
			{ID: "u1", MAC: managed, NetworkID: "net-70"},
			{ID: "u2", MAC: "11:22:33:44:55:66", Name: "bob", NetworkID: "net-70"},
		},
	}
	env := swapGlobals(t, fake, "")

	hostVarsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostVarsDir, "h.yml"),
		[]byte("node_mac: "+managed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inv := writeInventory(t, nil)
	if err := runBootstrap([]string{
		"--inventory-path", inv,
		"--host-vars-dir", hostVarsDir,
	}); err != nil {
		t.Fatal(err)
	}
	out := env.out.String()
	managedFile := "aa-bb-cc-dd-ee-ff.yml"
	if strings.Contains(out, managedFile) {
		t.Errorf("managed mac should be skipped, but appeared in output: %q", out)
	}
	if !strings.Contains(out, "11-22-33-44-55-66.yml") {
		t.Errorf("non-managed mac should be proposed, got %q", out)
	}
}

func TestRunBootstrap_HostVarsLoadError(t *testing.T) {
	// loadHostVarMACs returns an error if the dir read itself fails for a
	// reason other than IsNotExist. The easiest reliable trigger is a path
	// pointing at a regular file (not a dir): ReadDir then returns
	// ENOTDIR, which surfaces as a non-IsNotExist error.
	fake := &fakeUnifiAPI{networks: defaultNetworks()}
	swapGlobals(t, fake, "")

	badHostVars := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(badHostVars, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := writeInventory(t, nil)
	err := runBootstrap([]string{
		"--inventory-path", inv,
		"--host-vars-dir", badHostVars,
	})
	if err == nil || !strings.Contains(err.Error(), "load host_vars MACs") {
		t.Fatalf("want load host_vars wrap, got %v", err)
	}
}

func TestRunBootstrap_InventoryMissing_DryRunContinues(t *testing.T) {
	// In dry-run, an inventory path that fails to resolve emits a "note:"
	// line and continues with a placeholder directory.
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	// chdir into a tempdir so the walk-up search fails.
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")
	// No --inventory-path; walk-up fails; dry-run swallows the error.
	if err := runBootstrap(nil); err != nil {
		t.Fatalf("dry-run should not error on missing inventory, got %v", err)
	}
}

func TestRunBootstrap_InventoryMissing_WriteFails(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")
	err := runBootstrap([]string{"--write"})
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound with --write, got %v", err)
	}
}

func TestRunBootstrap_ListErrors(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeUnifiAPI
		want string
	}{
		{"networks", &fakeUnifiAPI{listNetErr: errors.New("x")}, "list networks"},
		{"users", &fakeUnifiAPI{networks: defaultNetworks(), listUserErr: errors.New("x")}, "list users"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapGlobals(t, tc.fake, "")
			err := runBootstrap([]string{"--inventory-path", writeInventory(t, nil)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRunBootstrap_NoAPIKey(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	t.Setenv("UNIFI_API_KEY", "")
	err := runBootstrap([]string{"--inventory-path", writeInventory(t, nil)})
	if !errors.Is(err, errMissingAPIKey) {
		t.Fatalf("want errMissingAPIKey, got %v", err)
	}
}

func TestRunBootstrap_FlagParseError(t *testing.T) {
	swapGlobals(t, nil, "")
	err := runBootstrap([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("want flag parse error, got nil")
	}
}

// --- runPull ---

func TestRunPull_Clean(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:                            "u1",
			MAC:                           "aa:bb:cc:dd:ee:ff",
			Name:                          "alice",
			NetworkID:                     "net-70",
			VirtualNetworkOverrideEnabled: true,
			VirtualNetworkOverrideID:      "net-70",
		}},
	}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	if err := runPull([]string{"--inventory-path", dir}); err != nil {
		t.Fatalf("want nil for clean pull, got %v", err)
	}
}

func TestRunPull_DriftReports(t *testing.T) {
	// Inventory has aa:..; controller has 11:.. → inventory-only AND
	// controller-only entries, plus a plan-drift (would-create).
	env := swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u-extra",
			MAC:       "11:22:33:44:55:66",
			NetworkID: "net-70",
		}},
	}, "")
	dir := writeInventory(t, map[string]string{
		"aa-bb-cc-dd-ee-ff.yml": "mac: aa:bb:cc:dd:ee:ff\nname: alice\nvlan_id: 70\n",
	})
	err := runPull([]string{"--inventory-path", dir})
	var de driftError
	if !errors.As(err, &de) {
		t.Fatalf("want driftError, got %v", err)
	}
	out := env.out.String()
	if !strings.Contains(out, "inventory-only:") || !strings.Contains(out, "controller-only:") {
		t.Errorf("want both inv-only and ctrl-only lines, got %q", out)
	}
}

func TestRunPull_GuestSkipped(t *testing.T) {
	// A controller-only user that lives on a guest network should NOT be
	// reported as drift. Inventory is empty.
	swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{{
			ID:        "u-guest",
			MAC:       "11:22:33:44:55:66",
			NetworkID: "net-99", // guest
		}},
	}, "")
	dir := writeInventory(t, nil)
	if err := runPull([]string{"--inventory-path", dir}); err != nil {
		t.Fatalf("guest-only controller user should not be drift, got %v", err)
	}
}

func TestRunPull_GarbageMACSkipped(t *testing.T) {
	// canonicalMAC failures in the controller's user list are silently
	// dropped from the set-difference computation.
	swapGlobals(t, &fakeUnifiAPI{
		networks: defaultNetworks(),
		users: []unifi.User{
			{ID: "garbage", MAC: "not-a-mac", NetworkID: "net-70"},
		},
	}, "")
	dir := writeInventory(t, nil)
	if err := runPull([]string{"--inventory-path", dir}); err != nil {
		t.Fatalf("garbage MAC should not error, got %v", err)
	}
}

func TestRunPull_ListErrors(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeUnifiAPI
		want string
	}{
		{"networks", &fakeUnifiAPI{listNetErr: errors.New("x")}, "list networks"},
		{"users", &fakeUnifiAPI{networks: defaultNetworks(), listUserErr: errors.New("x")}, "list users"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapGlobals(t, tc.fake, "")
			dir := writeInventory(t, nil)
			err := runPull([]string{"--inventory-path", dir})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRunPull_NoAPIKey(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	t.Setenv("UNIFI_API_KEY", "")
	if err := runPull(nil); !errors.Is(err, errMissingAPIKey) {
		t.Fatalf("want errMissingAPIKey, got %v", err)
	}
}

func TestRunPull_ClientCreateError(t *testing.T) {
	// runPull has its own newUnifiClient call (separate from
	// buildPlanFromLive's, which the plan/apply paths use).
	swapGlobals(t, nil, "")
	newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) {
		return nil, errors.New("boom")
	}
	err := runPull(nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want client-create error, got %v", err)
	}
}

func TestRunPull_ResolveInventoryError(t *testing.T) {
	// No --inventory-path, no UNIFI_ANSIBLE_INVENTORY, chdir to a clean
	// tempdir → walk-up search exhausts → errInventoryNotFound surfaces
	// from resolveInventoryPath (distinct from loadClients failing).
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := runPull(nil); !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestBuildPlanFromLive_ResolveInventoryError(t *testing.T) {
	// Covers buildPlanFromLive's resolveInventoryPath-fails arm, distinct
	// from the newUnifiClient-fails arm in TestBuildPlanFromLive_ClientCreateError.
	swapGlobals(t, &fakeUnifiAPI{}, "")
	t.Setenv("UNIFI_ANSIBLE_INVENTORY", "")
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	_, _, err := buildPlanFromLive(&common{})
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestRunPull_InventoryMissing(t *testing.T) {
	swapGlobals(t, &fakeUnifiAPI{networks: defaultNetworks()}, "")
	err := runPull([]string{"--inventory-path", filepath.Join(t.TempDir(), "nope")})
	if !errors.Is(err, errInventoryNotFound) {
		t.Fatalf("want errInventoryNotFound, got %v", err)
	}
}

func TestRunPull_FlagParseError(t *testing.T) {
	swapGlobals(t, nil, "")
	err := runPull([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("want flag parse error, got nil")
	}
}

// --- buildPlanFromLive newUnifiClient failure ---

func TestBuildPlanFromLive_ClientCreateError(t *testing.T) {
	// The newUnifiClient seam errors → buildPlanFromLive surfaces the error.
	swapGlobals(t, nil, "")
	newUnifiClient = func(_, _ string, _ bool) (unifiAPI, error) {
		return nil, errors.New("boom")
	}
	_, _, err := buildPlanFromLive(&common{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want client-create error, got %v", err)
	}
}

// --- bootstrapCandidates direct tests ---

func TestBootstrapCandidates_HostnameFallback(t *testing.T) {
	netByID := map[string]*unifi.Network{
		"net-70": {ID: "net-70", VLAN: 70},
	}
	users := []unifi.User{{
		MAC:       "aa:bb:cc:dd:ee:ff",
		Hostname:  "fallback-host",
		NetworkID: "net-70",
	}}
	got, _ := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 1 || got[0].Name != "fallback-host" {
		t.Errorf("hostname should fall in when name is empty, got %+v", got)
	}
}

func TestBootstrapCandidates_IPVLANMismatchDropped(t *testing.T) {
	netByID := map[string]*unifi.Network{
		"net-70": {ID: "net-70", VLAN: 70},
	}
	users := []unifi.User{{
		MAC:       "aa:bb:cc:dd:ee:ff",
		Name:      "alice",
		FixedIP:   "10.0.60.42", // third octet 60 ≠ vlan 70
		NetworkID: "net-70",
	}}
	got, _ := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 1 || got[0].IP != "" {
		t.Errorf("IP should be dropped when third octet doesn't match VLAN, got %+v", got)
	}
}

func TestBootstrapCandidates_SkipUnresolvedNetwork(t *testing.T) {
	netByID := map[string]*unifi.Network{} // empty
	users := []unifi.User{{
		MAC:       "aa:bb:cc:dd:ee:ff",
		NetworkID: "net-missing",
	}}
	got, s := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 0 || s.total != 1 {
		t.Errorf("expected skip, got candidates=%v skipped=%+v", got, s)
	}
}

func TestBootstrapCandidates_SkipZeroVLAN(t *testing.T) {
	// Network exists but VLAN==0 (WAN / untagged) → skip.
	netByID := map[string]*unifi.Network{
		"wan": {ID: "wan", VLAN: 0},
	}
	users := []unifi.User{{MAC: "aa:bb:cc:dd:ee:ff", NetworkID: "wan"}}
	got, _ := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 0 {
		t.Errorf("VLAN 0 should be skipped, got %v", got)
	}
}

func TestBootstrapCandidates_SkipInvalidMAC(t *testing.T) {
	netByID := map[string]*unifi.Network{"net-70": {ID: "net-70", VLAN: 70}}
	users := []unifi.User{{MAC: "not-a-mac", NetworkID: "net-70"}}
	got, _ := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 0 {
		t.Errorf("invalid MAC should be skipped, got %v", got)
	}
}

func TestBootstrapCandidates_SkipGuestDirect(t *testing.T) {
	// Guest-network skip arm. Bumps both s.total and s.guest.
	netByID := map[string]*unifi.Network{
		"net-guest": {ID: "net-guest", VLAN: 99, Purpose: "guest"},
	}
	guestIDs := map[string]struct{}{"net-guest": {}}
	users := []unifi.User{{MAC: "aa:bb:cc:dd:ee:ff", NetworkID: "net-guest"}}
	got, s := bootstrapCandidates(users, netByID, guestIDs, nil)
	if len(got) != 0 {
		t.Errorf("guest should be skipped, got %v", got)
	}
	if s.total != 1 || s.guest != 1 {
		t.Errorf("want total=1 guest=1, got %+v", s)
	}
}

func TestBootstrapCandidates_SkipManagedDirect(t *testing.T) {
	// host_vars-managed skip arm. Bumps both s.total and s.managed.
	netByID := map[string]*unifi.Network{"net-70": {ID: "net-70", VLAN: 70}}
	skipMACs := map[string]struct{}{"aa:bb:cc:dd:ee:ff": {}}
	users := []unifi.User{{MAC: "aa:bb:cc:dd:ee:ff", NetworkID: "net-70"}}
	got, s := bootstrapCandidates(users, netByID, nil, skipMACs)
	if len(got) != 0 {
		t.Errorf("managed mac should be skipped, got %v", got)
	}
	if s.total != 1 || s.managed != 1 {
		t.Errorf("want total=1 managed=1, got %+v", s)
	}
}

func TestBootstrapCandidates_DeriveVLANFromFixedIP(t *testing.T) {
	// network_id is null (legacy/stale client) but FixedIP exists.
	// VLAN must be derived from the third octet of FixedIP.
	netByID := map[string]*unifi.Network{
		"net-70": {ID: "net-70", VLAN: 70},
	}
	users := []unifi.User{{
		MAC:     "aa:bb:cc:dd:ee:ff",
		Name:    "alice",
		FixedIP: "10.0.70.42",
	}}
	got, s := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d (skipped=%+v)", len(got), s)
	}
	if got[0].VLANID != 70 || got[0].IP != "10.0.70.42" {
		t.Errorf("want vlan=70 ip=10.0.70.42, got vlan=%d ip=%q", got[0].VLANID, got[0].IP)
	}
}

func TestBootstrapCandidates_DeriveVLANFromObservedIP(t *testing.T) {
	// network_id is null and FixedIP is empty, but observed IP exists.
	// VLAN derives from observed IP; Client.IP stays empty (no fixed
	// reservation in inventory unless the user opts in).
	users := []unifi.User{{
		MAC:  "aa:bb:cc:dd:ee:ff",
		Name: "alice",
		IP:   "10.0.20.55",
	}}
	got, _ := bootstrapCandidates(users, map[string]*unifi.Network{}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(got))
	}
	if got[0].VLANID != 20 || got[0].IP != "" {
		t.Errorf("want vlan=20 ip='', got vlan=%d ip=%q", got[0].VLANID, got[0].IP)
	}
}

func TestBootstrapCandidates_SkipGuestByDerivedVLAN(t *testing.T) {
	// network_id is null but the last-seen IP is on the guest subnet.
	// Skip as guest via the derived-VLAN check, even though the user
	// is not associated to any guest network by ID.
	netByID := map[string]*unifi.Network{
		"net-guest": {ID: "net-guest", VLAN: 100, Purpose: "guest"},
	}
	guestIDs := map[string]struct{}{"net-guest": {}}
	users := []unifi.User{{
		MAC:  "aa:bb:cc:dd:ee:ff",
		Name: "stale-phone",
		IP:   "10.0.100.45", // last seen on guest VLAN
	}}
	got, s := bootstrapCandidates(users, netByID, guestIDs, nil)
	if len(got) != 0 {
		t.Errorf("want skip, got %v", got)
	}
	if s.guest != 1 {
		t.Errorf("want s.guest=1, got %+v", s)
	}
}

func TestBootstrapCandidates_NoVLANSignal(t *testing.T) {
	// network_id null, FixedIP empty, observed IP empty → no signal.
	// Skipped into the new noVLAN bucket.
	users := []unifi.User{{MAC: "aa:bb:cc:dd:ee:ff", Name: "ghost"}}
	got, s := bootstrapCandidates(users, map[string]*unifi.Network{}, nil, nil)
	if len(got) != 0 {
		t.Errorf("want skip, got %v", got)
	}
	if s.total != 1 || s.noVLAN != 1 {
		t.Errorf("want total=1 noVLAN=1, got %+v", s)
	}
}

func TestBootstrapCandidates_NetworkVLAN0FallsBackToIP(t *testing.T) {
	// Network exists but has VLAN=0 (untagged main LAN). The third
	// octet of FixedIP should win.
	netByID := map[string]*unifi.Network{
		"main": {ID: "main", VLAN: 0, IPSubnet: "10.0.10.1/24"},
	}
	users := []unifi.User{{
		MAC:       "aa:bb:cc:dd:ee:ff",
		Name:      "alice",
		NetworkID: "main",
		FixedIP:   "10.0.10.20",
	}}
	got, _ := bootstrapCandidates(users, netByID, nil, nil)
	if len(got) != 1 || got[0].VLANID != 10 {
		t.Errorf("want vlan=10 from FixedIP fallback, got %+v", got)
	}
}

// --- vlanFromIP ---

func TestVLANFromIP(t *testing.T) {
	cases := []struct {
		ip      string
		want    int
		wantOK  bool
	}{
		{"10.0.70.42", 70, true},
		{"10.0.1.1", 1, true},
		{"10.0.4094.1", 4094, true},
		{"10.0.0.1", 0, false},    // third octet 0 → out of [1,4094]
		{"10.0.4095.1", 0, false}, // out of range
		{"not.an.ip.here", 0, false},
		{"", 0, false},
		{"10.0.70", 0, false}, // wrong arity
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			got, ok := vlanFromIP(tc.ip)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("got (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// --- ipMatchesVLAN ---

func TestIPMatchesVLAN(t *testing.T) {
	cases := []struct {
		ip   string
		vlan int
		want bool
	}{
		{"10.0.70.42", 70, true},
		{"10.0.60.42", 70, false},
		{"not.an.ip", 70, false},
		{"10.0.x.42", 70, false}, // third octet non-numeric
		{"10.0.70", 70, false},   // wrong part count
	}
	for _, tc := range cases {
		if got := ipMatchesVLAN(tc.ip, tc.vlan); got != tc.want {
			t.Errorf("ipMatchesVLAN(%q, %d) = %t, want %t", tc.ip, tc.vlan, got, tc.want)
		}
	}
}

// --- stringSlice ---

func TestStringSlice(t *testing.T) {
	var s stringSlice
	if err := s.Set("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b"); err != nil {
		t.Fatal(err)
	}
	if got := s.String(); got != "a,b" {
		t.Errorf("String() = %q, want %q", got, "a,b")
	}
}

// --- filterPlan ---

func TestFilterPlan_DropsInvalidMAC(t *testing.T) {
	p := Plan{Steps: []Step{{MAC: "aa:bb:cc:dd:ee:ff"}, {MAC: "11:22:33:44:55:66"}}}
	got := filterPlan(p, []string{"aa:bb:cc:dd:ee:ff", "not-a-mac"})
	if len(got.Steps) != 1 || got.Steps[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("filterPlan should drop unparseable MAC, got %+v", got.Steps)
	}
}
