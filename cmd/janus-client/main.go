// Command janus-client is the JANUS Phase 0 client: it connects to a JANUS
// server with a binding token and offers the agent a stateless terminal on
// this host. It runs as an ordinary local user and switches no privileges.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nfinit/janus/internal/client"
	"github.com/nfinit/janus/internal/version"
)

// fileConfig is the on-disk config (JSON). Flags override file values.
type fileConfig struct {
	ServerURL      string `json:"server_url"`
	Token          string `json:"token"`
	Wait           *int   `json:"wait,omitempty"`
	DefaultTimeout *int   `json:"default_timeout_seconds,omitempty"`
	MaxTimeout     *int   `json:"max_timeout_seconds,omitempty"`
	OutputCapKiB   *int   `json:"output_cap_kib,omitempty"`
}

func main() {
	var (
		configPath = flag.String("config", "", "path to JSON config file")
		server     = flag.String("server", "", "JANUS server base URL (overrides config)")
		token      = flag.String("token", "", "binding token (overrides config)")
		wait       = flag.Int("wait", -1, "poll hold time in seconds; 0 = plain polling")
		showV      = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showV {
		log.SetFlags(0)
		log.Println(version.Client())
		return
	}

	var fc fileConfig
	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			log.Fatalf("read config: %v", err)
		}
		if err := json.Unmarshal(data, &fc); err != nil {
			log.Fatalf("parse config: %v", err)
		}
	}
	if *server != "" {
		fc.ServerURL = *server
	}
	if *token != "" {
		fc.Token = *token
	}
	if *wait >= 0 {
		fc.Wait = wait
	}

	if strings.TrimSpace(fc.ServerURL) == "" || strings.TrimSpace(fc.Token) == "" {
		log.Fatal("server URL and token are required (via -config or -server/-token)")
	}

	term := client.DefaultTerminalConfig()
	if fc.DefaultTimeout != nil {
		term.DefaultTimeout = time.Duration(*fc.DefaultTimeout) * time.Second
	}
	if fc.MaxTimeout != nil {
		term.MaxTimeout = time.Duration(*fc.MaxTimeout) * time.Second
	}
	if fc.OutputCapKiB != nil {
		term.OutputCap = *fc.OutputCapKiB * 1024
	}

	cfg := client.Config{
		ServerURL:  fc.ServerURL,
		Token:      fc.Token,
		Wait:       25, // sensible long-poll default
		Terminal:   term,
		RetryDelay: 5 * time.Second,
	}
	if fc.Wait != nil {
		cfg.Wait = *fc.Wait
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c := client.New(cfg)
	if err := c.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("client stopped: %v", err)
	}
	log.Println("client stopped")
}
