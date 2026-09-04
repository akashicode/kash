package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EntityFacts captures an entity and the graph facts connected to it.
type EntityFacts struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Degree  int      `json:"degree"`
	Facts   []string `json:"facts"`
	// ChunkIDs are the passages the entity's facts were extracted from.
	//
	// This is the entity end of the provenance chain. A description is
	// synthesised from facts and says nothing a reader can check; these are the
	// passages that actually mention the entity, so a semantic hit on the
	// description can pull the supporting text in with it.
	ChunkIDs []string `json:"chunk_ids,omitempty"`
}

// maxChunkIDsPerEntity caps how much provenance one entity carries.
//
// A well-connected entity appears in hundreds of chunks and listing them all
// would swamp retrieval with weak signal — the point is to reach the passages
// that best support the entity, not every passage that mentions it.
const maxChunkIDsPerEntity = 8

// EntityFacts extracts unique entities and their associated facts from the graph.
// Only entities with degree >= minDegree are returned.
// When an alias map is loaded, spelling variants are merged under their canonical name.
// Results are sorted by degree descending.
func (db *DB) EntityFacts(ctx context.Context, minDegree int) []EntityFacts {
	if minDegree <= 0 {
		minDegree = 1
	}

	snap := db.index(ctx)
	if snap == nil || len(snap.byEntity) == 0 {
		return nil
	}

	db.mu.RLock()
	aliases := db.aliases
	db.mu.RUnlock()

	// Alias inverse lookup to populate Aliases slice if available
	aliasVariants := map[string][]string{}
	canonicalNames := map[string]string{}
	if aliases != nil {
		for variant, canonical := range aliases.canonical {
			if variant != canonical {
				aliasVariants[canonical] = append(aliasVariants[canonical], variant)
			}
		}
	}

	out := make([]EntityFacts, 0, len(snap.byEntity))

	for key, indices := range snap.byEntity {
		if len(indices) < minDegree {
			continue
		}

		// Choose the best display name and collect facts
		surfaceCounts := map[string]int{}
		seenFacts := map[string]bool{}
		var facts []string
		seenChunks := map[string]bool{}
		var chunkIDs []string

		for _, i := range indices {
			if i < 0 || i >= len(snap.triples) {
				continue
			}
			t := snap.triples[i]

			if t.ChunkID != "" && !seenChunks[t.ChunkID] && len(chunkIDs) < maxChunkIDsPerEntity {
				seenChunks[t.ChunkID] = true
				chunkIDs = append(chunkIDs, t.ChunkID)
			}

			subjKey := key
			objKey := key
			if aliases != nil {
				subjKey = aliases.Resolve(t.Subject)
				objKey = aliases.Resolve(t.Object)
			} else {
				subjKey = normalizeSurface(t.Subject)
				objKey = normalizeSurface(t.Object)
			}

			if subjKey == key {
				surfaceCounts[t.Subject]++
				fact := strings.TrimSpace(fmt.Sprintf("%s %s", t.Predicate, t.Object))
				if fact != "" && !seenFacts[fact] {
					seenFacts[fact] = true
					facts = append(facts, fact)
				}
			}
			if objKey == key {
				surfaceCounts[t.Object]++
				fact := strings.TrimSpace(fmt.Sprintf("%s %s", t.Subject, t.Predicate))
				if fact != "" && !seenFacts[fact] {
					seenFacts[fact] = true
					facts = append(facts, fact)
				}
			}
		}

		if len(facts) == 0 {
			continue
		}

		// Best surface name: most frequent, then longest
		displayName := canonicalNames[key]
		if displayName == "" {
			bestCount := -1
			for surface, count := range surfaceCounts {
				if count > bestCount || (count == bestCount && len(surface) > len(displayName)) {
					displayName = surface
					bestCount = count
				}
			}
		}
		if displayName == "" {
			displayName = key
		}

		// Cap facts to a reasonable limit per entity
		const maxFactsPerEntity = 20
		if len(facts) > maxFactsPerEntity {
			facts = facts[:maxFactsPerEntity]
		}

		sort.Strings(facts)

		var entAliases []string
		if vars, ok := aliasVariants[key]; ok {
			entAliases = vars
			sort.Strings(entAliases)
		}

		out = append(out, EntityFacts{
			Name:     displayName,
			Aliases:  entAliases,
			Degree:   len(indices),
			Facts:    facts,
			ChunkIDs: chunkIDs,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Degree != out[j].Degree {
			return out[i].Degree > out[j].Degree
		}
		return out[i].Name < out[j].Name
	})

	return out
}

// SearchWithSeeds runs a graph search using text query matching, explicit seed entity names,
// and/or semantically matched seed triples (e.g. from relationship vector search).
// Direct matches and seed matches are combined before one-hop traversal is run.
func (db *DB) SearchWithSeeds(ctx context.Context, query string, seedEntities []string, seedTriples []Triple, topK, maxHops int) ([]SearchResult, error) {
	if query == "" && len(seedEntities) == 0 && len(seedTriples) == 0 {
		return nil, errors.New("query, seedEntities, and seedTriples cannot all be empty")
	}
	if topK <= 0 {
		topK = 10
	}

	var seeds []SearchResult
	if query != "" {
		var err error
		seeds, err = db.Search(ctx, query, topK)
		if err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	for _, s := range seeds {
		seen[FoldKey(s.Subject, s.Predicate, s.Object)] = true
	}

	// Incorporate explicit seed triples (e.g. from semantic relationship search)
	for _, st := range seedTriples {
		fk := FoldKey(st.Subject, st.Predicate, st.Object)
		if seen[fk] {
			continue
		}
		seen[fk] = true
		seeds = append(seeds, SearchResult{
			Subject:   st.Subject,
			Predicate: st.Predicate,
			Object:    st.Object,
			ChunkID:   st.ChunkID,
			Score:     2.5,
			Hop:       0,
		})
	}

	snap := db.index(ctx)

	// Incorporate facts directly connected to seedEntities
	if len(seedEntities) > 0 && snap != nil {
		for _, seed := range seedEntities {
			k := db.entityKey(seed)
			indices := snap.byEntity[k]
			for _, i := range indices {
				if i < 0 || i >= len(snap.triples) {
					continue
				}
				t := snap.triples[i]
				fk := FoldKey(t.Subject, t.Predicate, t.Object)
				if seen[fk] {
					continue
				}
				seen[fk] = true
				t.Score = 2.0 // Direct match equivalence
				t.Hop = 0
				seeds = append(seeds, t)
			}
		}
	}

	if maxHops <= 0 || len(seeds) == 0 {
		return seeds, nil
	}

	expandFrom := seeds
	if len(expandFrom) > maxSeedsToExpand {
		expandFrom = expandFrom[:maxSeedsToExpand]
	}

	var expanded []SearchResult
	for _, seed := range expandFrom {
		for _, entity := range []string{seed.Subject, seed.Object} {
			key := db.entityKey(entity)
			neighbours := snap.byEntity[key]
			// Skip hubs: too generic for their neighbours to mean anything
			if len(neighbours) == 0 || len(neighbours) > maxHubDegree {
				continue
			}

			added := 0
			for _, i := range neighbours {
				if added >= maxPerEntity {
					break
				}
				t := snap.triples[i]
				fk := FoldKey(t.Subject, t.Predicate, t.Object)
				if seen[fk] {
					continue
				}
				seen[fk] = true

				t.Hop = 1
				t.Via = entity
				t.Score = seed.Score * hopDecay * (1 - float64(len(neighbours))/float64(maxHubDegree)*0.3)
				expanded = append(expanded, t)
				added++
			}
		}
	}

	sort.SliceStable(expanded, func(i, j int) bool {
		return expanded[i].Score > expanded[j].Score
	})

	hopBudget := topK * hopSharePct / 100
	if hopBudget == 0 && topK > 1 && len(expanded) > 0 {
		hopBudget = 1
	}
	if hopBudget > len(expanded) {
		hopBudget = len(expanded)
	}

	out := make([]SearchResult, 0, len(seeds)+hopBudget)
	out = append(out, seeds...)
	out = append(out, expanded[:hopBudget]...)
	return out, nil
}
