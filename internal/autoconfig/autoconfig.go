// Package autoconfig discovers IMAP/SMTP/CalDAV endpoints from an email address
// using RFC 6186 SRV records and Thunderbird autoconfig XML. It does not ship
// branded hostname catalogs.
package autoconfig

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/timborovkov/posthouse/internal/model"
)

const maxAutoconfigBytes = 1 << 20

type Result struct {
	Email    string            `json:"email"`
	Domain   string            `json:"domain"`
	IMAP     *model.IMAPConfig `json:"imap,omitempty"`
	SMTP     *model.SMTPConfig `json:"smtp,omitempty"`
	CalDAV   string            `json:"caldav_url,omitempty"`
	Sources  []string          `json:"sources,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

// Probe discovers mail endpoints for email. Secrets and identities are not filled.
// Private/loopback/link-local destinations are rejected unless allowPrivate is set
// (or POSTHOUSE_AUTOCONFIG_ALLOW_PRIVATE=1).
func Probe(ctx context.Context, email string, allowPrivate bool) (Result, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || strings.Contains(email[:at], " ") {
		return Result{}, fmt.Errorf("email address is required")
	}
	allowPrivate = allowPrivate || envAllowPrivate()
	domain := email[at+1:]
	result := Result{Email: email, Domain: domain}
	if err := validateHost(ctx, domain, allowPrivate); err != nil {
		return Result{}, fmt.Errorf("probe domain %s: %w", domain, err)
	}

	tbIMAP, tbSMTP, tbSource, tbErr := probeThunderbird(ctx, email, domain, allowPrivate)
	if tbErr != nil {
		result.Warnings = append(result.Warnings, "thunderbird autoconfig: "+tbErr.Error())
	} else if tbSource != "" {
		result.Sources = append(result.Sources, tbSource)
		result.IMAP = tbIMAP
		result.SMTP = tbSMTP
	}

	srvIMAP, srvSMTP, srvSources, srvWarnings := probeSRV(ctx, domain, allowPrivate)
	result.Warnings = append(result.Warnings, srvWarnings...)
	result.Sources = append(result.Sources, srvSources...)
	if result.IMAP == nil {
		result.IMAP = srvIMAP
	}
	if result.SMTP == nil {
		result.SMTP = srvSMTP
	}

	if caldav, source, err := probeCalDAV(ctx, domain, allowPrivate); err != nil {
		result.Warnings = append(result.Warnings, "caldav well-known: "+err.Error())
	} else if caldav != "" {
		result.CalDAV = caldav
		result.Sources = append(result.Sources, source)
	}

	if result.IMAP == nil && result.SMTP == nil && result.CalDAV == "" {
		return result, fmt.Errorf("no IMAP, SMTP, or CalDAV endpoints discovered for %s", domain)
	}
	return result, nil
}

func probeSRV(ctx context.Context, domain string, allowPrivate bool) (imapCfg *model.IMAPConfig, smtpCfg *model.SMTPConfig, sources, warnings []string) {
	type candidate struct {
		service string
		secure  bool
	}
	lookup := func(service string) ([]*net.SRV, error) {
		resolver := net.DefaultResolver
		_, records, err := resolver.LookupSRV(ctx, service, "tcp", domain)
		return records, err
	}
	pick := func(records []*net.SRV) *net.SRV {
		if len(records) == 0 {
			return nil
		}
		best := records[0]
		for _, record := range records[1:] {
			if record.Priority < best.Priority || (record.Priority == best.Priority && record.Weight > best.Weight) {
				best = record
			}
		}
		if best.Target == "" || best.Target == "." {
			return nil
		}
		return best
	}

	for _, item := range []candidate{{"imaps", true}, {"imap", false}} {
		records, err := lookup(item.service)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("SRV _%s._tcp: %v", item.service, err))
			continue
		}
		best := pick(records)
		if best == nil {
			continue
		}
		host := strings.TrimSuffix(best.Target, ".")
		address := net.JoinHostPort(host, fmt.Sprintf("%d", best.Port))
		imapCfg = &model.IMAPConfig{Address: address, TLS: item.secure, StartTLS: !item.secure && best.Port != 143}
		if !item.secure && best.Port == 143 {
			imapCfg.StartTLS = true
		}
		if err := validateMailAddress(ctx, imapCfg.Address, allowPrivate); err != nil {
			warnings = append(warnings, fmt.Sprintf("SRV _%s._tcp: %v", item.service, err))
			imapCfg = nil
			continue
		}
		sources = append(sources, "srv:_"+item.service+"._tcp")
		break
	}
	for _, item := range []candidate{{"submissions", true}, {"submission", false}} {
		records, err := lookup(item.service)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("SRV _%s._tcp: %v", item.service, err))
			continue
		}
		best := pick(records)
		if best == nil {
			continue
		}
		host := strings.TrimSuffix(best.Target, ".")
		address := net.JoinHostPort(host, fmt.Sprintf("%d", best.Port))
		smtpCfg = &model.SMTPConfig{Address: address, TLS: item.secure, StartTLS: !item.secure}
		if err := validateMailAddress(ctx, smtpCfg.Address, allowPrivate); err != nil {
			warnings = append(warnings, fmt.Sprintf("SRV _%s._tcp: %v", item.service, err))
			smtpCfg = nil
			continue
		}
		sources = append(sources, "srv:_"+item.service+"._tcp")
		break
	}
	return imapCfg, smtpCfg, sources, warnings
}

type thunderbirdClientConfig struct {
	EmailProvider struct {
		IncomingServers []thunderbirdServer `xml:"incomingServer"`
		OutgoingServers []thunderbirdServer `xml:"outgoingServer"`
	} `xml:"emailProvider"`
}

type thunderbirdServer struct {
	Type           string `xml:"type,attr"`
	Hostname       string `xml:"hostname"`
	Port           int    `xml:"port"`
	SocketType     string `xml:"socketType"`
	Authentication string `xml:"authentication"`
	Username       string `xml:"username"`
}

func probeThunderbird(ctx context.Context, email, domain string, allowPrivate bool) (*model.IMAPConfig, *model.SMTPConfig, string, error) {
	urls := []string{
		fmt.Sprintf("https://autoconfig.%s/mail/config-v1.1.xml?emailaddress=%s", domain, url.QueryEscape(email)),
		fmt.Sprintf("https://%s/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress=%s", domain, url.QueryEscape(email)),
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return checkAutoconfigRedirect(req.URL, len(via), allowPrivate)
	}}
	var lastErr error
	for _, endpoint := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "posthouse-autoconfig/0.2")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAutoconfigBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s returned %s", endpoint, resp.Status)
			continue
		}
		if len(data) > maxAutoconfigBytes {
			lastErr = fmt.Errorf("autoconfig document exceeds %d bytes", maxAutoconfigBytes)
			continue
		}
		var doc thunderbirdClientConfig
		if err := xml.Unmarshal(data, &doc); err != nil {
			lastErr = err
			continue
		}
		imapCfg, smtpCfg := thunderbirdConfigs(doc)
		if imapCfg != nil {
			if err := validateMailAddress(ctx, imapCfg.Address, allowPrivate); err != nil {
				lastErr = err
				imapCfg = nil
			}
		}
		if smtpCfg != nil {
			if err := validateMailAddress(ctx, smtpCfg.Address, allowPrivate); err != nil {
				lastErr = err
				smtpCfg = nil
			}
		}
		if imapCfg == nil && smtpCfg == nil {
			if lastErr == nil {
				lastErr = fmt.Errorf("%s had no IMAP/SMTP servers", endpoint)
			}
			continue
		}
		return imapCfg, smtpCfg, "thunderbird:" + endpoint, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no autoconfig document found")
	}
	return nil, nil, "", lastErr
}

func thunderbirdConfigs(doc thunderbirdClientConfig) (*model.IMAPConfig, *model.SMTPConfig) {
	var imapCfg *model.IMAPConfig
	var smtpCfg *model.SMTPConfig
	for _, server := range doc.EmailProvider.IncomingServers {
		if !strings.EqualFold(server.Type, "imap") || server.Hostname == "" || server.Port == 0 {
			continue
		}
		tls, startTLS := socketFlags(server.SocketType, server.Port, true)
		imapCfg = &model.IMAPConfig{Address: net.JoinHostPort(server.Hostname, fmt.Sprintf("%d", server.Port)), TLS: tls, StartTLS: startTLS}
		break
	}
	for _, server := range doc.EmailProvider.OutgoingServers {
		if !strings.EqualFold(server.Type, "smtp") || server.Hostname == "" || server.Port == 0 {
			continue
		}
		tls, startTLS := socketFlags(server.SocketType, server.Port, false)
		smtpCfg = &model.SMTPConfig{Address: net.JoinHostPort(server.Hostname, fmt.Sprintf("%d", server.Port)), TLS: tls, StartTLS: startTLS}
		break
	}
	return imapCfg, smtpCfg
}

func socketFlags(socketType string, port int, incoming bool) (tls, startTLS bool) {
	switch strings.ToLower(strings.TrimSpace(socketType)) {
	case "ssl", "tls":
		return true, false
	case "starttls":
		return false, true
	case "plain":
		return false, false
	default:
		if incoming {
			return port == 993, port == 143
		}
		return port == 465, port == 587
	}
}

func probeCalDAV(ctx context.Context, domain string, allowPrivate bool) (string, string, error) {
	endpoint := fmt.Sprintf("https://%s/.well-known/caldav", domain)
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "posthouse-autoconfig/0.2")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	caldav, err := acceptCalDAVWellKnown(endpoint, resp.StatusCode, resp.Header.Get("Location"), resp.Request.URL, allowPrivate)
	if err != nil {
		return "", "", err
	}
	return caldav, "well-known:caldav", nil
}

func acceptCalDAVWellKnown(endpoint string, status int, location string, base *url.URL, allowPrivate bool) (string, error) {
	if location != "" {
		resolved, err := base.Parse(location)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(resolved.Scheme, "https") {
			return "", fmt.Errorf("caldav well-known redirected to non-HTTPS URL")
		}
		if err := validateHost(context.Background(), resolved.Hostname(), allowPrivate); err != nil {
			return "", err
		}
		return resolved.String(), nil
	}
	if status >= 300 && status < 400 {
		return "", fmt.Errorf("%s returned %s without Location", endpoint, http.StatusText(status))
	}
	if status == http.StatusOK || status == http.StatusUnauthorized || status == http.StatusMethodNotAllowed {
		if err := validateHost(context.Background(), base.Hostname(), allowPrivate); err != nil {
			return "", err
		}
		return endpoint, nil
	}
	return "", fmt.Errorf("%s returned %s", endpoint, http.StatusText(status))
}
