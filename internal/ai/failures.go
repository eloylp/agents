package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	secretAssignmentRE = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:token|secret|password|passwd|apikey|api_key|authorization|credential)[a-z0-9_]*)\s*[:=]\s*("[^"]+"|'[^']+'|Bearer\s+[^\s,;]+|[^\s,;]+)`)
	bearerTokenRE      = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-]+=*`)
	longSecretRE       = regexp.MustCompile(`\b[A-Za-z0-9+/_-]{40,}={0,2}\b`)
	authFailureRE      = regexp.MustCompile(`(?i)\b(unauthorized|unauthenticated|forbidden|authorization|bearer|invalid credentials?|missing credentials?|expired credentials?|invalid api key|invalid token|expired token|refresh token|access token|sign in|log in|login required|401|403)\b`)
	errorLineRE        = regexp.MustCompile(`(?i)\b(error|failed|failure|fatal|unauthorized|forbidden)\b`)
	hexRE              = regexp.MustCompile(`(?i)^[a-f0-9]{40,}$`)
)

func runnerFailure(backend string, kind FailureKind, detail string, err error) error {
	if kind == "" {
		kind = FailureKindUnknown
	}
	return RunFailureError{
		Backend: backend,
		Kind:    kind,
		Detail:  sanitizeFailureDetail(detail),
		Err:     err,
	}
}

func commandFailureKind(cmdErr error, detail string) FailureKind {
	var interrupted CommandInterruptedError
	if errors.As(cmdErr, &interrupted) {
		switch interrupted.Kind {
		case CommandInterruptedTimeout:
			return FailureKindTimeout
		case CommandInterruptedCanceled:
			return FailureKindCanceled
		}
	}
	if looksLikeAuthFailure(detail) {
		return FailureKindBackendAuth
	}
	if strings.TrimSpace(detail) != "" {
		return FailureKindBackendError
	}
	if cmdErr != nil {
		return FailureKindRunnerError
	}
	return FailureKindUnknown
}

func backendFailureDetail(lines []timedLine, stderr string) string {
	for _, line := range lines {
		if detail := backendFailureDetailFromJSON(line.data); detail != "" {
			return detail
		}
	}
	if line := firstErrorLine(stderr); line != "" {
		return line
	}
	return firstNonEmptyLine(stderr)
}

func backendFailureDetailFromJSON(line []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return ""
	}
	if msg := stringField(obj, "message"); msg != "" && isErrorEvent(obj) {
		return msg
	}
	if errObj, ok := obj["error"].(map[string]any); ok {
		if msg := stringField(errObj, "message"); msg != "" {
			return msg
		}
		if msg := stringField(errObj, "details"); msg != "" {
			return msg
		}
	}
	if msg := stringField(obj, "stderr"); msg != "" && isErrorEvent(obj) {
		return msg
	}
	return ""
}

func isErrorEvent(obj map[string]any) bool {
	typ := strings.ToLower(stringField(obj, "type"))
	level := strings.ToLower(stringField(obj, "level"))
	return strings.Contains(typ, "error") ||
		strings.Contains(typ, "failed") ||
		level == "error"
}

func stringField(obj map[string]any, key string) string {
	v, _ := obj[key].(string)
	return strings.TrimSpace(v)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstErrorLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && errorLineRE.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}

func sanitizeFailureDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	detail = strings.ReplaceAll(detail, "\x00", "")
	detail = redactSecretAssignments(detail)
	detail = bearerTokenRE.ReplaceAllString(detail, "Bearer [REDACTED]")
	detail = longSecretRE.ReplaceAllStringFunc(detail, func(s string) string {
		if hexRE.MatchString(s) {
			return s
		}
		return "[REDACTED]"
	})
	if len(detail) > 600 {
		return fmt.Sprintf("%s...", strings.TrimSpace(detail[:600]))
	}
	return detail
}

func redactSecretAssignments(detail string) string {
	return secretAssignmentRE.ReplaceAllStringFunc(detail, func(match string) string {
		parts := secretAssignmentRE.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(parts[2])), "bearer ") {
			return parts[1] + "=Bearer [REDACTED]"
		}
		return parts[1] + "=[REDACTED]"
	})
}

func looksLikeAuthFailure(detail string) bool {
	return authFailureRE.MatchString(detail)
}
