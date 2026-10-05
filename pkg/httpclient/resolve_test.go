package httpclient

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"
)

// imdsIPv4 is what every name handed to the fake resolver resolves to.
var imdsIPv4 = [4]byte{169, 254, 169, 254}

var errTestContainment = errors.New("test containment denied off-loopback dial")

func newContainedClient(t *testing.T) *http.Client {
	t.Helper()
	client, dialer := newClient()
	productionControl := dialer.Control
	if productionControl == nil {
		t.Fatal("metadata dial control is missing")
	}
	dialer.Control = func(network, address string, conn syscall.RawConn) error {
		if err := productionControl(network, address, conn); err != nil {
			return err
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !ap.Addr().Unmap().WithZone("").IsLoopback() {
			return errTestContainment
		}
		return nil
	}
	client.Timeout = 2 * time.Second
	t.Cleanup(client.Transport.(*http.Transport).CloseIdleConnections)
	return client
}

// useFakeResolver points New's dialer at an in-process DNS server that answers
// every A query with 169.254.169.254, the way metadata.google.internal (and
// getaddrinfo for decimal/hex/octal IPv4 spellings) resolves in production.
func useFakeResolver(t *testing.T) {
	t.Helper()
	prev := resolver
	resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveOneDNSQuery(server)
			return client, nil
		},
	}
	t.Cleanup(func() { resolver = prev })
}

// serveOneDNSQuery answers one TCP-framed query (net.Pipe is not a PacketConn,
// so the Go resolver uses length-prefixed framing). A queries get imdsIPv4;
// anything else gets an empty answer section.
func serveOneDNSQuery(conn net.Conn) {
	defer conn.Close()
	var l [2]byte
	if _, err := io.ReadFull(conn, l[:]); err != nil {
		return
	}
	q := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(conn, q); err != nil || len(q) < 12 {
		return
	}
	end := 12
	for end < len(q) && q[end] != 0 {
		end += int(q[end]) + 1
	}
	end += 5 // root label + QTYPE + QCLASS
	if end > len(q) {
		return
	}
	isA := binary.BigEndian.Uint16(q[end-4:end-2]) == 1

	resp := append([]byte{}, q[:2]...)                      // ID
	resp = append(resp, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0) // flags, QD=1, AN/NS/AR=0
	resp = append(resp, q[12:end]...)
	if isA {
		resp[7] = 1 // ANCOUNT
		resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		resp = append(resp, imdsIPv4[:]...)
	}
	out := binary.BigEndian.AppendUint16(nil, uint16(len(resp)))
	_, _ = conn.Write(append(out, resp...))
}

func assertBlocked(t *testing.T, c *http.Client, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("request to %s must be blocked", url)
	}
	if errors.Is(err, errTestContainment) || !strings.Contains(err.Error(), "connection to metadata endpoint ") {
		t.Fatalf("request to %s: expected production metadata error, got: %v", url, err)
	}
}

// A hostname that resolves to IMDS must be blocked on the resolved address,
// not waved through because the name itself is not an IP literal.
func TestNew_BlocksHostnameResolvingToIMDS(t *testing.T) {
	useFakeResolver(t)
	c := newContainedClient(t)
	for _, url := range []string{
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token",
		"http://instance-data.ec2.internal/latest/meta-data/",
		// A hex IPv4 spelling reaches the resolver as a name; getaddrinfo
		// decodes it (and the decimal/octal forms) to 169.254.169.254.
		"http://0xa9fea9fe/latest/meta-data/",
	} {
		t.Run(url, func(t *testing.T) { assertBlocked(t, c, url) })
	}
}

// Literal addresses outside the old two-entry map: the IPv4-mapped IPv6 form
// of IMDS, and the ECS / EKS Pod Identity credential endpoints.
func TestNew_BlocksMetadataLiteralsOutsideExactMatch(t *testing.T) {
	c := newContainedClient(t)
	for _, url := range []string{
		"http://[::ffff:169.254.169.254]/metadata/identity/oauth2/token",
		"http://[::ffff:a9fe:a9fe]/latest/meta-data/",
		"http://169.254.170.2/v2/credentials/",
		"http://169.254.170.23/v1/credentials",
		"http://[fd00:ec2::23]/v1/credentials",
		"http://[fe80::1]/",
		"http://[fe80::1%251]/",
	} {
		t.Run(url, func(t *testing.T) { assertBlocked(t, c, url) })
	}
}

// Metadata services on routable addresses outside link-local (Azure WireServer,
// Alibaba Cloud IMDS), reached directly, IPv4-mapped, or via a redirect.
func TestNew_BlocksRoutableMetadataAddresses(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://168.63.129.16/machine?comp=goalstate", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	c := newContainedClient(t)
	for _, url := range []string{
		"http://168.63.129.16/machine?comp=goalstate",
		"http://168.63.129.16:32526/vmSettings",
		"http://[::ffff:168.63.129.16]/machine?comp=goalstate",
		"http://100.100.100.200/latest/meta-data/",
		redirect.URL,
	} {
		t.Run(url, func(t *testing.T) { assertBlocked(t, c, url) })
	}
}

// A redirect from an allowed endpoint to a name resolving to IMDS must be
// blocked when the redirect target is dialed.
func TestNew_BlocksRedirectToIMDS(t *testing.T) {
	useFakeResolver(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://metadata.google.internal/computeMetadata/v1/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	assertBlocked(t, newContainedClient(t), srv.URL)
}

func TestContainedClient_AllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	resp, err := newContainedClient(t).Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback request failed: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestContainedClient_DeniesDocumentationAddress(t *testing.T) {
	resp, err := newContainedClient(t).Get("http://192.0.2.1/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("documentation address reached without containment")
	}
	if !errors.Is(err, errTestContainment) {
		t.Fatalf("expected test containment error, got: %v", err)
	}
}
