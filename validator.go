package teellm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ValidateEndpoint validates that the given endpoint is strictly https://, does not target restricted
// IP addresses (such as cloud metadata 169.254.169.254, 0.0.0.0, ::, or link-local IPs),
// and adheres to allowedHosts whitelist when provided.
func ValidateEndpoint(endpoint string, allowedHosts []string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return errors.New("endpoint cannot be empty")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" {
		return fmt.Errorf("unsupported protocol scheme %q: strictly https is required", scheme)
	}

	rawHost := parsed.Hostname()
	hostname := strings.ToLower(strings.Trim(rawHost, "[]"))
	if hostname == "" {
		return errors.New("endpoint host cannot be empty")
	}

	if portStr := parsed.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("invalid port %q: must be between 1 and 65535", portStr)
		}
	}

	// Reject cloud metadata services and any-addresses unconditionally
	if hostname == "169.254.169.254" || hostname == "0.0.0.0" || hostname == "::" {
		return fmt.Errorf("access to restricted address %q is forbidden", hostname)
	}

	hostForIP := strings.Split(hostname, "%")[0]
	ip := net.ParseIP(hostForIP)
	if ip != nil {
		if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("access to restricted address %q is forbidden", hostname)
		}
	}

	// Enforce allowedHosts whitelist if provided
	if len(allowedHosts) > 0 {
		allowedMap := make(map[string]struct{}, len(allowedHosts))
		for _, h := range allowedHosts {
			trimmed := strings.ToLower(strings.TrimSpace(h))
			if trimmed == "" {
				continue
			}
			host, _, err := net.SplitHostPort(trimmed)
			if err != nil {
				host = trimmed
			}
			host = strings.Trim(host, "[]")
			if host != "" {
				allowedMap[host] = struct{}{}
			}
		}

		if _, ok := allowedMap[hostname]; !ok {
			return fmt.Errorf("host %q is not in the allowed endpoint whitelist", hostname)
		}
	}

	return nil
}
