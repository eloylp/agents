package store

// boolToInt converts a bool to 0/1 for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// intToBool converts a SQLite 0/1 to bool.
func intToBool(i int) bool { return i != 0 }

// bindingEnabledInt converts a nullable *bool flag to 0/1 for SQLite storage.
// Nil means the default (enabled); only an explicit non-nil false maps to 0.
// Used for both binding.Enabled and agent.AllowMemory, which share this
// nil-means-default-on semantics.
func bindingEnabledInt(enabled *bool) int {
	if enabled != nil && !*enabled {
		return 0
	}
	return 1
}
