package netguard

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestObserversAreToldBeforeTheLookup(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	remove := Observe(func(network, address string) {
		mu.Lock()
		seen = append(seen, network+" "+address)
		mu.Unlock()
	})
	looked := false
	dial := Dialer(Options{Mode: PublicOnly, LookupIP: func(context.Context, string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		looked = true
		if len(seen) != 1 {
			t.Error("the name was looked up before the observers were told")
		}
		return []net.IP{net.IPv4(127, 0, 0, 1)}, nil // refused by the mode: nothing is connected
	}})
	if _, err := dial(context.Background(), "tcp", "writing.example.org:443"); err == nil {
		t.Fatal("a loopback answer was dialled in PublicOnly")
	}
	if !looked || len(seen) != 1 || seen[0] != "tcp writing.example.org:443" {
		t.Errorf("seen %v", seen)
	}
	remove()
	_, _ = dial(context.Background(), "tcp", "writing.example.org:443")
	if len(seen) != 1 {
		t.Error("a removed observer was still told")
	}
	// Two observers, each removed on its own.
	var a, b int
	ra := Observe(func(string, string) { a++ })
	rb := Observe(func(string, string) { b++ })
	Note("tcp", "x:1")
	ra()
	Note("tcp", "x:1")
	rb()
	Note("tcp", "x:1")
	if a != 1 || b != 2 {
		t.Errorf("observers were told %d and %d times, want 1 and 2", a, b)
	}
}

func TestIsLoopbackAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "127.8.9.10:1": true, "[::1]:443": true, "localhost:11434": true, "::1": true, "127.0.0.1": true,
		"example.com:443": false, "192.168.1.1:80": false, "10.0.0.1:1": false, "0.0.0.0:80": false, "localhost.example.com:80": false,
		"169.254.169.254:80": false, "[fe80::1]:80": false, "": false, "LOCALHOST:1": false,
	} {
		if got := IsLoopbackAddress(addr); got != want {
			t.Errorf("IsLoopbackAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}

// dialSites is every place in the program (tests aside) that can open an outbound connection: a file that builds an
// HTTP client or transport, a dialer, or calls one of the dialling functions directly. Each says how its connections
// are observed (see Note). A new one fails TestEveryDialSiteIsObserved until it is listed with its reason, which is
// what keeps "nothing leaves the machine" a statement about the whole program and not about the paths a test walks.
var dialSites = map[string]string{
	"internal/netguard/netguard.go":     "the guarded dialer: Note is its first statement",
	"internal/ai/client.go":             "dials through netguard.Dialer",
	"internal/webhook/send.go":          "dials through netguard.Dialer",
	"internal/gitmirror/provider.go":    "dials through netguard.Dialer",
	"internal/gitmirror/netguard.go":    "Git sync's own guarded dialer: guardedDial and the resolver call Note",
	"internal/auth/oidc.go":             "the sign-in client and the pinned client: both wrap their dial in netguard.Observed",
	"internal/domain/desktop/ollama.go": "the look for a local model server: netguard.Observed, one fixed loopback address",
	"internal/cli/client.go":            "the command-line client: another program, which talks to the server the person named",
	"internal/cli/mcp.go":               "the command-line client's MCP bridge: another program",
	"internal/bench/soak.go":            "the soak test's own load generator, against a server it started on loopback",
	"cmd/scriobh/main.go":               "the healthcheck command: asks this machine's own listener",
}

// dialNames are the identifiers that open a connection or build something that does, by package.
var dialNames = map[string]map[string]bool{
	"net":  {"Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialIP": true, "DialUnix": true, "Dialer": true, "LookupIP": true, "LookupHost": true, "LookupAddr": true, "DefaultResolver": true, "Resolver": true},
	"http": {"Client": true, "Transport": true, "Get": true, "Post": true, "PostForm": true, "Head": true, "DefaultClient": true, "DefaultTransport": true},
	"tls":  {"Dial": true, "DialWithDialer": true, "Dialer": true},
	"smtp": {"Dial": true, "SendMail": true},
	"ssh":  {"Dial": true},
}

func TestEveryDialSiteIsObserved(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string][]string{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			// The local names of the packages that can dial, as this file imports them.
			pkgs := map[string]string{}
			for _, im := range f.Imports {
				p := strings.Trim(im.Path.Value, `"`)
				base := p[strings.LastIndex(p, "/")+1:]
				if _, ok := dialNames[base]; !ok || (p != "net" && p != "net/http" && p != "crypto/tls" && p != "net/smtp" && p != "golang.org/x/crypto/ssh") {
					continue
				}
				name := base
				if im.Name != nil {
					name = im.Name.Name
				}
				pkgs[name] = base
			}
			if len(pkgs) == 0 {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if base, ok := pkgs[id.Name]; ok && dialNames[base][sel.Sel.Name] {
					found[rel] = append(found[rel], base+"."+sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) < 8 {
		t.Fatalf("only %d files with dial sites were found; has the scan stopped seeing them?", len(found))
	}
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if _, ok := dialSites[f]; !ok {
			t.Errorf("%s can open a connection (%s) and is not listed in dialSites: make it call netguard.Note (or dial through netguard.Dialer or netguard.Observed), and list it with how", f, strings.Join(found[f], ", "))
		}
	}
	for f := range dialSites {
		if _, ok := found[f]; !ok {
			t.Errorf("dialSites lists %s, which no longer has a dial site", f)
		}
	}
	// The sites that say they call Note do.
	for f, word := range map[string]string{"internal/netguard/netguard.go": "Note(network, addr)", "internal/gitmirror/netguard.go": "netguard.Note(", "internal/gitmirror/transport.go": "netguard.Note(",
		"internal/auth/oidc.go": "netguard.Observed(", "internal/domain/desktop/ollama.go": "netguard.Observed("} {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || !strings.Contains(string(src), word) {
			t.Errorf("%s does not contain %q: its connections are not observed", f, word)
		}
	}
}
