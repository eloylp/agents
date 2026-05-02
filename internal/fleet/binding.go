package fleet

import "strings"

// Binding wires an agent to one or more triggers on a specific repo.
// An agent can appear multiple times in a repo's Use list with different
// triggers.
//
// ID is the SQLite AUTOINCREMENT primary key of the binding row. It is not
// present in YAML (zero for entries loaded from a YAML file) and is populated
// by the store when loaded. Atomic per-binding CRUD endpoints address a row
// by its ID so UI edits can target one binding without replacing the whole
// repo.
type Binding struct {
	ID      int64    `yaml:"-" json:"id,omitempty"`
	Agent   string   `yaml:"agent"`
	Labels  []string `yaml:"labels"`
	Cron    string   `yaml:"cron"`
	Events  []string `yaml:"events"`
	Enabled *bool    `yaml:"enabled"`
}

// IsEnabled reports whether this binding should be active. Absent =
// enabled; only explicit `enabled: false` disables.
func (b Binding) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}

// IsCron reports whether this binding is cron-triggered.
func (b Binding) IsCron() bool { return strings.TrimSpace(b.Cron) != "" }

// IsLabel reports whether this binding is label-triggered.
func (b Binding) IsLabel() bool { return len(b.Labels) > 0 }

// IsEvent reports whether this binding is event-triggered (via the events: field).
func (b Binding) IsEvent() bool { return len(b.Events) > 0 }

// TriggerCount returns the number of trigger types (cron, labels, events) set
// on this binding. Bindings must use exactly one trigger; callers use
// TriggerCount to enforce that invariant without repeating the three IsX checks.
func (b Binding) TriggerCount() int {
	n := 0
	if b.IsLabel() {
		n++
	}
	if b.IsEvent() {
		n++
	}
	if b.IsCron() {
		n++
	}
	return n
}
