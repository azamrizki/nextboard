package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/studio"
)

// Code workbench endpoints (docs/API-studio.md §7.2–§7.4): the file list of "Go
// to file", search and replace in files, what git ignores, commands on save,
// language server bridge and the AI edit job.

const (
	filesTimeout = 45 * time.Second
	// aiEditMaxContext bounds the code sent with an AI edit.
	aiEditMaxContext = 256 << 10
)

// projectHost resolves {id} to the project and its connected machine.
func (s *Server) projectHost(w http.ResponseWriter, r *http.Request) (project.Project, machine.Host, bool) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return pr, nil, false
	}
	h, ok := s.machines.Host(pr.MachineID)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return pr, nil, false
	}
	if s.hubPrivate(w, h, pr.Path) {
		return pr, nil, false
	}
	return pr, h, true
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	fl, err := studio.ListFiles(ctx, h, pr.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fl)
}

func (s *Server) handleFilesSearch(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req studio.SearchOptions
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	res, err := studio.Search(ctx, h, pr.Path, req)
	if err != nil {
		filesErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func filesErr(w http.ResponseWriter, err error) {
	if errors.Is(err, studio.ErrBadPattern) {
		writeErrCode(w, http.StatusBadRequest, "bad_pattern", err.Error())
		return
	}
	fsErr(w, err)
}

func (s *Server) handleFilesReplace(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		studio.SearchOptions
		Replacement string       `json:"replacement"`
		Paths       []string     `json:"paths"`
		Confirm     *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	abs := make([]string, 0, len(req.Paths))
	for _, rel := range req.Paths {
		a, ok := studio.RelInside(pr.Path, rel)
		if !ok {
			writeErrCode(w, http.StatusForbidden, "outside_project", studio.ErrOutsideProject.Error())
			return
		}
		abs = append(abs, a)
	}
	if !s.remoteConfigConfirm(w, r, h, abs, req.Confirm) {
		return
	}
	// Longer than a search: each file is read and written on its own.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := studio.Replace(ctx, h, pr.Path, req.SearchOptions, req.Replacement, req.Paths, func(a string) error {
		out, err := studio.OutsideProject(ctx, h, pr.Path, a)
		if err != nil {
			return err
		}
		if out {
			return studio.ErrOutsideProject
		}
		return nil
	})
	if err != nil {
		filesErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleFilesIgnored(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	ignored, err := studio.Ignored(ctx, h, pr.Path, req.Paths)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ignored})
}

func (s *Server) handleFilesOnSave(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"` // relative to the project
	}
	if !decode(w, r, &req) {
		return
	}
	saved := s.settings.Get().Editor.OnSave
	rules := make([]studio.OnSaveRule, 0, len(saved))
	for _, x := range saved {
		rules = append(rules, studio.OnSaveRule{Pattern: x.Pattern, Command: x.Command})
	}
	// Every rule may use its whole 60 s; the rules stop at the first failure.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	runs, err := studio.RunOnSave(ctx, h, pr.Path, req.Path, rules)
	if err != nil {
		if errors.Is(err, studio.ErrOutsideProject) {
			writeErrCode(w, http.StatusForbidden, "outside_project", err.Error())
			return
		}
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

var aiEditSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["replacement"],"properties":{"replacement":{"type":"string"}}}`)

// aiEditPrompt asks for the replacement text of the selection (or of the insert
// point when nothing is selected).
func aiEditPrompt(path, language, instruction, before, selection, after string) string {
	var b strings.Builder
	b.WriteString("You are editing a file in a code editor. Follow the instruction and return only the new code for the marked part.\n")
	b.WriteString("Keep the file's style and indentation. Do not add explanations or code fences. You may read other project files for context, but you cannot change any file.\n")
	fmt.Fprintf(&b, "\nFile: %s\nLanguage: %s\nInstruction: %s\n", path, language, instruction)
	fmt.Fprintf(&b, "\n--- code before the marked part ---\n%s\n", before)
	if selection == "" {
		b.WriteString("--- the marked part is empty: write code to insert here ---\n")
	} else {
		fmt.Fprintf(&b, "--- the marked part, to be replaced ---\n%s\n", selection)
	}
	fmt.Fprintf(&b, "--- code after the marked part ---\n%s\n", after)
	b.WriteString("\nAnswer with JSON: {\"replacement\": \"<the new code>\"}\n")
	return b.String()
}

func (s *Server) handleCodeEdit(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, _, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Path        string `json:"path"`
		Language    string `json:"language"`
		Instruction string `json:"instruction"`
		Before      string `json:"before"`
		Selection   string `json:"selection"`
		After       string `json:"after"`
		Agent       string `json:"agent"`
		ProfileID   string `json:"profileId"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Instruction) == "" {
		writeErrCode(w, http.StatusBadRequest, "bad_instruction", "instruksi wajib diisi")
		return
	}
	if len(req.Before)+len(req.Selection)+len(req.After) > aiEditMaxContext {
		writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "kode yang dikirim terlalu besar")
		return
	}
	t, err := s.runnerFor(pr, req.Agent, req.ProfileID)
	if err != nil {
		runnerErr(w, err)
		return
	}
	j, err := s.submitRunner(jobs.RunnerRequest{Purpose: "code-edit", ProjectID: pr.ID, MachineID: pr.MachineID, Cwd: pr.Path,
		Agent: t.agent, Command: t.command, Env: t.env, Schema: aiEditSchema,
		Prompt: aiEditPrompt(req.Path, req.Language, req.Instruction, req.Before, req.Selection, req.After)})
	if err != nil {
		runnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleLSPStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	rows, err := studio.LangServerStatus(ctx, h, pr.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// handleESLint lints one file as the editor holds it.
func (s *Server) handleESLint(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
		Text string `json:"text"`
		Fix  bool   `json:"fix"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := studio.RunESLint(r.Context(), h, pr.Path, req.Path, req.Text, req.Fix)
	if errors.Is(err, studio.ErrBadLintPath) {
		writeErrCode(w, http.StatusBadRequest, "bad_path", err.Error())
		return
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleLSPSocket starts the project's language server on its machine and
// bridges it to the WebSocket: each text frame is one JSON-RPC message. The
// server lives exactly as long as the connection.
func (s *Server) handleLSPSocket(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin tidak diizinkan")
		return
	}
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	script, err := studio.ResolveLangServer(ctx, h, pr.Path, r.PathValue("server"))
	if err != nil {
		if errors.Is(err, studio.ErrNoLangServer) {
			writeErrCode(w, http.StatusNotFound, "lsp_not_found", err.Error())
			return
		}
		fsErr(w, err)
		return
	}
	proc, err := machine.StartPipe(h, machine.ExecSpec{Cwd: pr.Path, Script: script})
	if err != nil {
		fsErr(w, err)
		return
	}
	defer proc.Stop()
	s.bridgeFrames(w, r, p, "language server", proc.Stdin, proc.Stdout, proc.StderrTail)
}

// bridgeFrames upgrades to a WebSocket and carries Content-Length framed
// messages both ways, one per text frame, until either side goes away.
func (s *Server) bridgeFrames(w http.ResponseWriter, r *http.Request, p *principal, what string, in io.Writer, out io.Reader, stderr func() string) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	readerDone := make(chan struct{})
	go conn.readLoop(studio.LSPMaxMessage, func(b []byte) {
		// A dead program shows up on the other side as a closed stream.
		_ = studio.WriteLSPFrame(in, b)
	}, readerDone)

	frames := make(chan []byte, 32)
	go func() {
		defer close(frames)
		br := bufio.NewReaderSize(out, 64<<10)
		for {
			body, err := studio.ReadLSPFrame(br)
			if err != nil {
				return
			}
			select {
			case frames <- body:
			case <-readerDone:
				return
			}
		}
	}()

	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-frames:
			if !ok {
				conn.closeWith(websocket.CloseInternalServerErr, exitReason(what, stderr()))
				return
			}
			if conn.write(websocket.TextMessage, b) != nil {
				return
			}
		case <-ping.C:
			if conn.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)) != nil {
				return
			}
		case <-readerDone:
			return
		case <-conn.kill:
			conn.closeWith(wsCloseRevok, "device revoked")
			return
		}
	}
}

// exitReason fits the end of stderr into a WebSocket close reason (≤ 123 bytes).
func exitReason(what, stderr string) string {
	reason := strings.ToValidUTF8(strings.TrimSpace(stderr), "")
	if i := strings.LastIndexByte(reason, '\n'); i >= 0 {
		reason = reason[i+1:]
	}
	if reason == "" {
		return what + " exited"
	}
	if len(reason) > 120 {
		reason = strings.ToValidUTF8(reason[:120], "")
	}
	return reason
}

func (s *Server) handleDAPStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	rows, err := studio.DebugAdapterList(ctx, h, pr.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// handleDAPSocket starts a debug adapter on the project's machine and bridges
// it to the WebSocket. The adapter lives exactly as long as the connection, so
// closing the workbench ends the debug session.
func (s *Server) handleDAPSocket(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin tidak diizinkan")
		return
	}
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	ad, err := studio.ResolveDebugAdapter(ctx, h, pr.Path, r.PathValue("adapter"))
	if err != nil {
		if errors.Is(err, studio.ErrNoDebugAdapter) {
			writeErrCode(w, http.StatusNotFound, "adapter_not_found", err.Error())
			return
		}
		fsErr(w, err)
		return
	}
	proc, err := machine.StartPipe(h, machine.ExecSpec{Cwd: pr.Path, Script: ad.Script})
	if err != nil {
		fsErr(w, err)
		return
	}
	defer proc.Stop()
	if !ad.TCP {
		s.bridgeFrames(w, r, p, "debug adapter", proc.Stdin, proc.Stdout, proc.StderrTail)
		return
	}
	port, err := studio.AdapterPort(proc.Stdout)
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "adapter_failed", err.Error())
		return
	}
	conn, err := machine.DialLoopback(ctx, h, port)
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "adapter_failed", err.Error())
		return
	}
	defer conn.Close()
	s.bridgeFrames(w, r, p, "debug adapter", conn, conn, proc.StderrTail)
}

// handleCodeComplete asks the hidden runner for the code to insert at the
// cursor: the inline suggestions of the editor, only ever on request of the
// client (off by default, since each one spends the user's plan).
func (s *Server) handleCodeComplete(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, _, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Path     string `json:"path"`
		Language string `json:"language"`
		Before   string `json:"before"`
		After    string `json:"after"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Before)+len(req.After) > aiEditMaxContext {
		writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "kode yang dikirim terlalu besar")
		return
	}
	t, err := s.runnerFor(pr, "", "")
	if err != nil {
		runnerErr(w, err)
		return
	}
	model := ""
	if t.agent == "claude" {
		model = "haiku" // speed matters more here than anywhere else
	}
	j, err := s.submitRunner(jobs.RunnerRequest{Purpose: "code-complete", ProjectID: pr.ID, MachineID: pr.MachineID, Cwd: pr.Path,
		Agent: t.agent, Command: t.command, Env: t.env, Model: model, Prompt: codeCompletePrompt(req.Path, req.Language, req.Before, req.After)})
	if err != nil {
		runnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

// handleCodeAsk answers a question about the code through the hidden runner,
// which may read the project but not change it. No console is needed.
func (s *Server) handleCodeAsk(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, _, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		Question  string `json:"question"`
		Path      string `json:"path"`
		Language  string `json:"language"`
		Selection string `json:"selection"`
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeErrCode(w, http.StatusBadRequest, "bad_question", "pertanyaan wajib diisi")
		return
	}
	if len(req.Selection)+len(req.Question) > aiEditMaxContext {
		writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "kode yang dikirim terlalu besar")
		return
	}
	t, err := s.runnerFor(pr, "", "")
	if err != nil {
		runnerErr(w, err)
		return
	}
	j, err := s.submitRunner(jobs.RunnerRequest{Purpose: "code-ask", ProjectID: pr.ID, MachineID: pr.MachineID, Cwd: pr.Path,
		Agent: t.agent, Command: t.command, Env: t.env, Stream: true,
		Prompt: codeAskPrompt(req.Question, req.Path, req.Language, req.Selection, req.StartLine, req.EndLine)})
	if err != nil {
		runnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func codeAskPrompt(question, path, language, selection string, start, end int) string {
	var b strings.Builder
	b.WriteString("You answer questions from a programmer about the project in this folder, inside their code editor. You can read files but cannot change anything. Be direct and short, and use Markdown.\n")
	if path != "" {
		fmt.Fprintf(&b, "\nThe file open in the editor: %s\n", path)
	}
	if selection != "" {
		fmt.Fprintf(&b, "Selected, lines %d to %d:\n```%s\n%s\n```\n", start, end, language, selection)
	}
	fmt.Fprintf(&b, "\nQuestion: %s\n", question)
	return b.String()
}

func codeCompletePrompt(path, language, before, after string) string {
	var b strings.Builder
	b.WriteString("You are an autocomplete engine in a code editor. Continue the code at <CURSOR>.\n")
	b.WriteString("Answer with only the text to insert: no explanation, no code fences, and do not repeat the code around the cursor. Keep it short, at most a few lines. If nothing useful fits, answer with nothing.\n")
	fmt.Fprintf(&b, "\nFile: %s\nLanguage: %s\n\n%s<CURSOR>%s\n", path, language, before, after)
	return b.String()
}

func extErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, studio.ErrExtensionNotFound):
		writeErrCode(w, http.StatusNotFound, "extension_not_found", err.Error())
	case errors.Is(err, studio.ErrExtensionBadID):
		writeErrCode(w, http.StatusBadRequest, "bad_extension", err.Error())
	case errors.Is(err, studio.ErrExtensionCodeOnly):
		writeErrCode(w, http.StatusUnprocessableEntity, "extension_code_only", err.Error())
	default:
		writeErrCode(w, http.StatusBadGateway, "extension_failed", err.Error())
	}
}

func (s *Server) handleExtensions(w http.ResponseWriter, r *http.Request, p *principal) {
	items, err := s.extensions.List()
	if err != nil {
		extErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleExtensionSearch(w http.ResponseWriter, r *http.Request, p *principal) {
	res, err := s.extensions.Search(r.Context(), r.URL.Query().Get("q"), 24)
	if err != nil {
		extErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleExtensionInstall(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	x, err := s.extensions.Install(r.Context(), req.Namespace, req.Name)
	if err != nil {
		extErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, x)
}

func (s *Server) handleExtensionDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.extensions.Remove(r.PathValue("extId")); err != nil {
		extErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleExtensionFile(w http.ResponseWriter, r *http.Request, p *principal) {
	b, err := s.extensions.File(r.PathValue("extId"), r.URL.Query().Get("path"))
	if err != nil {
		extErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	_, _ = w.Write(b)
}
