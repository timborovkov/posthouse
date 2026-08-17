// Package netproxy dials TCP through optional SOCKS5/HTTP proxies.
// When no explicit proxy is set, ALL_PROXY / HTTPS_PROXY / HTTP_PROXY are honored.
package netproxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
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
	case "http", "https":
		return dialHTTPConnect(ctx, dialer, parsed, address)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use socks5:// or http://)", parsed.Scheme)
	}
}

func dialHTTPConnect(ctx context.Context, dialer *net.Dialer, proxyURL *url.URL, address string) (net.Conn, error) {
	proxyHost := proxyURL.Host
	if !strings.Contains(proxyHost, ":") {
		if proxyURL.Scheme == "https" {
			proxyHost += ":443"
		} else {
			proxyHost += ":80"
		}
	}
	connection, err := dialer.DialContext(ctx, "tcp", proxyHost)
	if err != nil {
		return nil, fmt.Errorf("connect to HTTP proxy: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", address, address)
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	request += "\r\n"
	if _, err := connection.Write([]byte(request)); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("write HTTP CONNECT: %w", err)
	}
	buffer := make([]byte, 1024)
	n, err := connection.Read(buffer)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	statusLine := string(buffer[:n])
	if !strings.Contains(statusLine, " 200 ") && !strings.HasPrefix(statusLine, "HTTP/1.1 200") && !strings.HasPrefix(statusLine, "HTTP/1.0 200") {
		_ = connection.Close()
		return nil, fmt.Errorf("HTTP CONNECT failed: %s", strings.Split(statusLine, "\r\n")[0])
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
