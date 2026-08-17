// Package netproxy dials TCP through optional SOCKS5/HTTP proxies.
// When no explicit proxy is set, ALL_PROXY / HTTPS_PROXY / HTTP_PROXY are honored.
// Loopback targets and NO_PROXY matches bypass the environment proxy.
package netproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// DialTCP opens a TCP connection to address, optionally via proxyURL or environment proxies.
func DialTCP(ctx context.Context, address, proxyURL string) (net.Conn, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		if bypassProxy(address) {
			return dialDirect(ctx, address)
		}
		proxyURL = firstEnv("ALL_PROXY", "all_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy")
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if proxyURL == "" {
		return dialer.DialContext(ctx, "tcp", address)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		socks, err := proxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil, fmt.Errorf("configure SOCKS5 proxy: %w", err)
		}
		if contextDialer, ok := socks.(proxy.ContextDialer); ok {
			return contextDialer.DialContext(ctx, "tcp", address)
		}
		return socks.Dial("tcp", address)
	case "http":
		return dialHTTPConnect(ctx, dialer, parsed, address, false)
	case "https":
		return dialHTTPConnect(ctx, dialer, parsed, address, true)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use socks5:// or http://)", parsed.Scheme)
	}
}

func dialDirect(ctx context.Context, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, "tcp", address)
}

func proxyDialAddress(proxyURL *url.URL, useTLS bool) string {
	host := proxyURL.Hostname()
	port := proxyURL.Port()
	if port == "" {
		if useTLS {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

func dialHTTPConnect(ctx context.Context, dialer *net.Dialer, proxyURL *url.URL, address string, useTLS bool) (net.Conn, error) {
	proxyHost := proxyDialAddress(proxyURL, useTLS)
	connection, err := dialer.DialContext(ctx, "tcp", proxyHost)
	if err != nil {
		return nil, fmt.Errorf("connect to HTTP proxy: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)

	var transport net.Conn = connection
	if useTLS {
		serverName := proxyURL.Hostname()
		tlsConn := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("TLS handshake with HTTPS proxy: %w", err)
		}
		transport = tlsConn
	}

	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: make(http.Header),
	}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := request.Write(transport); err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("write HTTP CONNECT: %w", err)
	}
	reader := bufio.NewReader(transport)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = transport.Close()
		return nil, fmt.Errorf("HTTP CONNECT failed: %s", response.Status)
	}
	_ = transport.SetDeadline(time.Time{})
	return &bufferedConn{Conn: transport, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

func (c *bufferedConn) Close() error {
	return c.Conn.Close()
}

func bypassProxy(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
		port = ""
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	noProxy := firstEnv("NO_PROXY", "no_proxy")
	if noProxy == "" {
		return false
	}
	for _, entry := range strings.Split(noProxy, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			if ip := net.ParseIP(host); ip != nil && network.Contains(ip) {
				return true
			}
			continue
		}
		entryHost, entryPort, splitErr := net.SplitHostPort(entry)
		if splitErr == nil {
			entryHost = strings.Trim(entryHost, "[]")
			if strings.EqualFold(entryHost, host) && (entryPort == "" || entryPort == port) {
				return true
			}
			continue
		}
		if strings.EqualFold(entry, host) {
			return true
		}
		if strings.HasPrefix(entry, ".") && strings.HasSuffix(strings.ToLower(host), strings.ToLower(entry)) {
			return true
		}
		if strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(entry)) {
			return true
		}
	}
	return false
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
