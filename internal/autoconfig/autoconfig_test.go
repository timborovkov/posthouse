package autoconfig

import (
	"context"
	"encoding/xml"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProbeRejectsInvalidEmail(t *testing.T) {
	if _, err := Probe(context.Background(), "not-an-email", false); err == nil || !strings.Contains(err.Error(), "email address is required") {
		t.Fatalf("Probe error = %v", err)
	}
}

func TestThunderbirdConfigsMapsSocketTypes(t *testing.T) {
	const body = `<?xml version="1.0"?>
<clientConfig version="1.1">
  <emailProvider id="example.test">
    <incomingServer type="imap">
      <hostname>imap.example.test</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>smtp.example.test</hostname>
      <port>587</port>
      <socketType>STARTTLS</socketType>
    </outgoingServer>
  </emailProvider>
</clientConfig>`
	var doc thunderbirdClientConfig
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	imapCfg, smtpCfg := thunderbirdConfigs(doc)
	if imapCfg == nil || imapCfg.Address != "imap.example.test:993" || !imapCfg.TLS || imapCfg.StartTLS {
		t.Fatalf("imap = %#v", imapCfg)
	}
	if smtpCfg == nil || smtpCfg.Address != "smtp.example.test:587" || smtpCfg.TLS || !smtpCfg.StartTLS {
		t.Fatalf("smtp = %#v", smtpCfg)
	}
}

func TestAcceptCalDAVWellKnown(t *testing.T) {
	orig := lookupIP
	lookupIP = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	t.Cleanup(func() { lookupIP = orig })
	base, err := url.Parse("https://example.test/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	got, err := acceptCalDAVWellKnown(base.String(), http.StatusMovedPermanently, "https://cal.example.test/dav/", base, true)
	if err != nil || got != "https://cal.example.test/dav/" {
		t.Fatalf("https redirect = %q, %v", got, err)
	}
	if _, err := acceptCalDAVWellKnown(base.String(), http.StatusMovedPermanently, "http://cal.example.test/dav/", base, true); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("http redirect error = %v", err)
	}
	got, err = acceptCalDAVWellKnown(base.String(), http.StatusUnauthorized, "", base, true)
	if err != nil || got != base.String() {
		t.Fatalf("401 endpoint = %q, %v", got, err)
	}
	if _, err := acceptCalDAVWellKnown(base.String(), http.StatusMovedPermanently, "https://127.0.0.1/dav/", base, false); err == nil || !strings.Contains(err.Error(), "special-use") {
		t.Fatalf("loopback caldav error = %v", err)
	}
}

func TestProbeThunderbirdXML(t *testing.T) {
	const body = `<?xml version="1.0"?>
<clientConfig version="1.1">
  <emailProvider id="example.test">
    <incomingServer type="imap">
      <hostname>imap.example.test</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>smtp.example.test</hostname>
      <port>587</port>
      <socketType>STARTTLS</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </outgoingServer>
  </emailProvider>
</clientConfig>`
	var doc thunderbirdClientConfig
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.EmailProvider.IncomingServers) != 1 || doc.EmailProvider.IncomingServers[0].Hostname != "imap.example.test" {
		t.Fatalf("incoming = %#v", doc.EmailProvider.IncomingServers)
	}
	tls, startTLS := socketFlags("SSL", 993, true)
	if !tls || startTLS {
		t.Fatalf("imap flags tls=%v starttls=%v", tls, startTLS)
	}
	tls, startTLS = socketFlags("STARTTLS", 587, false)
	if tls || !startTLS {
		t.Fatalf("smtp flags tls=%v starttls=%v", tls, startTLS)
	}
}

func TestSocketFlagsPlain(t *testing.T) {
	tls, startTLS := socketFlags("plain", 143, true)
	if tls || startTLS {
		t.Fatalf("plain imap should be cleartext, got tls=%v starttls=%v", tls, startTLS)
	}
}
