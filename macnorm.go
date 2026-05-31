package main

import (
	"fmt"
	"strings"
)

// canonicalMAC normalizes any common MAC representation to lowercase
// colon-separated form ("aa:bb:cc:dd:ee:ff"). Accepts dashes, colons, or no
// separator, case-insensitive. Returns errInvalidMAC if the input isn't 12
// hex digits after stripping separators.
func canonicalMAC(s string) (string, error) {
	stripped := strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', ' ', '.':
			return -1
		}
		return r
	}, s)
	if len(stripped) != 12 {
		return "", fmt.Errorf("%w: %q (got %d hex digits, want 12)", errInvalidMAC, s, len(stripped))
	}
	stripped = strings.ToLower(stripped)
	for _, r := range stripped {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", fmt.Errorf("%w: %q (non-hex character %q)", errInvalidMAC, s, r)
		}
	}
	var b strings.Builder
	b.Grow(17)
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(stripped[i : i+2])
	}
	return b.String(), nil
}

// macFilename converts a canonical MAC ("aa:bb:cc:dd:ee:ff") to the on-disk
// filename used in inventory/clients/ ("aa-bb-cc-dd-ee-ff.yml"). Colons are
// filesystem-hostile across some tooling; dashes match the existing node_mac
// form in tynet-infra host_vars.
func macFilename(canonical string) string {
	return strings.ReplaceAll(canonical, ":", "-") + ".yml"
}
