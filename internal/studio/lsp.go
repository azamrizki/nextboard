package studio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"nextboard/internal/machine"
)

// Language servers (docs/API-studio.md §7.3): the hub starts the machine's own
// server in the project folder and bridges its stdio to a WebSocket. Nothing is
// installed for the user, and a project's own node_modules/.bin is never on the
// path: opening a project must not run a program that came with it.

// LangServer is one entry of the catalog.
type LangServer struct {
	ID        string
	Name      string
	Languages []string // Monaco language ids
	// Candidates are tried in order; the first word is looked up on PATH.
	Candidates [][]string
	Install    string
	// Extra serves its languages next to their main server, and only while the
	// extension that asks for it is installed.
	Extra bool
}

// LangServers is the catalog. CSS, HTML and JSON are absent: the editor brings
// its own services for them.
var LangServers = []LangServer{
	{ID: "go", Name: "gopls", Languages: []string{"go"}, Candidates: [][]string{{"gopls"}}, Install: "go install golang.org/x/tools/gopls@latest"},
	{ID: "typescript", Name: "TypeScript", Languages: []string{"typescript", "javascript"},
		Candidates: [][]string{{"typescript-language-server", "--stdio"}, {"vtsls", "--stdio"}}, Install: "npm install -g typescript-language-server typescript@6"},
	{ID: "python", Name: "Python", Languages: []string{"python"},
		Candidates: [][]string{{"basedpyright-langserver", "--stdio"}, {"pyright-langserver", "--stdio"}, {"pylsp"}, {"jedi-language-server"}}, Install: "npm install -g pyright"},
	{ID: "php", Name: "PHP", Languages: []string{"php"},
		Candidates: [][]string{{"intelephense", "--stdio"}, {"phpactor", "language-server"}}, Install: "npm install -g intelephense"},
	{ID: "rust", Name: "rust-analyzer", Languages: []string{"rust"}, Candidates: [][]string{{"rust-analyzer"}}, Install: "rustup component add rust-analyzer"},
	{ID: "cpp", Name: "clangd", Languages: []string{"c", "cpp"}, Candidates: [][]string{{"clangd"}}, Install: "sudo apt install clangd"},
	{ID: "java", Name: "Eclipse JDT", Languages: []string{"java"}, Candidates: [][]string{{"jdtls"}}, Install: "brew install jdtls"},
	{ID: "ruby", Name: "Ruby", Languages: []string{"ruby"}, Candidates: [][]string{{"ruby-lsp"}, {"solargraph", "stdio"}}, Install: "gem install ruby-lsp"},
	{ID: "csharp", Name: "C#", Languages: []string{"csharp"}, Candidates: [][]string{{"csharp-ls"}, {"omnisharp", "-lsp"}}, Install: "dotnet tool install -g csharp-ls"},
	{ID: "kotlin", Name: "Kotlin", Languages: []string{"kotlin"}, Candidates: [][]string{{"kotlin-language-server"}}, Install: "brew install kotlin-language-server"},
	{ID: "swift", Name: "SourceKit-LSP", Languages: []string{"swift"}, Candidates: [][]string{{"sourcekit-lsp"}}, Install: "Installed with the Swift toolchain"},
	{ID: "yaml", Name: "YAML", Languages: []string{"yaml"}, Candidates: [][]string{{"yaml-language-server", "--stdio"}}, Install: "npm install -g yaml-language-server"},
	{ID: "shell", Name: "Bash", Languages: []string{"shell"}, Candidates: [][]string{{"bash-language-server", "start"}}, Install: "npm install -g bash-language-server"},
	{ID: "lua", Name: "Lua", Languages: []string{"lua"}, Candidates: [][]string{{"lua-language-server"}}, Install: "brew install lua-language-server"},
	{ID: "dockerfile", Name: "Dockerfile", Languages: []string{"dockerfile"}, Candidates: [][]string{{"docker-langserver", "--stdio"}}, Install: "npm install -g dockerfile-language-server-nodejs"},
	{ID: "vue", Name: "Vue", Languages: []string{"vue"}, Candidates: [][]string{{"vue-language-server", "--stdio"}}, Install: "npm install -g @vue/language-server typescript-language-server typescript@6"},
	{ID: "toml", Name: "Taplo", Languages: []string{"toml"}, Candidates: [][]string{{"taplo", "lsp", "stdio"}}, Install: "cargo install taplo-cli --locked --features lsp"},
	{ID: "tailwind", Name: "Tailwind CSS", Languages: []string{"html", "css", "scss", "less", "javascript", "typescript", "vue"}, Extra: true,
		Candidates: [][]string{{"tailwindcss-language-server", "--stdio"}}, Install: "npm install -g @tailwindcss/language-server"},
}

// ErrNoLangServer: the catalog has no such server or none is installed.
var ErrNoLangServer = errors.New("language server tidak ditemukan di machine ini")

// lspPath adds the usual per-user install folders, which login shells often
// leave out of a non-interactive PATH. The last three are where a Go tarball
// goes: its PATH line usually sits in ~/.bashrc, below the check that ends a
// non-interactive shell, and gopls and Delve run go themselves.
const lspPath = `PATH="$PATH:$HOME/go/bin:$HOME/.cargo/bin:$HOME/.local/bin:$HOME/.npm-global/bin:/usr/local/go/bin:$HOME/.local/go/bin:$HOME/.go/bin"; export PATH; `

// goToolchainInstall comes before gopls on a machine that has no go.
const goToolchainInstall = "sudo apt install golang-go"

const lspProbeTimeout = 15 * time.Second

// LSPStatus is one row of GET /api/projects/{id}/lsp.
type LSPStatus struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Languages []string `json:"languages"`
	Available bool     `json:"available"`
	Command   string   `json:"command"`
	Install   string   `json:"install"`
	Extra     bool     `json:"extra,omitempty"`
	// Plugin is where TypeScript's server finds the Vue plugin, when Vue's server
	// leaves the script of a file to it (version 3 and later).
	Plugin string `json:"plugin,omitempty"`
}

// vuePluginScript prints the package folder of Vue's server when it needs the
// plugin. tsserver resolves the plugin from there, hoisted or not.
const vuePluginScript = lspPath + `P=$(readlink -f "$(command -v vue-language-server)") || exit 0; D=${P%/bin/*}; ` +
	`grep -Eq '"version": *"([3-9]|[1-9][0-9])\.' "$D/package.json" 2>/dev/null || exit 0; ` +
	`[ -d "$D/node_modules/@vue/typescript-plugin" ] || [ -d "$D/../typescript-plugin" ] || exit 0; printf '%s' "$D"; exit 0`

// tsserverScript prints ok when typescript-language-server has a tsserver to run:
// in the project or next to itself. TypeScript 7 has none, and the server exits
// without one, so it counts as absent and the editor keeps its own service.
const tsserverScript = lspPath + `P=$(readlink -f "$(command -v typescript-language-server)") || exit 0; D=${P%/lib/*}; ` +
	`for t in node_modules/typescript/lib/tsserver.js "$D/node_modules/typescript/lib/tsserver.js" "$D/../typescript/lib/tsserver.js"; do ` +
	`[ -f "$t" ] && { printf ok; exit 0; }; done; exit 0`

// probeLangServers returns the catalog's executable names found on the machine.
func probeLangServers(ctx context.Context, h machine.Host, root string) (map[string]bool, error) {
	var names []string
	for _, ls := range LangServers {
		for _, c := range ls.Candidates {
			names = append(names, machine.Quote(c[0]))
		}
	}
	names = append(names, "go")
	script := lspPath + `for c in ` + strings.Join(names, " ") + `; do command -v "$c" >/dev/null 2>&1 && printf '%s\n' "$c"; done; exit 0`
	res, err := execScriptIn(ctx, h, root, script, nil, lspProbeTimeout, 1<<16)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, n := range strings.Fields(string(res.Stdout)) {
		found[n] = true
	}
	// gopls loads every package through go. Without it the server only reports
	// that as an error for each file opened, so it counts as absent.
	if !found["go"] {
		delete(found, "gopls")
	}
	if found["typescript-language-server"] {
		res, err := execScriptIn(ctx, h, root, tsserverScript, nil, lspProbeTimeout, 1<<12)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(res.Stdout)) != "ok" {
			delete(found, "typescript-language-server")
		}
	}
	return found, nil
}

// LangServerStatus lists the catalog with what the machine has.
func LangServerStatus(ctx context.Context, h machine.Host, root string) ([]LSPStatus, error) {
	found, err := probeLangServers(ctx, h, root)
	if err != nil {
		return nil, err
	}
	out := make([]LSPStatus, 0, len(LangServers))
	for _, ls := range LangServers {
		st := LSPStatus{ID: ls.ID, Name: ls.Name, Languages: ls.Languages, Install: ls.Install, Extra: ls.Extra}
		for _, c := range ls.Candidates {
			if found[c[0]] {
				st.Available, st.Command = true, strings.Join(c, " ")
				break
			}
		}
		if st.ID == "go" && !found["go"] {
			st.Install = goToolchainInstall
		}
		if st.ID == "vue" && st.Available {
			if res, err := execScriptIn(ctx, h, root, vuePluginScript, nil, lspProbeTimeout, 1<<12); err == nil {
				st.Plugin = strings.TrimSpace(string(res.Stdout))
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// ResolveLangServer returns the script that starts server id on the machine.
func ResolveLangServer(ctx context.Context, h machine.Host, root, id string) (string, error) {
	found, err := probeLangServers(ctx, h, root)
	if err != nil {
		return "", err
	}
	for _, ls := range LangServers {
		if ls.ID != id {
			continue
		}
		for _, c := range ls.Candidates {
			if found[c[0]] {
				return lspPath + "exec " + machine.Join(c...), nil
			}
		}
	}
	return "", ErrNoLangServer
}

// LSPMaxMessage bounds one message in either direction.
const LSPMaxMessage = 64 << 20

// ReadLSPFrame reads one Content-Length framed message.
func ReadLSPFrame(r *bufio.Reader) ([]byte, error) {
	size := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			if size, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, fmt.Errorf("Content-Length tidak valid: %w", err)
			}
		}
	}
	if size < 0 || size > LSPMaxMessage {
		return nil, fmt.Errorf("ukuran pesan language server tidak valid: %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// WriteLSPFrame writes one Content-Length framed message.
func WriteLSPFrame(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
