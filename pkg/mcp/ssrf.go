package mcp

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

var blockedMetadataHosts = []string{
	"169.254.169.254",
	"metadata.google.internal",
	"100.100.100.200",
}

// ValidateEndpoint checks whether a server URL is safe from SSRF attacks against cloud metadata or forbidden networks.
func ValidateEndpoint(rawURL string, allowLocalhost bool) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("%w: endpoint is empty", ErrInvalidEndpoint)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: malformed URL: %v", ErrInvalidEndpoint, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: unsupported scheme %q, only http and https are allowed", ErrInvalidEndpoint, scheme)
	}

	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return fmt.Errorf("%w: missing host in URL", ErrInvalidEndpoint)
	}

	// 1. Explicit metadata service block
	for _, blocked := range blockedMetadataHosts {
		if hostname == blocked {
			return fmt.Errorf("%w: access to cloud metadata service (%s) is forbidden", ErrSSRFBlocked, hostname)
		}
	}

	// 2. Check IP addresses
	ip := net.ParseIP(hostname)
	if ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("%w: access to link-local address (%s) is forbidden", ErrSSRFBlocked, ip.String())
		}
		if ip.IsLoopback() {
			if !allowLocalhost {
				return fmt.Errorf("%w: access to loopback address (%s) is forbidden in production", ErrSSRFBlocked, ip.String())
			}
		}
	} else if (hostname == "localhost" || strings.HasSuffix(hostname, ".localhost")) && !allowLocalhost {
		return fmt.Errorf("%w: access to localhost is forbidden in production", ErrSSRFBlocked)
	}

	return nil
}
