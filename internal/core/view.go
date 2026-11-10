package core

import "fileload/internal/platform"

// Snapshot is everything the UI renders. JSON field names are the
// frontend's contract.
type Snapshot struct {
	Platform    string              `json:"platform"`
	TargetKind  platform.TargetKind `json:"targetKind"`
	Translucent bool                `json:"translucent"`
	DepsError   string              `json:"depsError"`
	// DepsInstallable: the missing driver can be installed from the app.
	DepsInstallable bool          `json:"depsInstallable"`
	Connected       bool          `json:"connected"`
	Server          string        `json:"server"` // user@host:port of the browse connection
	Servers         []ServerItem  `json:"servers"`
	Mounts          []MountItem   `json:"mounts"` // saved records
	Mount           MountView     `json:"mount"`  // active mount (M0: at most one)
	Cmd             CmdView       `json:"rcc"`
	Commands        []CommandItem `json:"commands"`
}

type ServerItem struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Host  string `json:"host"`
}

type MountItem struct {
	Index  int    `json:"index"`
	User   string `json:"user"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Root   string `json:"root"`
	Target string `json:"drive"`
}

type MountView struct {
	Phase    Phase  `json:"phase"`
	Target   string `json:"drive"`
	Root     string `json:"root"`
	Server   string `json:"server"`
	CacheDir string `json:"cacheDir"`
	Message  string `json:"message"`
}

type CmdView struct {
	Phase     CmdPhase `json:"phase"`
	Error     string   `json:"error"`
	ProxyAddr string   `json:"proxyAddr"`
	BinDir    string   `json:"binDir"`
	Target    string   `json:"drive"`
	Root      string   `json:"root"`
}

type CommandItem struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Desc    string `json:"desc"`
	Builtin bool   `json:"builtin"`
}

type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type DirListing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}
