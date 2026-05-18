package fleet

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// CronParser is the five-field cron parser used by the scheduler and config
// mutation validation.
var CronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ValidateBindingShape checks entity-local binding invariants.
func ValidateBindingShape(b Binding) error {
	if strings.TrimSpace(b.Agent) == "" {
		return fmt.Errorf("agent is required")
	}
	n := b.TriggerCount()
	if n == 0 {
		return fmt.Errorf("binding has no trigger (set cron, labels, or events)")
	}
	if n > 1 {
		return fmt.Errorf("binding mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger")
	}
	if b.IsCron() {
		if _, err := CronParser.Parse(b.Cron); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", b.Cron, err)
		}
	}
	return nil
}

// ValidateRepoCronExpressions checks cron bindings with the scheduler parser.
func ValidateRepoCronExpressions(repos []Repo) error {
	for _, r := range repos {
		for _, b := range r.Use {
			if !b.IsCron() {
				continue
			}
			if _, err := CronParser.Parse(b.Cron); err != nil {
				return fmt.Errorf("invalid cron expression %q for repo %q: %w", b.Cron, r.Name, err)
			}
		}
	}
	return nil
}
