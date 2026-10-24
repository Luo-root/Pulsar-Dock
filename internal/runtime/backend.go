package runtime

import "context"

// EnvironmentSpec is the minimum information a runtime needs to create an
// isolated Agent environment. The Controller owns policy decisions; a backend
// only executes the approved specification.
type EnvironmentSpec struct {
	TaskID       string
	Agent        string
	TemplateRef  string
	Workspace    WorkspaceMode
	Skills       SkillsMode
	CPUs         int
	Memory       string
	NetworkRules []NetworkRule
	GatewayURL   string
	ExpiresAfter int64
}

type WorkspaceMode string

const (
	WorkspaceNone   WorkspaceMode = "none"
	WorkspaceClone  WorkspaceMode = "clone"
	WorkspaceDirect WorkspaceMode = "direct"
)

type SkillsMode string

const (
	SkillsOff      SkillsMode = "off"
	SkillsReadonly SkillsMode = "readonly"
)

type NetworkRule struct {
	Host   string
	Port   int
	Action string
}

type CapabilitySet struct {
	MicroVM         bool
	NetworkPolicy   bool
	WorkspaceModes  []WorkspaceMode
	ProcessTreeStop bool
	CredentialProxy bool
	PortPublish     bool
	GuestExec       bool
}

type Diagnostics struct {
	Backend      string
	Ready        bool
	Capabilities CapabilitySet
	Messages     []string
}

type Environment struct {
	ID      string
	Name    string
	Backend string
	State   string
	Agent   string
	CPUs    int
	Memory  string
}

type ExecRequest struct {
	Command []string
	User    string
}

// CopyReceipt describes one regular file copied into the guest, not a host mount.
// Digest is SHA-256 of the staged bytes.
type CopyReceipt struct {
	GuestPath string
	Bytes     int64
	Digest    string
}

// CopySelector selects a guest file with an explicit byte bound. Backends without
// a verified transfer-time bound (including SbxBackend) must return unsupported.
type CopySelector struct {
	GuestPath string
	MaxBytes  int64
}

// ArtifactSnapshot is a bounded in-memory snapshot, not an applied project artifact.
// Callers own persistence, provenance, baseline checks, and explicit application.
type ArtifactSnapshot struct {
	GuestPath string
	Bytes     []byte
	Digest    string
}

type RuntimeBackend interface {
	Name() string
	Capabilities(ctx context.Context) CapabilitySet
	Check(ctx context.Context) Diagnostics
	Create(ctx context.Context, spec EnvironmentSpec) (Environment, error)
	Start(ctx context.Context, environment Environment) error
	Exec(ctx context.Context, environment Environment, request ExecRequest) (string, error)
	CopyIn(ctx context.Context, environment Environment, sourcePath, guestPath string) (CopyReceipt, error)
	CopyOut(ctx context.Context, environment Environment, selector CopySelector) (ArtifactSnapshot, error)
	Stop(ctx context.Context, environment Environment) error
	Remove(ctx context.Context, environment Environment) error
	// SetNetworkPolicy replaces the environment's managed allow set; empty denies all.
	SetNetworkPolicy(ctx context.Context, environment Environment, rules []NetworkRule) error
	Revoke(ctx context.Context, environment Environment, grant string) error
	Inspect(ctx context.Context, environment Environment) (Environment, error)
}
