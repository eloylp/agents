package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

const fingerprintVersion = "v1"

func IssueFingerprint(issue Issue, maxBytes int) string {
	builder := strings.Builder{}
	builder.WriteString(issue.Title)
	builder.WriteString("\n")
	builder.WriteString(issue.Body)
	content := truncateBytes(builder.String(), maxBytes)
	contentHash := sha256Hex(content)
	return fmt.Sprintf("issue:%s:%d:%s:%s", fingerprintVersion, issue.Number, issue.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"), contentHash)
}

func PRFingerprint(pr PullRequest, role string, maxBytes int) string {
	builder := strings.Builder{}
	builder.WriteString(pr.Head.SHA)
	builder.WriteString("\n")
	builder.WriteString(pr.Title)
	builder.WriteString("\n")
	builder.WriteString(pr.Body)
	content := truncateBytes(builder.String(), maxBytes)
	contentHash := sha256Hex(content)
	return fmt.Sprintf("pr:%s:%d:%s:%s:%s", fingerprintVersion, pr.Number, role, pr.Head.SHA, contentHash)
}

func sha256Hex(value string) string {
	payload := sha256.Sum256([]byte(value))
	return hex.EncodeToString(payload[:])
}

func truncateBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	if value == "" {
		return value
	}
	used := 0
	for i := 0; i < len(value); {
		_, size := utf8.DecodeRuneInString(value[i:])
		if used+size > maxBytes {
			return value[:i]
		}
		used += size
		i += size
	}
	return value
}
