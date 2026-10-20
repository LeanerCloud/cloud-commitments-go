// Package httpclient provides a hardened HTTP client for outbound
// requests. It blocks connections to the cloud Instance Metadata
// Service (IMDS) endpoints to prevent SSRF attacks that could leak
// cloud credentials, and applies sane dial/TLS/overall timeouts so a
// misbehaving endpoint cannot hang a caller indefinitely.
//
// This is the single shared implementation; the Azure provider's
// internal httpclient package delegates here so every module uses the
// same hardening.
//
// The guard lives in the transport's dialer, so it protects only clients
// returned by New. A caller-supplied *http.Client (for example one passed to
// an Azure service's NewClientWithHTTP) is used as-is and bypasses it.
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

// localNAT64 is the RFC 8215 local-use NAT64 prefix. Unlike the well-known
// 64:ff9b::/96 (which RFC 6052 section 3.1 forbids from carrying non-global
// IPv4), a network-specific prefix may embed any IPv4 address, including
// metadata ones, and the operator chooses the prefix length (/48 to /96). The
// IPv4 address is therefore decoded under every RFC 6052 layout and the dial is
// refused if any decoding is blocked (#238).
var localNAT64 = netip.MustParsePrefix("64:ff9b:1::/48")

// rfc6052Layouts lists, per NAT64 prefix length (/48, /56, /64, /96), the byte
// offsets of the four IPv4 octets in the 16-byte form. Byte 8 is the reserved
// "u" octet and is skipped by RFC 6052.
var rfc6052Layouts = [][4]int{
	{6, 7, 9, 10},
	{7, 9, 10, 11},
	{9, 10, 11, 12},
	{12, 13, 14, 15},
}

// localNAT64Embedded returns the IPv4 address ip would translate to under each
// RFC 6052 layout, or nil when ip is outside localNAT64.
func localNAT64Embedded(ip netip.Addr) []netip.Addr {
	if !localNAT64.Contains(ip) {
		return nil
	}
	b := ip.As16()
	out := make([]netip.Addr, 0, len(rfc6052Layouts))
	for _, l := range rfc6052Layouts {
		out = append(out, netip.AddrFrom4([4]byte{b[l[0]], b[l[1]], b[l[2]], b[l[3]]}))
	}
	return out
}

// Teredo (2001::/32, deprecated) is deliberately NOT decoded (#238; pinned by
// TestBlockMetadata_TeredoStaysAllowed). An address carries the relay's IPv4
// and the client's obfuscated IPv4 (XOR 0xffff). The obfuscated client IPv4 is
// where a relay sends the tunnelled packets, as IPv6 inside UDP; it is not
// reachable as a metadata endpoint, because nothing here opens a TCP
// connection to it. A field that decodes to a metadata address is therefore
// not a path to that endpoint.

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
	for _, v4 := range localNAT64Embedded(ip) {
		if isMetadata(v4) {
			return fmt.Errorf("connection to metadata endpoint %s (embedded in %s) is blocked", v4, ip)
		}
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
