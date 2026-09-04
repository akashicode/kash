// Package profile holds the corpus profile: the domain configuration kash
// derives from the documents themselves rather than asking a human to write.
//
// Extraction predicates, entity-resolution rules and structural reference
// patterns all depend on what a corpus actually contains, so nobody can write
// them before they know their corpus. The profile is measured at build time and
// sits between the built-in defaults and agent.yaml, which remains the override
// layer.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/fsutil"
)

// FileName is the profile, stored next to the compiled databases.
const FileName = "domain.profile.json"

// FormatVersion is bumped when the profile schema changes incompatibly.
const FormatVersion = 1

// Note is written into the file to explain that it is machine-owned.
//
// The file is regenerated wholesale, so unlike the alias map it cannot promise
// that hand edits survive. Saying so plainly, in the file, is the only way a
// reader finds that out before losing work.
const Note = "Derived from your corpus by 'kash build'. DO NOT EDIT — " +
	"'kash build --refresh-profile' rewrites this file wholesale. " +
	"To change any value, set it in agent.yaml: agent.yaml wins over this file, " +
	"and this file wins over the built-in defaults. Setting a list in agent.yaml " +
	"REPLACES the list here, it does not merge with it. " +
	"Delete this file to fall back to generic defaults — the agent still works, " +
	"just tuned for no corpus in particular."

// Provenance values for Signal.DecidedBy.
const (
	// DecidedDefault means no evidence was strong enough; the built-in default stands.
	DecidedDefault = "default"
	// DecidedDetected means a deterministic measurement over the corpus settled it.
	DecidedDetected = "detected"
	// DecidedLLM means the model chose the value.
	DecidedLLM = "llm"
	// DecidedLLMNamed means detection found the thing and the model only named it.
	DecidedLLMNamed = "llm-named"
)

// Signal records how one field was decided and on what evidence.
//
// Provenance is a sibling array rather than inline in Config so that Config
// deserialises straight into config.DomainOverlay and stays copy-pasteable into
// agent.yaml verbatim.
type Signal struct {
	// Field is the dotted config path, e.g. "resolution.fold_diacritics".
	Field string `json:"field"`
	// Value is a short rendering of what was chosen.
	Value string `json:"value"`
	// DecidedBy is one of the Decided* constants.
	DecidedBy string `json:"decided_by"`
	// Evidence is the measurement behind the decision, in human terms. This is
	// what makes a wrong profile debuggable instead of magic.
	Evidence string `json:"evidence,omitempty"`
}

// CorpusFingerprint identifies what a profile was derived from, so a build can
// report that the corpus has moved on without re-deriving to find out.
type CorpusFingerprint struct {
	Documents int    `json:"documents"`
	Bytes     int64  `json:"bytes"`
	Hash      string `json:"hash"`
}

// Profile is the generated domain configuration for one corpus.
type Profile struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at,omitzero"`
	Note        string    `json:"note"`

	// KashVersion records the binary that derived this. Chunk metadata and the
	// profile schema both change across releases, so knowing the builder is
	// what lets a later build or serve report an incompatibility instead of
	// behaving oddly.
	KashVersion string `json:"kash_version,omitempty"`

	// Complete is false when the LLM leg failed. The next build retries it
	// without redoing detection.
	//
	// The distinction matters: a profile marked complete with default
	// predicates would extract an entire corpus with generic vocabulary and
	// never revisit it — reproducing the exact failure this package exists to
	// fix.
	Complete bool `json:"complete"`
	// LLMStatus explains a Complete=false profile.
	LLMStatus string `json:"llm_status,omitempty"`

	Corpus CorpusFingerprint `json:"corpus"`

	// Config is the derived configuration, in agent.yaml's own shape.
	Config config.DomainOverlay `json:"config"`

	// MCPToolName and MCPToolDescription are the generated MCP tool identity.
	//
	// These live here rather than in agent.yaml because they are machine
	// generated. Writing them back into agent.yaml meant round-tripping it
	// through a map on every build, which deleted every comment in the file —
	// including the only documentation for several options — reordered its
	// keys, and rewrote it non-atomically. agent.yaml overrides these like any
	// other field.
	MCPToolName        string `json:"mcp_tool_name,omitempty"`
	MCPToolDescription string `json:"mcp_tool_description,omitempty"`

	// Signals is per-field provenance, ordered by field name.
	Signals []Signal `json:"signals,omitempty"`
}

// New returns an empty profile stamped with the current format version.
func New() *Profile {
	return &Profile{Version: FormatVersion, Note: Note}
}

// Overlay returns the config layer this profile contributes, or nil when the
// profile is absent — which is what ResolveDomainConfig expects for "no
// profile layer".
func (p *Profile) Overlay() *config.DomainOverlay {
	if p == nil {
		return nil
	}
	return &p.Config
}

// AddSignal records how a field was decided.
func (p *Profile) AddSignal(field, value, decidedBy, evidence string) {
	p.Signals = append(p.Signals, Signal{
		Field: field, Value: value, DecidedBy: decidedBy, Evidence: evidence,
	})
}

// SignalFor returns the signal recorded for a field, if any.
func (p *Profile) SignalFor(field string) (Signal, bool) {
	if p == nil {
		return Signal{}, false
	}
	for _, s := range p.Signals {
		if s.Field == field {
			return s, true
		}
	}
	return Signal{}, false
}

// Fingerprint summarises a corpus so a later build can tell whether it moved.
// Names are sorted so the hash does not depend on directory walk order.
func Fingerprint(names []string, sizes []int64) CorpusFingerprint {
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)

	h := sha256.New()
	var total int64
	for _, n := range ordered {
		fmt.Fprintf(h, "%s\n", n)
	}
	for _, s := range sizes {
		total += s
	}
	fmt.Fprintf(h, "bytes:%d", total)

	return CorpusFingerprint{
		Documents: len(names),
		Bytes:     total,
		Hash:      hex.EncodeToString(h.Sum(nil))[:16],
	}
}

// Load reads a profile. A missing file returns (nil, nil): no profile is a
// valid state, and the agent falls back to generic defaults.
//
// A corrupt file is also non-fatal at serve time, matching how a malformed
// alias file is handled — a configuration artifact must never take the server
// down. Callers that care can inspect the returned error.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profile %q: %w", path, err)
	}

	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile %q: %w", path, err)
	}
	if p.Version > FormatVersion {
		return nil, fmt.Errorf("profile %q has format version %d, this kash understands %d — upgrade kash or delete the file",
			path, p.Version, FormatVersion)
	}
	return &p, nil
}

// Save writes the profile atomically, so an interrupted build never leaves a
// half-written profile that a later run would read as authoritative.
func (p *Profile) Save(path string) error {
	p.Version = FormatVersion
	p.Note = Note
	p.GeneratedAt = time.Now().UTC()
	sort.SliceStable(p.Signals, func(i, j int) bool { return p.Signals[i].Field < p.Signals[j].Field })

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("replace profile: %w", err)
	}
	return nil
}

// shortHash returns a stable short digest, used for the drift signatures.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}
