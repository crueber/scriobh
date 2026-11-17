package cli

import (
	"fmt"
	"sort"
	"strings"
)

// completion prints a shell completion script generated from the schema.
func (a *app) completion(args []string) error {
	rest := stripLeadingGlobals(args)
	if len(rest) > 0 {
		rest = rest[1:]
	}
	if len(rest) != 1 {
		return &UsageError{Msg: "completion needs a shell: bash, zsh or fish", Hint: "Example: source <(scriobh-cli completion bash)"}
	}
	schema, _ := a.rt.LoadSchema(a.ctx, a.g, false)
	script, err := Completion(NewIndex(schema), rest[0])
	if err != nil {
		return err
	}
	fmt.Fprint(a.rt.Stdout, script)
	return nil
}

// builtinWords are the commands that are not operations.
var builtinWords = []string{"completion", "config", "help", "login", "logout", "mcp", "ops", "schema", "skill", "version", "whoami"}

var skillVerbs = []string{"check", "install", "show", "uninstall"}

// flagNames lists the flags a command takes, with the global ones.
func flagNames(o Op, ix *Index) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	specs, _ := ix.specs(o)
	for name := range specs {
		add("--" + name)
	}
	for _, g := range globalFlags {
		add("--" + g.name)
	}
	sort.Strings(out)
	return out
}

// Completion renders the completion script for a shell.
func Completion(ix *Index, shell string) (string, error) {
	groups := ix.Groups()
	names := make([]string, 0, len(groups)+len(builtinWords))
	for n := range groups {
		names = append(names, n)
	}
	names = append(names, builtinWords...)
	sort.Strings(names)
	names = dedupe(names)
	verbs := func(g string) []string {
		var vs []string
		for _, o := range groups[g] {
			vs = append(vs, strings.Join(o.Words()[1:], "-"))
		}
		if g == "skill" {
			vs = append(vs, skillVerbs...)
		}
		sort.Strings(vs)
		return dedupe(vs)
	}
	switch shell {
	case "bash":
		var b strings.Builder
		b.WriteString("# bash completion for scriobh-cli, generated from the server's schema.\n# Load it with: source <(scriobh-cli completion bash)\n_scriobh_cli() {\n  local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
		b.WriteString("  if [ \"$COMP_CWORD\" -eq 1 ]; then\n    COMPREPLY=( $(compgen -W \"" + strings.Join(names, " ") + "\" -- \"$cur\") )\n    return\n  fi\n")
		b.WriteString("  if [ \"$COMP_CWORD\" -eq 2 ]; then\n    case \"${COMP_WORDS[1]}\" in\n")
		for _, g := range sortedKeys(groups) {
			b.WriteString("      " + g + ") COMPREPLY=( $(compgen -W \"" + strings.Join(verbs(g), " ") + "\" -- \"$cur\") ); return ;;\n")
		}
		b.WriteString("      skill) COMPREPLY=( $(compgen -W \"" + strings.Join(skillVerbs, " ") + "\" -- \"$cur\") ); return ;;\n")
		b.WriteString("      completion) COMPREPLY=( $(compgen -W \"bash zsh fish\" -- \"$cur\") ); return ;;\n")
		b.WriteString("    esac\n  fi\n  case \"${COMP_WORDS[1]} ${COMP_WORDS[2]}\" in\n")
		for _, o := range ix.SortedOps() {
			b.WriteString("    \"" + strings.Join(o.Words(), " ") + "\") COMPREPLY=( $(compgen -W \"" + strings.Join(flagNames(o, ix), " ") + "\" -- \"$cur\") ) ;;\n")
		}
		b.WriteString("    *) COMPREPLY=( $(compgen -W \"" + strings.Join(globalFlagList(), " ") + "\" -- \"$cur\") ) ;;\n  esac\n}\ncomplete -F _scriobh_cli scriobh-cli\n")
		return b.String(), nil
	case "zsh":
		var b strings.Builder
		b.WriteString("#compdef scriobh-cli\n# zsh completion for scriobh-cli, generated from the server's schema.\n# Load it with: source <(scriobh-cli completion zsh)\n_scriobh_cli() {\n  local -a opts\n")
		b.WriteString("  if (( CURRENT == 2 )); then\n    opts=(" + strings.Join(names, " ") + ")\n    compadd -a opts\n    return\n  fi\n")
		b.WriteString("  if (( CURRENT == 3 )); then\n    case \"$words[2]\" in\n")
		for _, g := range sortedKeys(groups) {
			b.WriteString("      " + g + ") opts=(" + strings.Join(verbs(g), " ") + "); compadd -a opts; return ;;\n")
		}
		b.WriteString("      skill) opts=(" + strings.Join(skillVerbs, " ") + "); compadd -a opts; return ;;\n")
		b.WriteString("      completion) opts=(bash zsh fish); compadd -a opts; return ;;\n")
		b.WriteString("    esac\n  fi\n  case \"$words[2] $words[3]\" in\n")
		for _, o := range ix.SortedOps() {
			b.WriteString("    \"" + strings.Join(o.Words(), " ") + "\") opts=(" + strings.Join(flagNames(o, ix), " ") + ") ;;\n")
		}
		b.WriteString("    *) opts=(" + strings.Join(globalFlagList(), " ") + ") ;;\n  esac\n  compadd -a opts\n}\ncompdef _scriobh_cli scriobh-cli\n")
		return b.String(), nil
	case "fish":
		var b strings.Builder
		b.WriteString("# fish completion for scriobh-cli, generated from the server's schema.\n# Load it with: scriobh-cli completion fish | source\ncomplete -c scriobh-cli -f\n")
		b.WriteString("complete -c scriobh-cli -n '__fish_use_subcommand' -a '" + strings.Join(names, " ") + "'\n")
		for _, g := range sortedKeys(groups) {
			b.WriteString("complete -c scriobh-cli -n '__fish_seen_subcommand_from " + g + "' -a '" + strings.Join(verbs(g), " ") + "'\n")
		}
		b.WriteString("complete -c scriobh-cli -n '__fish_seen_subcommand_from skill' -a '" + strings.Join(skillVerbs, " ") + "'\n")
		b.WriteString("complete -c scriobh-cli -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'\n")
		seen := map[string]bool{}
		for _, o := range ix.SortedOps() {
			ws := o.Words()
			for _, f := range flagNames(o, ix) {
				key := ws[0] + " " + f
				if seen[key] {
					continue
				}
				seen[key] = true
				b.WriteString("complete -c scriobh-cli -n '__fish_seen_subcommand_from " + ws[0] + "' -l " + strings.TrimPrefix(f, "--") + "\n")
			}
		}
		return b.String(), nil
	}
	return "", &UsageError{Msg: fmt.Sprintf("unknown shell %q; use bash, zsh or fish", shell)}
}

func globalFlagList() []string {
	var out []string
	for _, g := range globalFlags {
		out = append(out, "--"+g.name)
	}
	return out
}

func sortedKeys(m map[string][]Op) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
