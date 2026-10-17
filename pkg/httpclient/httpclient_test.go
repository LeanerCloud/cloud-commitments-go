package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// New must hand back a dedicated hardened client, never the process-wide
// default client/transport (SEC-04 regression guard).
func TestNew_NotDefaultClient(t *testing.T) {
	c := New()

	if c == http.DefaultClient {
		t.Fatalf("New() must not return http.DefaultClient")
	}
	if c.Timeout == 0 {
		t.Fatalf("New() must set an overall request timeout")
	}
	if c.Transport == nil || c.Transport == http.DefaultTransport {
		t.Fatalf("New() must install a dedicated hardened transport")
	}
}

func TestNew_BlocksIMDS(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "ipv4 link-local IMDS", url: "http://169.254.169.254/latest/meta-data/"},
		{name: "ipv6 AWS IMDS", url: "http://[fd00:ec2::254]/latest/meta-data/"},
	}

	c := newContainedClient(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocked(t, c, tt.url)
		})
	}
}

func TestNew_AllowsRegularEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := New().Do(req)
	if err != nil {
		t.Fatalf("request to non-IMDS endpoint failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// The single-address metadata entries (/128 and /32) must not widen: the
// neighbors of each stay allowed. IPv4 link-local is deliberately a whole /16,
// so 169.254.169.253 is blocked, not allowed (#227).
func TestBlockMetadata_NeighborsOfSingleAddressEntries(t *testing.T) {
	blocked := []string{
		"169.254.169.253:80", // inside the link-local /16
		"169.254.169.255:80",
	}
	for _, addr := range blocked {
		t.Run("blocked "+addr, func(t *testing.T) {
			if err := blockMetadata("tcp", addr, nil); err == nil {
				t.Fatalf("%s must be blocked", addr)
			}
		})
	}

	allowed := []string{
		"[fd00:ec2::253]:80", "[fd00:ec2::255]:80", // AWS IPv6 IMDS /128
		"[fd00:ec2::22]:80", "[fd00:ec2::24]:80", // AWS Pod Identity /128
		"[fd20:ce::253]:80", "[fd20:ce::255]:80", "[fd20:ce:0:1::254]:80", // GCP IPv6 /128
		"168.63.129.15:80", "168.63.129.17:80", // Azure WireServer /32
		"100.100.100.199:80", "100.100.100.201:80", // Alibaba IMDS /32
	}
	for _, addr := range allowed {
		t.Run("allowed "+addr, func(t *testing.T) {
			if err := blockMetadata("tcp", addr, nil); err != nil {
				t.Fatalf("%s must stay allowed: %v", addr, err)
			}
		})
	}
}
