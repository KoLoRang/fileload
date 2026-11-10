package core

// Phase is the lifecycle state of a mount. It marshals to the strings the
// frontend expects ("idle", "mounting", ...).
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseMounting
	PhaseMounted
	PhaseUnmounting
)

var phaseNames = [...]string{"idle", "mounting", "mounted", "unmounting"}

func (p Phase) String() string               { return phaseNames[p] }
func (p Phase) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// CmdPhase is the state of the command proxy + wrappers of a mount.
type CmdPhase int

const (
	CmdOff CmdPhase = iota
	CmdStarting
	CmdReady
	CmdError
)

var cmdPhaseNames = [...]string{"off", "starting", "ready", "error"}

func (p CmdPhase) String() string               { return cmdPhaseNames[p] }
func (p CmdPhase) MarshalText() ([]byte, error) { return []byte(p.String()), nil }
