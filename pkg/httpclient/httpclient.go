// Package httpclient provides a hardened HTTP client for outbound
// requests. It blocks connections to the cloud Instance Metadata
// Service (IMDS) endpoints to prevent SSRF attacks that could leak
// cloud credentials, and applies sane dial/TLS/overall timeouts so a
// misbehaving endpoint cannot hang a caller indefinitely.
//
// This is the single shared implementation; the Azure provider's
// internal httpclient package delegates here so every module uses the
// same hardening.
package httpclient

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Timeouts applied by New. Exported indirectly via the constructed
// client; named here so the values are not magic numbers.
const (
	dialTimeout         = 10 * time.Second
	keepAliveInterval   = 30 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	requestTimeout      = 30 * time.Second
)

// metadataPrefixes are the address ranges of cloud metadata and credential
// services: all of IPv4/IPv6 link-local (AWS/Azure/GCP IMDS at 169.254.169.254,
// ECS task credentials at 169.254.170.2, EKS Pod Identity at 169.254.170.23)
// plus single routable addresses: the AWS IPv6 IMDS and Pod Identity endpoints,
// the GCP IPv6 metadata server (fd20:ce::254), Azure WireServer (168.63.129.16)
// and Alibaba Cloud IMDS (100.100.100.200).
var metadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd00:ec2::23/128"),
	netip.MustParsePrefix("fd20:ce::254/128"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
}

// resolver is the dialer's resolver; nil means net.DefaultResolver. Tests
// override it to resolve names to metadata addresses.
var resolver *net.Resolver

// embeddedIPv4Prefixes are IPv6 ranges that carry an IPv4 address in their
// last 32 bits (NAT64 and the deprecated IPv4-compatible form) or in bits
// 16-47 (6to4). Hosts with a NAT64 gateway or 6to4 relay forward these to the
// embedded IPv4 address, so it must pass the same metadata check.
var embeddedIPv4Prefixes = []struct {
	prefix netip.Prefix
	offset int // byte offset of the embedded IPv4 address in the 16-byte form
}{
	{netip.MustParsePrefix("64:ff9b::/96"), 12},
	{netip.MustParsePrefix("2002::/16"), 2},
	{netip.MustParsePrefix("::/96"), 12},
}

// embeddedIPv4 returns the IPv4 address ip carries, if any.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	for _, e := range embeddedIPv4Prefixes {
		if e.prefix.Contains(ip) {
			return netip.AddrFrom4([4]byte(b[e.offset : e.offset+4])), true
		}
	}
	return netip.Addr{}, false
}

func isMetadata(ip netip.Addr) bool {
	for _, p := range metadataPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// blockMetadata is a net.Dialer Control hook. It runs after name resolution
// with the exact IP about to be connected, so hostnames, alternate IPv4
// spellings, IPv4-mapped IPv6, NAT64/6to4 forms and redirects cannot bypass
// it. Unspecified addresses (0.0.0.0, ::) are refused too: they connect to
// local services on Linux and macOS.
func blockMetadata(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial unparseable address %q: %w", address, err)
	}
	// Prefix.Contains never matches a zoned address, so drop the zone.
	ip := ap.Addr().Unmap().WithZone("")
	if ip.IsUnspecified() {
		return fmt.Errorf("connection to unspecified address %s is blocked", ip)
	}
	if isMetadata(ip) {
		return fmt.Errorf("connection to metadata endpoint %s is blocked", ip)
	}
	if v4, ok := embeddedIPv4(ip); ok && isMetadata(v4) {
		return fmt.Errorf("connection to metadata endpoint %s (embedded in %s) is blocked", v4, ip)
	}
	return nil
}

// New returns an *http.Client with a 30-second timeout and IMDS blocking.
func New() *http.Client {
	client, _ := newClient()
	return client
}

func newClient() (*http.Client, *net.Dialer) {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
		Resolver:  resolver,
		Control:   blockMetadata,
	}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
	}
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
	}, dialer
}
