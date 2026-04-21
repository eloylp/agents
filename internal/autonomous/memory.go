package autonomous

// MemoryBackend is the interface satisfied by the SQLite-backed memory
// implementation. The scheduler calls ReadMemory before each run to inject
// existing memory into the prompt, and WriteMemory after each run to persist
// the agent's returned memory.
type MemoryBackend interface {
	ReadMemory(agent, repo string) (string, error)
	WriteMemory(agent, repo, content string) error
}
