package httpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// NAT64, 6to4 and IPv4-compatible spellings of metadata addresses, plus the
// unspecified addresses, must be refused at dial time.
func TestBlockMetadata_EmbeddedAndUnspecified(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"[64:ff9b::a9fe:a9fe]:80",
		"[64:ff9b::169.254.169.254]:80",
		"[64:FF9B::A9FE:A9FE]:80",
		"[64:ff9b::a9fe:aa02]:80", // 169.254.170.2, ECS credentials
		"[64:ff9b::a83f:8110]:80", // 168.63.129.16, Azure WireServer
		"[64:ff9b::6464:64c8]:80", // 100.100.100.200, Alibaba IMDS
		"[2002:a9fe:a9fe::]:80",
		"[2002:a9fe:a9fe:1::1]:80",
		"[2002:a9fe:aa02::]:80",
		"[::a9fe:a9fe]:80",
		"[::169.254.169.254]:80",
		"[64:ff9b::a9fe:a9fe%eth0]:80",
		"0.0.0.0:80",
		"[::]:80",
		// IPv4-mapped forms reach blockMetadata unchanged when a caller dials
		// the literal; Unmap must reduce them to the IPv4 address.
		"[::ffff:169.254.169.254]:80",
		"[::ffff:a9fe:a9fe]:80",
		"[::ffff:168.63.129.16]:80",
		"[::ffff:100.100.100.200]:80",
		"[::ffff:0.0.0.0]:80",
	}
	for _, addr := range blocked {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := blockMetadata("tcp", addr, nil); err == nil {
				t.Fatalf("%s must be blocked", addr)
			}
		})
	}

	allowed := []string{
		"[2606:4700:4700::1111]:443",
		"[64:ff9b::808:808]:443", // NAT64 of 8.8.8.8
		"[2002:808:808::]:443",   // 6to4 of 8.8.8.8
		"[::808:808]:443",
		"[::1]:80",
		"127.0.0.1:80",
		"[::ffff:127.0.0.1]:80",
		"8.8.8.8:443",
		"[::ffff:8.8.8.8]:443",
	}
	for _, addr := range allowed {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := blockMetadata("tcp", addr, nil); err != nil {
				t.Fatalf("%s must stay allowed: %v", addr, err)
			}
		})
	}
}

// nat64Local returns an address in 64:ff9b:1::/48 whose RFC 6052 layout
// (index into rfc6052Layouts) carries v4.
func nat64Local(t *testing.T, layout int, v4 string) string {
	t.Helper()
	b := netip.MustParseAddr("64:ff9b:1::").As16()
	o := netip.MustParseAddr(v4).As4()
	for i, off := range rfc6052Layouts[layout] {
		b[off] = o[i]
	}
	return "[" + netip.AddrFrom16(b).String() + "]:80"
}

// A local-use NAT64 prefix (RFC 8215) may embed any IPv4 under any RFC 6052
// layout (/48, /56, /64, /96), so each layout carrying a metadata address must
// be refused, and a global one must not.
func TestBlockMetadata_LocalNAT64AllLayouts(t *testing.T) {
	t.Parallel()
	for layout := range rfc6052Layouts {
		for _, v4 := range []string{
			"169.254.169.254", // IMDS
			"169.254.170.2",   // ECS credentials
			"168.63.129.16",   // Azure WireServer
			"100.100.100.200", // Alibaba IMDS
		} {
			addr := nat64Local(t, layout, v4)
			t.Run(addr, func(t *testing.T) {
				t.Parallel()
				if err := blockMetadata("tcp", addr, nil); err == nil {
					t.Fatalf("%s (layout %d, %s) must be blocked", addr, layout, v4)
				}
			})
		}
		addr := nat64Local(t, layout, "8.8.8.8")
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := blockMetadata("tcp", addr, nil); err != nil {
				t.Fatalf("%s must stay allowed: %v", addr, err)
			}
		})
	}
	for _, addr := range []string{
		"[64:ff9b:1::a9fe:a9fe]:80", // /96 literal from the issue
		"[64:ff9b:1::169.254.169.254]:80",
		"[64:ff9b:1::a9fe:a9fe%eth0]:80",
	} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := blockMetadata("tcp", addr, nil); err == nil {
				t.Fatalf("%s must be blocked", addr)
			}
		})
	}
	if err := blockMetadata("tcp", "[64:ff9b:2::a9fe:a9fe]:80", nil); err != nil {
		t.Fatalf("address outside the /48 must stay allowed: %v", err)
	}
}

// Teredo is intentionally not decoded (see the Teredo comment in
// httpclient.go).
func TestBlockMetadata_TeredoStaysAllowed(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{
		"[2001:0:4136:e378:8000:63bf:5601:5601]:80", // client field de-obfuscates to 169.254.169.254
		"[2001::a9fe:a9fe]:80",
	} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := blockMetadata("tcp", addr, nil); err != nil {
				t.Fatalf("%s must stay allowed: %v", addr, err)
			}
		})
	}
}

// The guard runs for every dial, so a redirect to an embedded form or an
// unspecified address is refused as well.
func TestNew_BlocksEmbeddedFormsViaRedirect(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:a9fe:a9fe::]/latest/meta-data/",
		"http://[::a9fe:a9fe]/latest/meta-data/",
		"http://0.0.0.0/",
		"http://[::]/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target, http.StatusFound)
			}))
			t.Cleanup(srv.Close)

			resp, err := newContainedClient(t).Get(srv.URL)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("redirect to %s must be blocked", target)
			}
			if errors.Is(err, errTestContainment) {
				t.Fatalf("redirect to %s reached test containment, not the production guard: %v", target, err)
			}
		})
	}
}

// A hostname whose AAAA record is an embedded metadata form, an IPv4-mapped
// metadata address or an unspecified address must be refused on the resolved
// address, so DNS (including DNS64 synthesis) cannot route around the guard.
func TestNew_BlocksHostnameResolvingToEmbeddedOrUnspecified(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   string
	}{
		{"64:ff9b::a9fe:a9fe", "connection to metadata endpoint "},
		{"64:ff9b::a83f:8110", "connection to metadata endpoint "},
		{"2002:a9fe:a9fe::", "connection to metadata endpoint "},
		{"::a9fe:a9fe", "connection to metadata endpoint "},
		{"::ffff:169.254.169.254", "connection to metadata endpoint "},
		{"::", "connection to unspecified address "},
		{"0.0.0.0", "connection to unspecified address "},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			useFakeResolverWith(t, netip.MustParseAddr(tc.answer))
			resp, err := newContainedClient(t).Get("http://imds.example.test/latest/meta-data/")
			if err == nil {
				resp.Body.Close()
				t.Fatalf("hostname resolving to %s must be blocked", tc.answer)
			}
			if errors.Is(err, errTestContainment) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("hostname resolving to %s: want %q from the production guard, got: %v", tc.answer, tc.want, err)
			}
		})
	}
}

// The fake resolver path still reaches an allowed loopback server, so the
// test above fails only because of the guard.
func TestNew_AllowsHostnameResolvingToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	useFakeResolverWith(t, netip.MustParseAddr("127.0.0.1"))
	resp, err := newContainedClient(t).Get("http://loopback.example.test" + port + "/")
	if err != nil {
		t.Fatalf("hostname resolving to loopback failed: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}
