package autoconfig

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// lookupIP is stubbed in tests.
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func envAllowPrivate() bool {
	value := strings.TrimSpace(os.Getenv("POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE"))
	return value == "1" || strings.EqualFold(value, "true")
}

func checkAutoconfigRedirect(next *url.URL, hops int, allowPrivate bool) error {
	if hops >= 5 {
		return fmt.Errorf("too many redirects")
	}
	if next == nil || !strings.EqualFold(next.Scheme, "https") {
		return fmt.Errorf("autoconfig redirected to non-HTTPS URL")
	}
	return validateHost(context.Background(), next.Hostname(), allowPrivate)
}

func validateHost(ctx context.Context, host string, allowPrivate bool) error {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ipDisallowed(ip, allowPrivate)
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if err := ipDisallowed(ip, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func validateMailAddress(ctx context.Context, address string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	return validateHost(ctx, host, allowPrivate)
}

func validateCalDAVURL(ctx context.Context, raw string, allowPrivate bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("caldav URL must be HTTPS")
	}
	return validateHost(ctx, parsed.Hostname(), allowPrivate)
}

func ipDisallowed(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return fmt.Errorf("empty address")
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("refusing special-use address %s", ip)
	}
	if !allowPrivate && ip.IsPrivate() {
		return fmt.Errorf("refusing private address %s; pass --allow-private or set POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE=1", ip)
	}
	return nil
}
