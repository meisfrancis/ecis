// Command ecis is a terminal UI for Amazon ECS, in the spirit of k9s: a
// keyboard-driven view of clusters, services, tasks and containers, with live
// CPU and memory charts from CloudWatch and spend from Cost Explorer.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/config"
	"github.com/meisfrancis/ecis/internal/ui"
	"github.com/meisfrancis/ecis/internal/view"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=v1.2.3" ./cmd/ecis
var version = "dev"

// startupTimeout bounds credential and identity resolution before the UI opens.
const startupTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ecis: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	profile     string
	region      string
	cluster     string
	command     string
	refresh     int
	readOnly    bool
	showVersion bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.profile, "profile", "", "AWS profile to use (defaults to AWS_PROFILE)")
	flag.StringVar(&o.region, "region", "", "AWS region to use (defaults to AWS_REGION)")
	flag.StringVar(&o.cluster, "cluster", "", "ECS cluster to open on startup")
	flag.StringVar(&o.command, "command", "", "view to open on startup, e.g. services, pulse, cost")
	flag.StringVar(&o.command, "c", "", "shorthand for -command")
	flag.IntVar(&o.refresh, "refresh", 0, "refresh interval in seconds")
	flag.BoolVar(&o.readOnly, "readonly", false, "disable every mutating action")
	flag.BoolVar(&o.showVersion, "version", false, "print the version and exit")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "ecis — an ECS interactive shell\n\nUsage:\n  ecis [flags]\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nOnce running, press ? for the keyboard reference or : for the command prompt.\n")
	}
	flag.Parse()
	return o
}

func run() error {
	opts := parseFlags()
	if opts.showVersion {
		fmt.Printf("ecis %s\n", version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		// A broken config file should not stop the session; report it and carry
		// on with defaults so the user can still get in and fix it.
		fmt.Fprintf(os.Stderr, "ecis: %v (continuing with defaults)\n", err)
	}
	applyFlags(cfg, opts)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	initCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	client, err := awsx.New(initCtx, cfg.Profile, cfg.Region)
	if err != nil {
		return err
	}

	app := ui.NewApp(cfg, client, version)
	registry := view.NewRegistry(app)
	app.CommandFn = registry.Exec
	app.SuggestFn = registry.Suggest
	app.HelpFn = func() {
		if err := registry.Goto("help", nil); err != nil {
			app.Flash().Err(err)
		}
	}

	// Identity resolution needs a network round trip; do it in the background so
	// a slow STS call cannot delay the first paint.
	go func() {
		id, err := client.Identity(ctx)
		if err != nil {
			app.QueueUpdateDraw(func() {
				app.Flash().Warnf("could not resolve caller identity: %v", err)
			})
			return
		}
		app.QueueUpdateDraw(func() {
			app.SetIdentity(id)
			app.RefreshHeader()
		})
	}()

	app.Reset(view.NewClusters(app))
	if err := openStartupView(app, registry, cfg, opts); err != nil {
		app.Flash().Err(err)
	}

	// Ctrl-C at the terminal and SIGTERM should both tear the UI down cleanly.
	// Stop is lock-protected and safe from any goroutine; queueing it would
	// block forever, since it shuts down the loop that drains the queue.
	go func() {
		<-ctx.Done()
		app.Stop()
	}()

	return app.Run(ctx)
}

// applyFlags folds command line overrides into the loaded configuration.
func applyFlags(cfg *config.Config, o options) {
	if o.profile != "" {
		cfg.Profile = o.profile
	}
	if o.region != "" {
		cfg.Region = o.region
	}
	if o.cluster != "" {
		cfg.Cluster = o.cluster
	}
	if o.refresh > 0 {
		cfg.RefreshRate = o.refresh
	}
	if o.readOnly {
		cfg.ReadOnly = true
	}
	cfg.Validate()
}

// openStartupView honours -cluster and -command, landing the user directly on
// the view they asked for instead of the cluster list.
func openStartupView(app *ui.App, registry *view.Registry, cfg *config.Config, o options) error {
	if cfg.Cluster != "" {
		app.SetCluster(cfg.Cluster)
	}

	command := strings.TrimSpace(o.command)
	if command == "" {
		if cfg.Cluster == "" {
			return nil
		}
		command = "services"
	}
	return registry.Exec(command)
}
