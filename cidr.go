package main

import (
	"fmt"
	"net"
)

// parseCIDRNetwork returns the canonical network-address form of a CIDR
// string. UniFi reports network IPSubnet as gateway-IP/prefix
// ("10.0.60.1/24") whereas our inventory uses network-address/prefix
// ("10.0.60.0/24"); normalizing both sides via net.ParseCIDR lets us compare
// network identity rather than string form.
func parseCIDRNetwork(s string) (string, error) {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}
	return n.String(), nil
}
