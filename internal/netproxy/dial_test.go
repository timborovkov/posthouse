package netproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialTCPHTTPConnect(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, acceptErr := backend.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(conn, buf)
		_, _ = conn.Write([]byte("pong"))
	}()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack", http.StatusInternalServerError)
			return
		}
		clientConn, _, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			return
		}
		defer clientConn.Close()
		target, dialErr := net.DialTimeout("tcp", r.Host, 2*time.Second)
		if dialErr != nil {
			_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			return
		}
		defer target.Close()
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(target, clientConn) }()
		_, _ = io.Copy(clientConn, target)
	}))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialTCP(ctx, backend.Addr().String(), "http://"+strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("got %q", buf)
	}
}

func TestDialTCPHTTPConnectPreservesCoalescedGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.ReadAll(conn)
	}()

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go func() {
		conn, acceptErr := proxyLn.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		if !strings.Contains(string(buf[:n]), "CONNECT") {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n* OK IMAP4rev1 ready\r\n"))
		time.Sleep(50 * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialTCP(ctx, "imap.example.test:993", "http://"+proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "* OK IMAP4rev1 ready") {
		t.Fatalf("greeting lost: %q", buf[:n])
	}
}

func TestDialTCPHTTPConnectRejectsNon200EvenIfBodyMentions200(t *testing.T) {
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go func() {
		conn, acceptErr := proxyLn.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 28\r\n\r\nblocked: quota 200 of 500 used"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = DialTCP(ctx, "imap.example.test:993", "http://"+proxyLn.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

func TestBypassProxyLoopback(t *testing.T) {
	if !bypassProxy("127.0.0.1:993") || !bypassProxy("localhost:143") {
		t.Fatal("loopback should bypass proxy")
	}
}
