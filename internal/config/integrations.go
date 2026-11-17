package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MCP configures the Model Context Protocol endpoint (SCRIOBH_MCP_*).
type MCP struct {
	// Off turns the endpoint off (SCRIOBH_MCP=off): /mcp answers 404 and nothing
	// of it is mounted. On by default: the endpoint is reachable only with an API
	// key, and a key only ever does what its scope and pin allow.
	Off bool
	// AdminTools exposes the instance-administration tools (membership
	// management) to a key that has the admin scope (SCRIOBH_MCP_ADMIN_TOOLS=1).
	// Off by default. Secrets, sessions, key management, backups, Git remotes and
	// webhooks are never exposed whatever this says.
	AdminTools bool
	// PermanentTools lets the tools that destroy data beyond the trash be offered
	// (SCRIOBH_MCP_PERMANENT_TOOLS=1), to a key that can write, on a connection that
	// asks for them with ?permanent=1. Off by default. Purging a whole project or
	// Compendium is never offered.
	PermanentTools bool
	// AllowedOrigins are the browser origins that may call /mcp, besides the
	// instance's own (SCRIOBH_MCP_ALLOWED_ORIGINS, comma separated). A request
	// with no Origin header (every non-browser client) always passes the origin
	// check; it still needs a key.
	AllowedOrigins []string
	// MaxResultKB caps what one tool result may carry (SCRIOBH_MCP_MAX_RESULT_KB,
	// default 512). A larger result is refused with a message, never cut.
	MaxResultKB int
	// MaxBatch is the most JSON-RPC messages one POST may carry
	// (SCRIOBH_MCP_MAX_BATCH, default 16). Batching is a legacy-era feature.
	MaxBatch int
	// CallTimeoutS bounds one tool call (SCRIOBH_MCP_CALL_TIMEOUT_S, default 55,
	// under the server's write timeout).
	CallTimeoutS int
}

// Webhook configures outgoing webhooks (SCRIOBH_WEBHOOK_*).
type Webhook struct {
	// Off turns webhooks off (SCRIOBH_WEBHOOKS=off): nothing is delivered and the
	// webhook ops refuse to create one.
	Off bool
	// AllowPrivate lets a webhook target loopback, private and carrier-grade NAT
	// addresses, and plain http to them (SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1). Off by
	// default: a webhook is then a public https address and nothing else.
	AllowPrivate bool
	// MaxPerProject and MaxTotal cap how many webhooks exist
	// (SCRIOBH_WEBHOOK_MAX_PER_PROJECT, 10; SCRIOBH_WEBHOOK_MAX_TOTAL, 200).
	MaxPerProject, MaxTotal int
	// RatePerMin is how many deliveries the whole instance sends per minute
	// (SCRIOBH_WEBHOOK_RATE_PER_MIN, 300), QueueMax how many may wait
	// (SCRIOBH_WEBHOOK_QUEUE_MAX, 5000), PerWebhookQueueMax how many may wait for
	// one webhook (SCRIOBH_WEBHOOK_QUEUE_PER_WEBHOOK, 500).
	RatePerMin, QueueMax, PerWebhookQueueMax int
	// ProjectRatePerMin is the share of that rate one project may use
	// (SCRIOBH_WEBHOOK_PROJECT_RATE_PER_MIN, 120).
	ProjectRatePerMin int
	// ProjectQueueMax is how many deliveries may wait for one project's webhooks
	// (SCRIOBH_WEBHOOK_QUEUE_PER_PROJECT, 1000).
	ProjectQueueMax int
	// Concurrency is how many deliveries are in flight at once (SCRIOBH_WEBHOOK_CONCURRENCY, 4).
	Concurrency int
	// TimeoutS is one delivery's whole budget (SCRIOBH_WEBHOOK_TIMEOUT_S, 10).
	TimeoutS int
	// MaxAttempts is how often one delivery is tried before it is dead-lettered
	// (SCRIOBH_WEBHOOK_MAX_ATTEMPTS, 8).
	MaxAttempts int
	// KeepDays is how long finished deliveries stay listed (SCRIOBH_WEBHOOK_KEEP_DAYS, 14).
	KeepDays int
	// DebounceMs holds a document.updated notification back so a burst of saves is
	// one delivery (SCRIOBH_WEBHOOK_DEBOUNCE_MS, 5000; 0 sends at once).
	DebounceMs int
}

func loadIntegrations(lookup func(string) (string, bool), c *Config) error {
	get := func(k string) string {
		v, _ := lookup(k)
		return strings.TrimSpace(v)
	}
	// flag is a yes/no setting that is off unless set; off is one that is on unless set (true means switched off).
	flag := func(k string) (bool, error) { return ParseBool(k, get(k), false) }
	off := func(k string) (bool, error) {
		on, err := ParseBool(k, get(k), true)
		return !on, err
	}
	num := func(k string, def int, min int) (int, error) {
		v := get(k)
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < min {
			return 0, fmt.Errorf("%s: %q is not an integer of at least %d", k, v, min)
		}
		return n, nil
	}
	var err error
	if c.MCP.Off, err = off("SCRIOBH_MCP"); err != nil {
		return err
	}
	if c.MCP.AdminTools, err = flag("SCRIOBH_MCP_ADMIN_TOOLS"); err != nil {
		return err
	}
	if c.MCP.PermanentTools, err = flag("SCRIOBH_MCP_PERMANENT_TOOLS"); err != nil {
		return err
	}
	for _, o := range strings.Split(get("SCRIOBH_MCP_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		u, perr := url.Parse(o)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil {
			return fmt.Errorf("SCRIOBH_MCP_ALLOWED_ORIGINS: %q is not an origin like https://app.example.com", o)
		}
		c.MCP.AllowedOrigins = append(c.MCP.AllowedOrigins, strings.ToLower(u.Scheme+"://"+u.Host))
	}
	for _, f := range []struct {
		env      string
		def, min int
		dst      *int
	}{
		{"SCRIOBH_MCP_MAX_RESULT_KB", 512, 1, &c.MCP.MaxResultKB},
		{"SCRIOBH_MCP_MAX_BATCH", 16, 1, &c.MCP.MaxBatch},
		{"SCRIOBH_MCP_CALL_TIMEOUT_S", 55, 1, &c.MCP.CallTimeoutS},
		{"SCRIOBH_WEBHOOK_MAX_PER_PROJECT", 10, 1, &c.Webhook.MaxPerProject},
		{"SCRIOBH_WEBHOOK_MAX_TOTAL", 200, 1, &c.Webhook.MaxTotal},
		{"SCRIOBH_WEBHOOK_RATE_PER_MIN", 300, 1, &c.Webhook.RatePerMin},
		{"SCRIOBH_WEBHOOK_PROJECT_RATE_PER_MIN", 120, 1, &c.Webhook.ProjectRatePerMin},
		{"SCRIOBH_WEBHOOK_QUEUE_MAX", 5000, 1, &c.Webhook.QueueMax},
		{"SCRIOBH_WEBHOOK_QUEUE_PER_WEBHOOK", 500, 1, &c.Webhook.PerWebhookQueueMax},
		{"SCRIOBH_WEBHOOK_QUEUE_PER_PROJECT", 1000, 1, &c.Webhook.ProjectQueueMax},
		{"SCRIOBH_WEBHOOK_CONCURRENCY", 4, 1, &c.Webhook.Concurrency},
		{"SCRIOBH_WEBHOOK_TIMEOUT_S", 10, 1, &c.Webhook.TimeoutS},
		{"SCRIOBH_WEBHOOK_MAX_ATTEMPTS", 8, 1, &c.Webhook.MaxAttempts},
		{"SCRIOBH_WEBHOOK_KEEP_DAYS", 14, 1, &c.Webhook.KeepDays},
		{"SCRIOBH_WEBHOOK_DEBOUNCE_MS", 5000, 0, &c.Webhook.DebounceMs},
	} {
		if *f.dst, err = num(f.env, f.def, f.min); err != nil {
			return err
		}
	}
	if c.MCP.MaxResultKB > 16384 {
		return errors.New("SCRIOBH_MCP_MAX_RESULT_KB is at most 16384")
	}
	if c.MCP.MaxBatch > 256 {
		return errors.New("SCRIOBH_MCP_MAX_BATCH is at most 256")
	}
	if c.Webhook.TimeoutS > 60 {
		return errors.New("SCRIOBH_WEBHOOK_TIMEOUT_S is at most 60")
	}
	if c.Webhook.MaxAttempts > 20 {
		return errors.New("SCRIOBH_WEBHOOK_MAX_ATTEMPTS is at most 20")
	}
	if c.Webhook.Off, err = off("SCRIOBH_WEBHOOKS"); err != nil {
		return err
	}
	if c.Webhook.AllowPrivate, err = flag("SCRIOBH_WEBHOOK_ALLOW_PRIVATE"); err != nil {
		return err
	}
	return nil
}
