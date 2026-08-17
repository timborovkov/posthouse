package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/timborovkov/posthouse/internal/httpauth"
	"github.com/timborovkov/posthouse/internal/mcpserver"
)

func (c *CLI) serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	address := flags.String("address", "", "listen address (default 127.0.0.1:8791, or 0.0.0.0:$PORT on hosted platforms)")
	allowContainerListener := flags.Bool("allow-container-listener", false, "allow cleartext non-loopback binding inside an externally loopback- or TLS-constrained container network")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return c.runHTTP(ctx, *address, *allowContainerListener)
}

func (c *CLI) runHTTP(ctx context.Context, address string, allowContainerListener bool) error {
	key, err := httpauth.AccessKey()
	if err != nil {
		return err
	}
	listen, allowContainerListener := resolveListenAddress(address, allowContainerListener)
	logger := slog.New(slog.NewJSONHandler(c.stderr, nil))
	return mcpserver.New(c.service).RunHTTP(ctx, listen, key, allowContainerListener, logger)
}

func resolveListenAddress(address string, allowContainerListener bool) (string, bool) {
	address = strings.TrimSpace(address)
	if address == "" {
		address = strings.TrimSpace(os.Getenv("POSTHOUSE_HTTP_ADDRESS"))
	}
	if address == "" {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			address = "0.0.0.0:" + port
			allowContainerListener = true
		}
	}
	if address == "" {
		address = "127.0.0.1:8791"
	}
	if truthyEnv("POSTHOUSE_ALLOW_CONTAINER_LISTENER") {
		allowContainerListener = true
	}
	return address, allowContainerListener
}

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (c *CLI) mcp(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse mcp <stdio|http>")
	}
	server := mcpserver.New(c.service)
	switch args[0] {
	case "stdio":
		return server.RunStdio(ctx)
	case "http":
		flags := flag.NewFlagSet("mcp http", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		address := flags.String("address", "", "listen address (default 127.0.0.1:8791, or 0.0.0.0:$PORT on hosted platforms)")
		allowContainerListener := flags.Bool("allow-container-listener", false, "allow cleartext non-loopback binding inside an externally loopback- or TLS-constrained container network")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return c.runHTTP(ctx, *address, *allowContainerListener)
	default:
		return fmt.Errorf("unknown MCP transport %q", args[0])
	}
}
