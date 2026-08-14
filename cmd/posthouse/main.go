package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/posthousehq/posthouse/internal/cli"
	"github.com/posthousehq/posthouse/internal/config"
	"github.com/posthousehq/posthouse/internal/service"
)

func main() {
	root := flag.NewFlagSet("posthouse", flag.ContinueOnError)
	root.SetOutput(os.Stderr)
	configPath := root.String("config", "", "config file path")
	if err := root.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	store, err := config.New(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "posthouse:", err)
		os.Exit(1)
	}
	ctx, cancel := cli.SignalContext()
	defer cancel()
	application := cli.New(service.New(store), os.Stdout, os.Stderr)
	if err := application.Run(ctx, root.Args()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "posthouse:", err)
		os.Exit(1)
	}
}
