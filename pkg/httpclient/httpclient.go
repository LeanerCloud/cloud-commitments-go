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
// Azure WireServer (168.63.129.16) and Alibaba Cloud IMDS (100.100.100.200).
var metadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd00:ec2::23/128"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
}

// resolver is the dialer's resolver; nil means net.DefaultResolver. Tests
// override it to resolve names to metadata addresses.
var resolver *net.Resolver

// blockMetadata is a net.Dialer Control hook. It runs after name resolution
// with the exact IP about to be connected, so hostnames, alternate IPv4
// spellings, IPv4-mapped IPv6 and redirects cannot bypass it.
func blockMetadata(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial unparseable address %q: %w", address, err)
	}
	// Prefix.Contains never matches a zoned address, so drop the zone.
	ip := ap.Addr().Unmap().WithZone("")
	for _, p := range metadataPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("connection to metadata endpoint %s is blocked", ip)
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
