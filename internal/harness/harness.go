// Package harness turns "run this prompt with that agent" into an argv.
//
// A node may name a harness, or inherit one from its role, or leave it "auto".
// `auto` prefers the harness that launched this process, so a worker spawned by
// jcode keeps using jcode instead of silently switching families. Everything
// here is data plus a small argv builder: no SDKs, no daemons.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Spec is one invocation request.
type Spec struct {
	Prompt   string
	Workdir  string
	Provider string
	Model    string
	Tools    []string
	Agent    string // forge/helioslite agent id
	Extra    []string
}

// Info is what detection learned about one harness.
type Info struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	Headless  bool   `json:"headless"`
	Note      string `json:"note,omitempty"`
}

// Adapter knows how to detect one harness and build its argv.
type Adapter struct {
	Name     string
	Bin      string
	Headless bool
	Note     string
	Args     func(bin string, s Spec) []string
	DetectFn func() (Info, bool) // optional extra detection
}

// Order is the auto-detect preference after the launching harness.
var Order = []string{"jcode", "kcode", "codex", "forge", "helioslite"}

// Registry returns the built-in adapters.
func Registry() map[string]*Adapter {
	m := map[string]*Adapter{}
	for _, a := range []*Adapter{jcodeAdapter("jcode"), jcodeAdapter("kcode"), codexAdapter(), forgeAdapter("forge"), forgeAdapter("helioslite")} {
		m[a.Name] = a
	}
	return m
}

func jcodeAdapter(name string) *Adapter {
	return &Adapter{
		Name:     name,
		Bin:      name,
		Headless: true,
		Args: func(bin string, s Spec) []string {
			args := []string{"run", "--json"}
			if s.Workdir != "" {
				args = append(args, "-C", s.Workdir)
			}
			if s.Provider != "" {
				args = append(args, "-p", s.Provider)
			}
			if s.Model != "" {
				args = append(args, "-m", s.Model)
			}
			if len(s.Tools) > 0 {
				args = append(args, "--tools", strings.Join(s.Tools, ","))
			}
			args = append(args, s.Extra...)
			return append(args, s.Prompt)
		},
	}
}

func codexAdapter() *Adapter {
	return &Adapter{
		Name:     "codex",
		Bin:      "codex",
		Headless: true,
		Args: func(bin string, s Spec) []string {
			args := []string{"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write"}
			if s.Workdir != "" {
				args = append(args, "-C", s.Workdir)
			}
			if s.Model != "" {
				args = append(args, "-m", s.Model)
			}
			args = append(args, s.Extra...)
			return append(args, s.Prompt)
		},
	}
}

func forgeAdapter(name string) *Adapter {
	return &Adapter{
		Name:     name,
		Bin:      name,
		Headless: true,
		Note:     "-p is single-shot; --agent selects a configured agent",
		Args: func(bin string, s Spec) []string {
			args := []string{}
			if s.Workdir != "" {
				args = append(args, "-C", s.Workdir)
			}
			if s.Agent != "" {
				args = append(args, "--agent", s.Agent)
			}
			args = append(args, s.Extra...)
			args = append(args, "-p", s.Prompt)
			return args
		},
	}
}

// Detect probes every built-in harness. Only `<bin> --version` is run; nothing
// is billed and nothing is written.
func Detect() []Info {
	reg := Registry()
	out := make([]Info, 0, len(Order))
	for _, name := range Order {
		a := reg[name]
		info := Info{Name: name, Headless: a.Headless, Note: a.Note}
		path, err := exec.LookPath(a.Bin)
		if err != nil {
			// fall back to the conventional install location
			cand := filepath.Join(homeDirectories()[0], ".local", "bin", a.Bin)
			if _, statErr := os.Stat(cand); statErr == nil {
				path = cand
			}
		}
		if path != "" {
			info.Installed = true
			info.Path = path
			info.Version = version(path)
		}
		out = append(out, info)
	}
	return out
}

func homeDirectories() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"."}
	}
	return []string{home}
}

func version(path string) string {
	cmd := exec.Command(path, "--version")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}

// Launching reports which harness started this process, or "" if unknown.
// The marker env vars are set by the harness itself; JCODE_BIN disambiguates
// jcode from its kcode fork, which reads the same environment.
func Launching() string {
	switch {
	case os.Getenv("KCODE_SESSION_ID") != "" || strings.Contains(os.Getenv("JCODE_BIN"), "kcode"):
		return "kcode"
	case os.Getenv("JCODE_SESSION_ID") != "" || os.Getenv("JCODE_SOCKET") != "":
		return "jcode"
	case os.Getenv("CODEX_HOME") != "" || os.Getenv("CODEX_SESSION_ID") != "":
		return "codex"
	case os.Getenv("HELIOSLITE_SESSION_ID") != "":
		return "helioslite"
	case os.Getenv("FORGE_SESSION_ID") != "" || os.Getenv("FORGE_EDITOR") != "":
		return "forge"
	}
	return ""
}

// Resolve picks an adapter. "auto" and "" prefer the launching harness, then
// the first installed headless harness in Order.
func Resolve(name string) (*Adapter, error) {
	reg := Registry()
	switch name {
	case "", "auto":
		if l := Launching(); l != "" {
			if a, ok := reg[l]; ok {
				if _, err := exec.LookPath(a.Bin); err == nil {
					return a, nil
				}
			}
		}
		for _, n := range Order {
			a := reg[n]
			if _, err := exec.LookPath(a.Bin); err == nil {
				return a, nil
			}
		}
		return nil, fmt.Errorf("no agent harness found on PATH (looked for %s); set harness: exec with a command template, or install one", strings.Join(Order, ", "))
	case "exec":
		return nil, nil // caller handles the exec fallback
	}
	if a, ok := reg[name]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("unknown harness %q; known: %s, auto, exec", name, strings.Join(Order, ", "))
}

// Command builds the executable and argv for a spec.
func (a *Adapter) Command(s Spec) (string, []string, error) {
	if a == nil {
		return "", nil, fmt.Errorf("no harness adapter")
	}
	if s.Prompt == "" {
		return "", nil, fmt.Errorf("harness %s: empty prompt", a.Name)
	}
	path, err := exec.LookPath(a.Bin)
	if err != nil {
		return "", nil, fmt.Errorf("harness %s not found on PATH", a.Name)
	}
	return path, a.Args(path, s), nil
}

// ExecCommand builds an argv from an explicit template such as
// "mytool --prompt {prompt} --dir {dir}". Placeholders are substituted
// literally; {stdin} is replaced by "-" and the prompt is passed on stdin.
func ExecCommand(template, prompt, workdir, model string) (string, []string, bool, error) {
	fields, err := splitTemplate(template)
	if err != nil {
		return "", nil, false, err
	}
	if len(fields) == 0 {
		return "", nil, false, fmt.Errorf("empty exec template")
	}
	useStdin := false
	for i := 1; i < len(fields); i++ {
		if fields[i] == "{stdin}" {
			fields[i] = "-"
			useStdin = true
			continue
		}
		fields[i] = strings.ReplaceAll(fields[i], "{prompt}", prompt)
		fields[i] = strings.ReplaceAll(fields[i], "{dir}", workdir)
		fields[i] = strings.ReplaceAll(fields[i], "{model}", model)
	}
	return fields[0], fields[1:], useStdin, nil
}

// splitTemplate is a quote-aware split so a template can carry a prompt with
// spaces in a single argument.
//
// Backslash is NOT an escape character here. A template is usually a shell
// command, and `printf 'a\nb'` must reach the shell with its backslash intact:
// treating `\n` as an escape produced the literal text "anb" and silently broke
// every template that wrote a newline. Backslash is only special inside
// single quotes, where the shell itself removes it.
func splitTemplate(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote == '\'':
			// inside single quotes the shell handles every escape itself
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced quote in exec template")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}
