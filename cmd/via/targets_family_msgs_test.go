package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// Any zoned address literal is refused at startup, not only link-local ones
// (viaduct#108 item 1). The lookup would drop the zone and trace the bare
// address.
func TestResolveTargetSpecs_RejectsZonedLiteral(t *testing.T) {
	for _, target := range []string{"2001:db8::1%eth0", "2001:db8::1%1", "198.51.100.7%eth0"} {
		t.Run(target, func(t *testing.T) {
			looked := false
			specs := resolveTargetSpecs([]string{target}, false, false, "", true, true, baseProbeConfig(), "udp",
				func(string) ([]net.IP, error) { looked = true; return []net.IP{net.ParseIP("2001:db8::1")}, nil })
			err := specs[0].Err
			if err == nil {
				t.Fatalf("zoned literal %s accepted (IP %v)", target, specs[0].IP)
			}
			if !strings.Contains(err.Error(), "zone") || !strings.Contains(err.Error(), "not supported") {
				t.Errorf("error does not say zoned targets are not supported: %v", err)
			}
			if !strings.Contains(err.Error(), target) {
				t.Errorf("error does not name the target %s: %v", target, err)
			}
			if looked {
				t.Error("lookup ran for a zoned literal")
			}
		})
	}

	t.Run("a zoned link-local literal keeps the link-local message", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"fe80::1%eth0"}, false, false, "", true, true, baseProbeConfig(), "udp", literalLookup(nil))
		if specs[0].Err == nil || !strings.Contains(specs[0].Err.Error(), "link-local") {
			t.Errorf("error = %v, want the link-local refusal", specs[0].Err)
		}
	})

	t.Run("a name containing % that is not an address goes to the lookup", func(t *testing.T) {
		looked := false
		resolveTargetSpecs([]string{"host%eth0"}, false, false, "", true, true, baseProbeConfig(), "udp",
			func(string) ([]net.IP, error) { looked = true; return nil, fmt.Errorf("no such host") })
		if !looked {
			t.Error("lookup did not run for a non-literal")
		}
	})
}

// Unspecified and broadcast targets are refused at startup, the same way
// multicast is (viaduct#108 item 5). via 0.0.0.0 used to trace localhost.
func TestResolveTargetSpecs_RejectsUnspecifiedAndBroadcast(t *testing.T) {
	lookup := literalLookup(map[string][]string{
		"zero.example":  {"0.0.0.0", "::"},
		"bcast.example": {"255.255.255.255"},
		"mixed.example": {"0.0.0.0", "198.51.100.7"},
	})
	for _, tc := range []struct{ target, want string }{
		{"0.0.0.0", "unspecified"},
		{"::", "unspecified"},
		{"::ffff:0.0.0.0", "unspecified"},
		{"255.255.255.255", "broadcast"},
		{"zero.example", "unspecified or broadcast"},
		{"bcast.example", "unspecified or broadcast"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			routeChecked := false
			withRouteCheck(t, func(net.IP) error { routeChecked = true; return nil })
			specs := resolveTargetSpecs([]string{tc.target}, false, false, "", true, true, baseProbeConfig(), "udp", lookup)
			err := specs[0].Err
			if err == nil {
				t.Fatalf("target %s accepted (IP %v)", tc.target, specs[0].IP)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "unicast targets only") {
				t.Errorf("error = %v, want it to say %q and unicast targets only", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("error does not name the target %s: %v", tc.target, err)
			}
			if routeChecked {
				t.Error("route pre-flight ran for a refused target")
			}
		})
	}

	t.Run("a name with a unicast address too uses the unicast one", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"mixed.example"}, false, false, "", true, true, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil || specs[0].IP.String() != "198.51.100.7" {
			t.Errorf("err %v, IP %v; want 198.51.100.7", specs[0].Err, specs[0].IP)
		}
	})

	t.Run("an address ending in .255 is ordinary unicast and accepted", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"198.51.100.255"}, false, false, "", true, true, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil {
			t.Errorf("unexpected error: %v", specs[0].Err)
		}
	})
}

// Family errors blame what forced the family: the flag or ip_family in the
// config file (viaduct#108 item 2).
func TestFamilySource(t *testing.T) {
	for _, tc := range []struct {
		forceV4, forceV6, fromConfig bool
		want                         string
	}{
		{false, false, false, ""},
		{false, false, true, ""},
		{true, false, false, "-4"},
		{false, true, false, "-6"},
		{true, false, true, `ip_family = "4" in the config file`},
		{false, true, true, `ip_family = "6" in the config file`},
	} {
		if got := familySource(tc.forceV4, tc.forceV6, tc.fromConfig); got != tc.want {
			t.Errorf("familySource(%v, %v, %v) = %q, want %q", tc.forceV4, tc.forceV6, tc.fromConfig, got, tc.want)
		}
	}
}

func TestResolveTargetSpecs_FamilyErrorsNameTheSource(t *testing.T) {
	const cfgSrc = `ip_family = "6" in the config file`
	lookup := literalLookup(map[string][]string{"dual.example": {"198.51.100.7", "2001:db8:100::7"}})

	t.Run("no IPv6 route with the family from config", func(t *testing.T) {
		withRouteCheck(t, func(net.IP) error { return fmt.Errorf("network is unreachable") })
		specs := resolveTargetSpecs([]string{"dual.example"}, false, true, cfgSrc, true, true, baseProbeConfig(), "udp", lookup)
		err := specs[0].Err
		if err == nil {
			t.Fatal("accepted with no IPv6 route")
		}
		if !strings.Contains(err.Error(), cfgSrc) || strings.Contains(err.Error(), "-6") {
			t.Errorf("error = %q, want it to blame the config and not -6", err)
		}
	})

	t.Run("IPv4-mapped literal with the family from config", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"::ffff:192.0.2.1"}, false, true, cfgSrc, true, true, baseProbeConfig(), "udp", lookup)
		err := specs[0].Err
		if err == nil {
			t.Fatal("accepted a mapped literal with IPv6 forced")
		}
		if !strings.Contains(err.Error(), cfgSrc) || strings.Contains(err.Error(), "-6") {
			t.Errorf("error = %q, want it to blame the config and not -6", err)
		}
	})
}

// selectFamily errors name the target, so multi-target failures can be told
// apart (viaduct#108 item 3).
func TestResolveTargetSpecs_FamilyErrorsNameTheTarget(t *testing.T) {
	lookup := literalLookup(map[string][]string{
		"v6only.example": {"2001:db8:100::8"},
		"v4only.example": {"198.51.100.9"},
	})
	initPortAllocator(2)
	specs := resolveTargetSpecs([]string{"v6only.example", "v4only.example"}, true, false, "-4", true, true, baseProbeConfig(), "udp", lookup)
	if specs[0].Err == nil {
		t.Fatal("IPv6-only target accepted with -4")
	}
	if !strings.Contains(specs[0].Err.Error(), "v6only.example") || !strings.Contains(specs[0].Err.Error(), "no IPv4 (A) record") {
		t.Errorf("error = %q, want it to name v6only.example and say it has no A record", specs[0].Err)
	}
	if specs[1].Err != nil {
		t.Errorf("v4only.example errored: %v", specs[1].Err)
	}
}
