package graphql

// ForgetSchemas drops the schemas fetched by introspection that s holds
// in memory, and their search indexes, when the cached schemas are cleared
// (docs/privacy.md#clearing-local-data): they are read from disk
// again on next use, and the cleared ones are gone there. Schemas read
// from the project's schema files are not cached on disk and are kept.
//
// It is a function, not a method, so it is not bound to the UI.
func ForgetSchemas(s *GraphQLService) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, e := range s.memory {
		if e != nil && e.Schema != nil {
			delete(s.indexes, e.Schema)
		}
		delete(s.memory, key)
	}
}
