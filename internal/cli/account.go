package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// account handles login, logout and config: the commands that manage the saved
// server and key.
func (a *app) account(name string, args []string) error {
	rest := stripLeadingGlobals(args)
	if len(rest) > 0 {
		rest = rest[1:]
	}
	var url, key string
	noVerify, all := false, false
	g := a.g
	for i := 0; i < len(rest); i++ {
		x := rest[i]
		n, val, hasVal := strings.Cut(strings.TrimLeft(x, "-"), "=")
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(rest) {
				return "", usagef("--%s needs a value", n)
			}
			i++
			return rest[i], nil
		}
		var err error
		switch n {
		case "url", "server":
			url, err = next()
		case "key":
			key, err = next()
		case "no-verify":
			noVerify = true
		case "all":
			all = true
		case "json", "quiet", "q":
			if k, _ := findGlobal(n, n == "q"); k.name != "" {
				err = g.set(k.name, "true")
			}
		case "config", "timeout":
			var v string
			if v, err = next(); err == nil {
				err = g.set(n, v)
			}
		default:
			if n == "path" && name == "config" {
				continue
			}
			err = usagef("unknown option %s for %q", x, name)
		}
		if err != nil {
			return err
		}
	}
	a.g = g
	switch name {
	case "login":
		return a.login(url, key, noVerify)
	case "logout":
		return a.logout(all)
	}
	return a.configShow(len(rest) > 0 && rest[0] == "path")
}

func (a *app) login(url, key string, noVerify bool) error {
	if url == "" {
		url = firstNonEmpty(a.rt.Getenv("SCRIOBH_URL"))
	}
	if url == "" {
		return &UsageError{Msg: "login needs --url", Hint: "Example: scriobh-cli login --url https://write.example.com --key scr_..."}
	}
	if key == "-" {
		b, err := io.ReadAll(io.LimitReader(a.rt.Stdin, 4096))
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		key = firstNonEmpty(a.rt.Getenv("SCRIOBH_API_KEY"), a.rt.Getenv("SCRIOBH_TOKEN"))
	}
	if key == "" {
		return &UsageError{Msg: "login needs --key (or - to read it from standard input, or SCRIOBH_API_KEY)", Hint: "Create a key in Scríobh under Settings, API keys."}
	}
	base, err := NormalizeServer(url)
	if err != nil {
		return err
	}
	g := a.g
	g.Server, g.Key = base.String(), key
	var who map[string]any
	if !noVerify {
		cl, err := a.rt.NewClient(g)
		if err != nil {
			return err
		}
		cl.Retries = 0
		resp, err := cl.Do(a.ctx, "GET", "/api/v1/_whoami", nil, nil, nil)
		if err != nil {
			return err
		}
		if resp.Status >= 400 {
			return fromServer(resp.Status, resp.Header, resp.Body)
		}
		_ = json.Unmarshal(resp.Body, &who)
	}
	path, err := a.rt.SaveConfig(a.g, Config{Server: base.String(), Key: key})
	if err != nil {
		return &Fail{Code: "write_failed", Exit: ExitGeneric, Message: "Cannot save the login: " + errText(err)}
	}
	data := map[string]any{"server": base.String(), "config": path, "key_saved": true, "verified": !noVerify}
	if who != nil {
		data["whoami"] = who
	}
	a.printResult(Op{Name: "login"}, 200, data, nil, nil)
	if isHTTPNotLoopback(base.String()) && !a.g.Quiet {
		fmt.Fprintf(a.rt.Stderr, "warning: %s is not https; the key travels in clear text.\n", base.Host)
	}
	return nil
}

func isHTTPNotLoopback(s string) bool {
	u, err := NormalizeServer(s)
	return err == nil && u.Scheme == "http" && !isLoopback(u)
}

func (a *app) logout(all bool) error {
	cfg, err := a.rt.LoadConfig(a.g)
	if err != nil {
		return err
	}
	path, _ := a.rt.configPath(a.g)
	had := cfg.Key != ""
	if all {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return &Fail{Code: "write_failed", Exit: ExitGeneric, Message: "Cannot remove " + path + ": " + errText(err)}
		}
	} else if had {
		cfg.Key = ""
		if _, err := a.rt.SaveConfig(a.g, cfg); err != nil {
			return &Fail{Code: "write_failed", Exit: ExitGeneric, Message: "Cannot update " + path + ": " + errText(err)}
		}
	}
	a.printResult(Op{Name: "logout"}, 200, map[string]any{"key_removed": had, "config": path, "file_removed": all}, nil, nil)
	return nil
}

// configShow reports where the settings come from. It never prints the key.
func (a *app) configShow(pathOnly bool) error {
	path, _ := a.rt.configPath(a.g)
	if pathOnly {
		fmt.Fprintln(a.rt.Stdout, path)
		return nil
	}
	cfg, err := a.rt.LoadConfig(a.g)
	if err != nil {
		return err
	}
	server, key, _ := a.rt.Credentials(a.g)
	src := func(flag, env, file string) string {
		switch {
		case flag != "":
			return "flag"
		case env != "":
			return "environment"
		case file != "":
			return "config file"
		}
		return "unset"
	}
	data := map[string]any{
		"config":        path,
		"server":        server,
		"server_from":   src(a.g.Server, a.rt.Getenv("SCRIOBH_URL"), cfg.Server),
		"key_set":       key != "",
		"key_from":      src(a.g.Key, firstNonEmpty(a.rt.Getenv("SCRIOBH_API_KEY"), a.rt.Getenv("SCRIOBH_TOKEN")), cfg.Key),
		"key_prefix":    keyHint(key),
		"cli_version":   Version,
		"cli_hash":      Embedded().Hash,
		"cache_enabled": true,
	}
	a.printResult(Op{Name: "config"}, 200, data, nil, nil)
	return nil
}

// keyHint shows enough of a key to recognise it and no more.
func keyHint(k string) string {
	if len(k) < 12 {
		return ""
	}
	return k[:8] + "…"
}
