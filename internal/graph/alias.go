package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/akashicode/kash/internal/fsutil"
)

// AliasFileName is the entity alias file, stored next to the compiled graph.
const AliasFileName = "entity_aliases.json"

// AliasFileNote is written into the file to explain hand-editing.
const AliasFileNote = "Entity resolution map. Only clusters with \"approved\": true are applied. " +
	"Edit freely: change canonical, add/remove aliases, or flip approved — your edits survive regeneration. " +
	"Writing anything in \"note\" marks a cluster as human-reviewed, so 'resolve-entities --llm' will leave it alone. " +
	"Delete this file to disable entity resolution entirely — the agent works fine without it."

// Cluster is a group of surface forms judged to name the same entity.
type Cluster struct {
	// Key is the blocking key that grouped these forms. Stable across runs,
	// so regenerating preserves manual edits.
	Key string `json:"key"`
	// Canonical is the display form the aliases resolve to.
	Canonical string `json:"canonical"`
	// Aliases are the other surface forms folded into Canonical.
	Aliases []string `json:"aliases"`
	// Approved gates whether this cluster is applied at query time.
	Approved bool `json:"approved"`
	// Reason records why the cluster was auto-approved or held for review.
	Reason string `json:"reason,omitempty"`
	// DecidedBy records what settled this cluster: "auto" for the
	// deterministic rules, "llm" for model adjudication. A cluster already
	// decided by the model is not re-adjudicated on later runs.
	DecidedBy string `json:"decided_by,omitempty"`
	// Note is free text. A non-empty note marks the cluster as human-reviewed:
	// regeneration preserves it and --llm leaves it alone.
	Note string `json:"note,omitempty"`
}

// Settled reports whether a cluster already has a decision that should not be
// overwritten by model adjudication: approved merges, clusters the model has
// already judged, and anything a human annotated.
func (c Cluster) Settled() bool {
	return c.Approved || c.DecidedBy == "llm" || strings.TrimSpace(c.Note) != ""
}

// AliasFile is the on-disk entity resolution map.
type AliasFile struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at,omitzero"`
	Note        string    `json:"note"`
	Clusters    []Cluster `json:"clusters"`
}

// AliasSet is the query-time lookup built from an AliasFile. The zero value
// (and a nil pointer) resolve every entity to itself, so entity resolution is
// entirely optional.
type AliasSet struct {
	// canonical maps a normalized variant to its normalized canonical key.
	canonical map[string]string
}

// Len reports how many aliases are active.
func (a *AliasSet) Len() int {
	if a == nil {
		return 0
	}
	return len(a.canonical)
}

// Resolve maps an entity surface form to its canonical normalized key.
// A nil AliasSet, or an unknown entity, returns the entity's own normalized
// form — so lookups behave identically with or without an alias file.
func (a *AliasSet) Resolve(entity string) string {
	key := normalizeSurface(entity)
	if a == nil {
		return key
	}
	if c, ok := a.canonical[key]; ok {
		return c
	}
	return key
}

// NewAliasSet builds a query-time lookup from approved clusters only.
func NewAliasSet(clusters []Cluster) *AliasSet {
	a := &AliasSet{canonical: map[string]string{}}
	for _, c := range clusters {
		if !c.Approved || c.Canonical == "" {
			continue
		}
		canonKey := normalizeSurface(c.Canonical)
		if canonKey == "" {
			continue
		}
		for _, alias := range c.Aliases {
			k := normalizeSurface(alias)
			if k == "" || k == canonKey {
				continue
			}
			a.canonical[k] = canonKey
		}
	}
	return a
}

// LoadAliasFile reads an alias file. A missing file is not an error: it
// returns an empty file and an empty set, so the agent runs normally without
// entity resolution.
func LoadAliasFile(path string) (*AliasFile, *AliasSet, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &AliasFile{Version: 1, Note: AliasFileNote}, NewAliasSet(nil), nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read alias file %q: %w", path, err)
	}

	var f AliasFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, nil, fmt.Errorf("parse alias file %q: %w", path, err)
	}
	return &f, NewAliasSet(f.Clusters), nil
}

// Save writes the alias file atomically so an interrupted write cannot leave
// a corrupt map behind.
func (f *AliasFile) Save(path string) error {
	f.Version = 1
	f.Note = AliasFileNote
	f.GeneratedAt = time.Now().UTC()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal alias file: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("replace alias file: %w", err)
	}
	return nil
}
