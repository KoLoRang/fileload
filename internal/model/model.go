// Package model holds the plain data types shared by store, core and UI.
// It has no dependencies so every layer can import it.
package model

import (
	"fmt"
	"net"
	"strconv"
)

// Server is a saved SSH/SFTP server profile.
type Server struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitzero"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"` // moves to the OS keychain in M2
	KeyPath  string `json:"keyPath,omitempty"`
}

// Addr returns "host:port".
func (s Server) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// Label returns "user@host:port".
func (s Server) Label() string { return fmt.Sprintf("%s@%s", s.User, s.Addr()) }

// SameEndpoint reports whether both profiles point at the same account.
func (s Server) SameEndpoint(o Server) bool {
	return s.Host == o.Host && s.Port == o.Port && s.User == o.User
}

// MountRecord remembers one successful mount for one-click re-mount.
// Target is the local mount point: a drive letter ("Z:") on Windows,
// a directory on macOS. The JSON key stays "drive" for compatibility.
type MountRecord struct {
	Server
	Target string `json:"drive"`
	Root   string `json:"root"`
}

// SameMount reports whether both records describe the same mount.
func (m MountRecord) SameMount(o MountRecord) bool {
	return m.SameEndpoint(o.Server) && m.Target == o.Target && m.Root == o.Root
}

// Command is a local command that ssh-es to the server, cd-s into the
// mount directory and runs Command there. Empty Command = login shell.
type Command struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Desc    string `json:"desc"`
	Builtin bool   `json:"builtin,omitzero"`
}

// CloseAction is what closing the main window does.
type CloseAction string

const (
	CloseAsk  CloseAction = ""
	CloseTray CloseAction = "tray"
	CloseQuit CloseAction = "quit"
)

// Valid reports whether a is a known action.
func (a CloseAction) Valid() bool { return a == CloseAsk || a == CloseTray || a == CloseQuit }

// UISettings are window preferences.
type UISettings struct {
	CloseAction CloseAction `json:"closeAction"`
}

// UpdateSettings hold the GitHub token used to read releases of the
// private repository.
type UpdateSettings struct {
	Token string `json:"githubToken"`
}
