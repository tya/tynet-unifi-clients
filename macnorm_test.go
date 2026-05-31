package main

import (
	"errors"
	"testing"
)

func TestCanonicalMAC(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff", false},
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", false},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", false},
		{"AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff", false},
		{"aabbccddeeff", "aa:bb:cc:dd:ee:ff", false},
		{"AABBCCDDEEFF", "aa:bb:cc:dd:ee:ff", false},
		{"dc-a6-32-8d-f3-ca", "dc:a6:32:8d:f3:ca", false},
		{"dca632807952", "dc:a6:32:80:79:52", false},
		{" aa:bb:cc:dd:ee:ff ", "aa:bb:cc:dd:ee:ff", false}, // whitespace tolerated
		{"aa:bb:cc:dd:ee", "", true},
		{"aa:bb:cc:dd:ee:ff:00", "", true},
		{"zz:bb:cc:dd:ee:ff", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := canonicalMAC(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				if !errors.Is(err, errInvalidMAC) {
					t.Fatalf("want errInvalidMAC, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalMAC_RoundTrip(t *testing.T) {
	const start = "AA:BB:CC:11:22:33"
	canon, err := canonicalMAC(start)
	if err != nil {
		t.Fatal(err)
	}
	again, err := canonicalMAC(canon)
	if err != nil {
		t.Fatal(err)
	}
	if again != canon {
		t.Errorf("canonical of canonical changed: %q → %q", canon, again)
	}
}

func TestMACFilename(t *testing.T) {
	got := macFilename("dc:a6:32:8d:f3:ca")
	want := "dc-a6-32-8d-f3-ca.yml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
