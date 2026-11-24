package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// literalLookup resolves IP literals to themselves and the given names to the
// given addresses, like net.LookupIP does for literals.
func literalLookup(names map[string][]string) lookupFunc {
	return func(host string) ([]net.IP, error) {
		if ip := net.ParseIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		addrs, ok := names[host]
		if !ok {
			return nil, fmt.Errorf("no such host")
		}
		var ips []net.IP
		for _, a := range addrs {
			ips = append(ips, net.ParseIP(a))
		}
		return ips, nil
	}
}

// Multicast targets are rejected at startup with a clear error (viaduct#57).
func TestResolveTargetSpecs_RejectsMulticast(t *testing.T) {
	lookup := literalLookup(map[string][]string{
		"mdns.example":  {"224.0.0.251", "ff02::fb"},
		"mixed.example": {"224.0.0.251", "198.51.100.7"},
	})

	for _, target := range []string{"224.0.0.251", "239.1.2.3", "ff02::1", "ff0e::1", "mdns.example"} {
		t.Run(target, func(t *testing.T) {
			specs := resolveTargetSpecs([]string{target}, false, false, true, true, baseProbeConfig(), "udp", lookup)
			err := specs[0].Err
			if err == nil {
				t.Fatalf("multicast target %s accepted (IP %v)", target, specs[0].IP)
			}
			if !strings.Contains(err.Error(), "multicast") {
				t.Errorf("error does not say multicast: %v", err)
			}
			if !strings.Contains(err.Error(), target) {
				t.Errorf("error does not name the target %s: %v", target, err)
			}
		})
	}

	t.Run("a name with a unicast address too uses the unicast one", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"mixed.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil {
			t.Fatalf("unexpected error: %v", specs[0].Err)
		}
		if got := specs[0].IP.String(); got != "198.51.100.7" {
			t.Errorf("IP = %s, want the unicast address 198.51.100.7", got)
		}
	})

	t.Run("in multi-target mode only the multicast target errors, and the error names it", func(t *testing.T) {
		initPortAllocator(2)
		specs := resolveTargetSpecs([]string{"198.51.100.1", "224.0.0.251"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil {
			t.Errorf("unicast target errored: %v", specs[0].Err)
		}
		if specs[1].Err == nil || !strings.Contains(specs[1].Err.Error(), "224.0.0.251") {
			t.Errorf("multicast target error = %v, want one naming 224.0.0.251", specs[1].Err)
		}
	})
}

// --no-paths means one flow, whatever the config file says (viaduct#51). It
// used to be overwritten by the config's paths value unless --paths was also
// given.
func TestResolveNumPaths(t *testing.T) {
	for _, tc := range []struct {
		name         string
		flagPaths    int
		pathsChanged bool
		noPaths      bool
		configPaths  int
		want         int
		wantErr      bool
	}{
		{name: "nothing given: the config value", flagPaths: 6, configPaths: 4, want: 4},
		{name: "--paths given: the flag wins over config", flagPaths: 12, pathsChanged: true, configPaths: 4, want: 12},
		{name: "--no-paths: one flow, config ignored", flagPaths: 6, noPaths: true, configPaths: 6, want: 1},
		{name: "--no-paths with config paths 1", flagPaths: 6, noPaths: true, configPaths: 1, want: 1},
		{name: "--no-paths with --paths 1: consistent, allowed", flagPaths: 1, pathsChanged: true, noPaths: true, configPaths: 6, want: 1},
		{name: "--no-paths with --paths 8: contradiction", flagPaths: 8, pathsChanged: true, noPaths: true, configPaths: 6, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveNumPaths(tc.flagPaths, tc.pathsChanged, tc.noPaths, tc.configPaths)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %d, want an error", got)
				}
				if !strings.Contains(err.Error(), "--no-paths") || !strings.Contains(err.Error(), "--paths") {
					t.Errorf("error should name both flags: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("paths = %d, want %d", got, tc.want)
			}
		})
	}
}

// withRouteCheck replaces the route pre-flight for one test.
func withRouteCheck(t *testing.T, f func(ip net.IP) error) {
	t.Helper()
	prev := routeTo
	routeTo = f
	t.Cleanup(func() { routeTo = prev })
}

// IPv6 is pre-flighted: an auto-selected IPv6 address with no route falls back
// to IPv4 and says so; an explicit -6 never falls back (viaduct#55).
func TestResolveTargetSpecs_IPv6RoutePreflight(t *testing.T) {
	lookup := literalLookup(map[string][]string{
		"dual.example":   {"198.51.100.7", "2001:db8:100::7"},
		"v6only.example": {"2001:db8:100::8"},
		"v4only.example": {"198.51.100.9"},
	})
	noV6Route := func(ip net.IP) error {
		if ip.To4() == nil {
			return fmt.Errorf("network is unreachable")
		}
		return nil
	}

	t.Run("auto-selected IPv6 with no route falls back to IPv4, with a notice", func(t *testing.T) {
		withRouteCheck(t, noV6Route)
		specs := resolveTargetSpecs([]string{"dual.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		sp := specs[0]
		if sp.Err != nil {
			t.Fatalf("unexpected error: %v", sp.Err)
		}
		if sp.Cfg.IPVersion != 4 || sp.IP.String() != "198.51.100.7" {
			t.Errorf("selected IPv%d %v, want IPv4 198.51.100.7", sp.Cfg.IPVersion, sp.IP)
		}
		if !strings.Contains(sp.Notice, "no IPv6 route") || !strings.Contains(sp.Notice, "IPv4") {
			t.Errorf("notice = %q, want it to say there is no IPv6 route and IPv4 is used", sp.Notice)
		}
	})

	t.Run("-6 with no route fails clearly and does not fall back", func(t *testing.T) {
		withRouteCheck(t, noV6Route)
		specs := resolveTargetSpecs([]string{"dual.example"}, false, true, true, true, baseProbeConfig(), "udp", lookup)
		err := specs[0].Err
		if err == nil {
			t.Fatalf("-6 with no IPv6 route accepted: IPv%d %v", specs[0].Cfg.IPVersion, specs[0].IP)
		}
		for _, want := range []string{"no IPv6 route", "2001:db8:100::7", "-6"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("IPv6 with a route is used, no notice", func(t *testing.T) {
		withRouteCheck(t, func(net.IP) error { return nil })
		specs := resolveTargetSpecs([]string{"dual.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		sp := specs[0]
		if sp.Err != nil || sp.Cfg.IPVersion != 6 || sp.Notice != "" {
			t.Errorf("err %v, IPv%d, notice %q; want IPv6 with no error and no notice", sp.Err, sp.Cfg.IPVersion, sp.Notice)
		}
	})

	t.Run("IPv6-only target with no route is left on IPv6: there is nothing to fall back to", func(t *testing.T) {
		withRouteCheck(t, noV6Route)
		specs := resolveTargetSpecs([]string{"v6only.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		sp := specs[0]
		if sp.Err == nil {
			t.Fatalf("an unreachable IPv6-only target was accepted: IPv%d %v", sp.Cfg.IPVersion, sp.IP)
		}
		if !strings.Contains(sp.Err.Error(), "no IPv6 route") || strings.Contains(sp.Err.Error(), "-6 was given") {
			t.Errorf("error = %q, want a no-route error that does not blame -6", sp.Err)
		}
	})

	t.Run("an IPv4 selection is not pre-flighted", func(t *testing.T) {
		called := false
		withRouteCheck(t, func(net.IP) error { called = true; return fmt.Errorf("unreachable") })
		specs := resolveTargetSpecs([]string{"v4only.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil || specs[0].Cfg.IPVersion != 4 {
			t.Errorf("err %v, IPv%d; want IPv4 with no error", specs[0].Err, specs[0].Cfg.IPVersion)
		}
		if called {
			t.Error("route check ran for an IPv4 target")
		}
	})
}

// IPv4-mapped IPv6 literals are traced as the IPv4 address they embed
// (viaduct#56).
func TestResolveTargetSpecs_IPv4MappedLiteral(t *testing.T) {
	lookup := literalLookup(nil)

	t.Run("traced over IPv4", func(t *testing.T) {
		for _, target := range []string{"::ffff:192.0.2.1", "::FFFF:192.0.2.1", "::ffff:c000:201"} {
			specs := resolveTargetSpecs([]string{target}, false, false, true, true, baseProbeConfig(), "udp", lookup)
			if specs[0].Err != nil {
				t.Fatalf("%s: unexpected error: %v", target, specs[0].Err)
			}
			if specs[0].Cfg.IPVersion != 4 {
				t.Errorf("%s: IPVersion = %d, want 4", target, specs[0].Cfg.IPVersion)
			}
			if got := specs[0].IP.String(); got != "192.0.2.1" {
				t.Errorf("%s: IP = %s, want 192.0.2.1", target, got)
			}
			if len(specs[0].IP) != net.IPv4len {
				t.Errorf("%s: IP is %d bytes, want the 4-byte form", target, len(specs[0].IP))
			}
		}
	})

	t.Run("works on a host with no IPv6 transport", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"::ffff:192.0.2.1"}, false, false, true, false, baseProbeConfig(), "udp", lookup)
		if specs[0].Err != nil || specs[0].Cfg.IPVersion != 4 {
			t.Errorf("err = %v, IPVersion = %d; want IPv4 with no error", specs[0].Err, specs[0].Cfg.IPVersion)
		}
	})

	t.Run("-6 fails with a clear error", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"::ffff:192.0.2.1"}, false, true, true, true, baseProbeConfig(), "udp", lookup)
		err := specs[0].Err
		if err == nil {
			t.Fatalf("-6 with a mapped literal accepted (IP %v, version %d)", specs[0].IP, specs[0].Cfg.IPVersion)
		}
		if !strings.Contains(err.Error(), "IPv4-mapped") || !strings.Contains(err.Error(), "-6") {
			t.Errorf("error should say the address is IPv4-mapped and cannot be used with -6: %v", err)
		}
	})

	t.Run("a plain IPv4 literal with -6 keeps its existing error", func(t *testing.T) {
		specs := resolveTargetSpecs([]string{"192.0.2.1"}, false, true, true, true, baseProbeConfig(), "udp", lookup)
		err := specs[0].Err
		if err == nil || strings.Contains(err.Error(), "IPv4-mapped") {
			t.Errorf("error = %v, want the ordinary no-IPv6 error", err)
		}
	})
}
