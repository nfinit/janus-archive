package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nfinit/janus/internal/protocol"
	"github.com/nfinit/janus/internal/version"
)

// Config is the client's runtime configuration (from a config file/flags).
type Config struct {
	ServerURL string // base URL of the JANUS server
	Token     string // this client instance's binding token
	Wait      int    // poll hold time in seconds; 0 = plain polling
	Terminal  TerminalConfig
	// RetryDelay is how long to wait after a failed poll before retrying.
	RetryDelay time.Duration
}

// Client is a JANUS client instance: one token, one host presence.
type Client struct {
	cfg      Config
	http     *http.Client
	hostname string
}

func New(cfg Config) *Client {
	host, _ := os.Hostname()
	return &Client{
		cfg:      cfg,
		hostname: host,
		// Timeout must exceed the server's max poll hold; the server holds
		// a poll up to ~30s, so give generous headroom.
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Run is the client's work loop: poll, execute, report, repeat. It always
// dials the server; the server never dials it.
func (c *Client) Run(ctx context.Context) error {
	log.Printf("%s connecting to %s", version.Client(), c.cfg.ServerURL)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		work, err := c.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("poll error: %v (retrying in %s)", err, c.cfg.RetryDelay)
			if !sleep(ctx, c.cfg.RetryDelay) {
				return ctx.Err()
			}
			continue
		}
		if work == nil {
			continue // nothing to do; poll again
		}
		c.handle(ctx, work)
	}
}

func (c *Client) poll(ctx context.Context) (*protocol.Work, error) {
	req := protocol.PollRequest{
		Token:    c.cfg.Token,
		Wait:     c.cfg.Wait,
		Version:  version.Client(),
		Hostname: c.hostname,
	}
	var resp protocol.PollResponse
	if err := c.post(ctx, "/api/poll", req, &resp); err != nil {
		return nil, err
	}
	return resp.Work, nil
}

func (c *Client) handle(ctx context.Context, work *protocol.Work) {
	switch work.Type {
	case protocol.WorkTypeTerminal:
		log.Printf("terminal: %s", oneLine(work.Command))
		res := RunTerminal(ctx, c.cfg.Terminal, *work)
		c.report(ctx, work.ID, res)
	default:
		// Unknown work type: report it as a spawn error so the agent learns.
		c.report(ctx, work.ID, protocol.TerminalResult{
			SpawnError: "client does not understand work type " + work.Type,
			ExitCode:   -1,
		})
	}
}

func (c *Client) report(ctx context.Context, workID string, res protocol.TerminalResult) {
	req := protocol.ResultRequest{Token: c.cfg.Token, WorkID: workID, Result: res}
	var resp protocol.ResultResponse
	if err := c.post(ctx, "/api/result", req, &resp); err != nil {
		log.Printf("result report failed for %s: %v", workID, err)
	}
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.cfg.ServerURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e protocol.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("server HTTP %d: %s", resp.StatusCode, e.Error)
		}
		return fmt.Errorf("server HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
