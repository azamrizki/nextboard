// Package machine abstracts where agent processes run: the hub's own PC
// (local PTY) or a remote host reached over agentless SSH.
package machine

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

// ErrDisconnected is returned by Proc.Wait when the transport (SSH
// connection) dropped while the process may still be alive remotely.
var ErrDisconnected = errors.New("connection lost")

// ErrNotConnected is returned by Start when the machine has no live link.
var ErrNotConnected = errors.New("machine not connected yet")

// ErrSessionsFull: the SSH server kept refusing a new channel; its MaxSessions
// were all in use for as long as the hub waited. The link itself is fine.
var ErrSessionsFull = errors.New("the SSH server refuses new sessions (MaxSessions reached)")

// LinkLost says a call failed because of the connection, not because of what it
// asked for. A dying link answers in many ways before the hub has noticed it.
func LinkLost(err error) bool {
	return errors.Is(err, ErrDisconnected) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, sftp.ErrSSHFxNoConnection)
}

// Proc is one running terminal process, local or remote.
type Proc interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Signal(sig string) error // "HUP" | "KILL"
	Wait() (exitCode int, err error)
}

// StartSpec describes the process to launch.
type StartSpec struct {
	SessionID string
	Command   string   // "$SHELL" means the machine's login shell
	Args      []string // already chosen (args or resumeArgs)
	Cwd       string   // "" or "~" = home
	Env       []string // KEY=VALUE
	Cols      uint16
	Rows      uint16
	TmuxName  string // remote only: non-empty = run inside tmux session

	// Shell, when set, replaces Command/Args: a POSIX command line (already
	// quoted) run by the login shell, e.g. the agent wrapper
	// `exec /bin/sh -c "$NB_WRAP" nb fresh <id> 0`. Env is exported to it
	// (local: process env; remote: inline `export`).
	Shell string
	// Check is a command that must resolve on the login PATH before a
	// Shell launch (local only; CommandNotFoundError otherwise).
	Check string
}

// ProbeResult is what the hub learned about a machine.
type ProbeResult struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Shell string `json:"shell"`
	Home  string `json:"home"`
	Tmux  bool   `json:"tmux"`
	// TmuxVersion is `tmux -V` without the "tmux " prefix ("3.4").
	TmuxVersion string            `json:"tmuxVersion,omitempty"`
	Commands    map[string]string `json:"commands"`
	ProbedAt    time.Time         `json:"probedAt"`
}

// HostKeyInfo describes an SSH host key awaiting user confirmation.
type HostKeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"keyType"`
	Host        string `json:"host"`
}

// DirEntry / DirListing are returned by the folder picker. Size and Mtime
// are missing only on the read-only exec fallback of SSH machines.
type DirEntry struct {
	Name  string    `json:"name"`
	Dir   bool      `json:"dir"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime,omitzero"`
}

type DirListing struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent"`
	Entries []DirEntry `json:"entries"`
}

// Config is the persisted definition of a machine (machines.json).
type Config struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Kind          string       `json:"kind"` // "local" | "ssh"
	Host          string       `json:"host"`
	Port          int          `json:"port"`
	User          string       `json:"user"`
	SSHConfigHost string       `json:"sshConfigHost"`
	AuthMethod    string       `json:"authMethod"` // "sshconfig" | "agent" | "key" | "password"
	CredRef       string       `json:"credRef,omitempty"`
	PersistMode   string       `json:"persistMode"` // "auto" | "tmux" | "direct"
	Probe         *ProbeResult `json:"probe,omitempty"`
}

// Status values reported to clients.
const (
	StatusLocal        = "local"
	StatusConnecting   = "connecting"
	StatusConnected    = "connected"
	StatusDisconnected = "disconnected"
	StatusReconnecting = "reconnecting"
	StatusHostKey      = "hostkey"
	// StatusAuth: waiting for a password or passphrase only a person can give.
	StatusAuth  = "auth"
	StatusError = "error"
)

// Info is the API view of a machine (Config + live state, no secrets).
type Info struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Kind             string       `json:"kind"`
	Host             string       `json:"host"`
	Port             int          `json:"port"`
	User             string       `json:"user"`
	SSHConfigHost    string       `json:"sshConfigHost"`
	AuthMethod       string       `json:"authMethod"`
	PersistMode      string       `json:"persistMode"`
	EffectivePersist string       `json:"effectivePersist"`
	Status           string       `json:"status"`
	Error            string       `json:"error"`
	PendingHostKey   *HostKeyInfo `json:"pendingHostKey"`
	HostKeyMismatch  bool         `json:"hostKeyMismatch"`
	NeedsAuth        *AuthNeed    `json:"needsAuth"`
	Probe            *ProbeResult `json:"probe"`
	SessionCount     int          `json:"sessionCount"`
	// HookForward is the reverse port-forward used as the HTTP fallback
	// for agent hooks (SSH only; nil when not requested).
	HookForward *HookForward `json:"hookForward,omitempty"`
}

// HookForward reports the remote loopback port forwarded to the hub, or
// why it could not be opened.
type HookForward struct {
	Port  int    `json:"port,omitempty"`
	Error string `json:"error,omitempty"`
}

// Host is a machine the session manager can launch processes on.
type Host interface {
	ID() string
	Info() Info
	Connected() bool
	Start(spec StartSpec) (Proc, error)
	Probe(ctx context.Context, commands []string) (*ProbeResult, error)
	ListDir(ctx context.Context, path string) (*DirListing, error)

	// File API (docs/API-board.md §4). Paths are absolute or start with ~.
	Stat(ctx context.Context, path string) (*FileStat, error)
	ReadFile(ctx context.Context, path string, max int64) (*FileContent, error)
	// ReadAt returns up to n bytes of a regular file starting at off
	// (fewer at EOF), plus its stat. Used for transcript tails and
	// fs/read offset/tail.
	ReadAt(ctx context.Context, path string, off, n int64) ([]byte, *FileStat, error)
	OpenRaw(ctx context.Context, path string) (io.ReadCloser, *FileStat, error)
	WriteFile(ctx context.Context, path string, data []byte, opt WriteOptions) (*FileStat, error)
	// Mkdir creates one folder (parent must exist; an existing folder is
	// fine). Only for explicit user actions such as reply/save.
	Mkdir(ctx context.Context, path string) error
}
