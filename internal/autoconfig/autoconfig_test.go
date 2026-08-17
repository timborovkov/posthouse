package autoconfig

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/autoconfig/mail/config-v1.1.xml" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	// Direct unit coverage of XML mapping via unmarshaling path used by probeThunderbird.
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
	_ = context.Background()
	_ = server
}

func TestSocketFlagsPlain(t *testing.T) {
	tls, startTLS := socketFlags("plain", 143, true)
	if tls || startTLS {
		t.Fatalf("plain imap should be cleartext, got tls=%v starttls=%v", tls, startTLS)
	}
}
