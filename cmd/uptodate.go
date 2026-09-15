package cmd

// upToDate reports whether a build has nothing left to do.
//
// Every document being built is not enough. The lexical index, entity
// descriptions and MCP tool description are produced after the per-document
// loop, so a build stopped between the two left all three missing — and when
// this check looked only at documents, every later run stopped here and never
// produced them. needsFinalize carries that unfinished state.
func upToDate(pending, removed int, prune, needsFinalize bool) bool {
	if needsFinalize {
		return false
	}
	return pending == 0 && (removed == 0 || !prune)
}
