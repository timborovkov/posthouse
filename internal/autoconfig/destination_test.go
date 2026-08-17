package autoconfig

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestIPDisallowed(t *testing.T) {
	if err := ipDisallowed(net.ParseIP("127.0.0.1"), false); err == nil || !strings.Contains(err.Error(), "special-use") {
		t.Fatalf("loopback error = %v", err)
	}
	if err := ipDisallowed(net.ParseIP("::1"), true); err == nil {
		t.Fatal("loopback allowed even with allowPrivate")
	}
	if err := ipDisallowed(net.ParseIP("169.254.169.254"), false); err == nil {
		t.Fatal("link-local allowed")
	}
	if err := ipDisallowed(net.ParseIP("10.0.0.1"), false); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private error = %v", err)
	}
	if err := ipDisallowed(net.ParseIP("10.0.0.1"), true); err != nil {
		t.Fatalf("private with allow = %v", err)
	}
	if err := ipDisallowed(net.ParseIP("8.8.8.8"), false); err != nil {
		t.Fatalf("public = %v", err)
	}
}

func TestCheckAutoconfigRedirect(t *testing.T) {
	httpURL, _ := url.Parse("http://mail.example.test/config")
	if err := checkAutoconfigRedirect(httpURL, 1, false); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("http redirect error = %v", err)
	}
	loop, _ := url.Parse("https://127.0.0.1/config")
	if err := checkAutoconfigRedirect(loop, 1, false); err == nil || !strings.Contains(err.Error(), "special-use") {
		t.Fatalf("loopback redirect error = %v", err)
	}
	if err := checkAutoconfigRedirect(loop, 5, false); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("hop limit error = %v", err)
	}
}

func TestValidateMailAddressRejectsPrivate(t *testing.T) {
	if err := validateMailAddress(context.Background(), "10.1.2.3:993", false); err == nil {
		t.Fatal("expected private rejection")
	}
	if err := validateMailAddress(context.Background(), "10.1.2.3:993", true); err != nil {
		t.Fatal(err)
	}
}
