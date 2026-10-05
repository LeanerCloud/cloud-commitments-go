package httpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NAT64, 6to4 and IPv4-compatible spellings of metadata addresses, plus the
// unspecified addresses, must be refused at dial time.
func TestBlockMetadata_EmbeddedAndUnspecified(t *testing.T) {
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
	}
	for _, addr := range blocked {
		t.Run(addr, func(t *testing.T) {
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
		"8.8.8.8:443",
	}
	for _, addr := range allowed {
		t.Run(addr, func(t *testing.T) {
			if err := blockMetadata("tcp", addr, nil); err != nil {
				t.Fatalf("%s must stay allowed: %v", addr, err)
			}
		})
	}
}

// The guard runs for every dial, so a redirect to an embedded form or an
// unspecified address is refused as well.
func TestNew_BlocksEmbeddedFormsViaRedirect(t *testing.T) {
	for _, target := range []string{
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:a9fe:a9fe::]/latest/meta-data/",
		"http://[::a9fe:a9fe]/latest/meta-data/",
		"http://0.0.0.0/",
		"http://[::]/",
	} {
		t.Run(target, func(t *testing.T) {
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
