package main

import "errors"

// Sentinel errors. Each is exposed at package scope so tests can use
// errors.Is for classification without coupling to message text.
var (
	errInvalidMAC         = errors.New("invalid MAC address")
	errInvalidVLAN        = errors.New("invalid vlan_id")
	errVLANIPMismatch     = errors.New("vlan_id does not match third octet of ip")
	errFilenameMismatch   = errors.New("filename does not match mac")
	errInvalidIP          = errors.New("invalid ip")
	errInvalidConnection  = errors.New("connection must be wired, wireless, or omitted")
	errMissingAPIKey      = errors.New("UNIFI_API_KEY env var is required")
	errUnknownSubcommand  = errors.New("unknown subcommand")
	errInventoryNotFound  = errors.New("inventory path not found")
	errAllowLargeRequired = errors.New("plan would change more than 25% of clients; pass --allow-large to confirm")
)
