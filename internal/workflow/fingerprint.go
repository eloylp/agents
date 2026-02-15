package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/eloylp/agents/internal/github"
)

const fingerprintVersion = "v1"

func IssueFingerprint(issue github.Issue, comments []github.Comment, maxBytes int) string {
	builder := strings.Builder{}
	builder.WriteString(issue.Title)
	builder.WriteString("\n")
	builder.WriteString(issue.Body)
	for _, comment := range comments {
		builder.WriteString("\n")
		builder.WriteString(comment.Body)
	}
	content := truncateBytes(builder.String(), maxBytes)
	contentHash := sha256Hex(content)
	return fmt.Sprintf("issue:%s:%d:%s:%s", fingerprintVersion, issue.Number, issue.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"), contentHash)
}

func PRFingerprint(pr github.PullRequest, role string, files []github.PullFile, maxBytes int) string {
	builder := strings.Builder{}
	builder.WriteString(pr.Head.SHA)
	for _, file := range files {
		builder.WriteString("\n")
		builder.WriteString(file.Filename)
		builder.WriteString("|")
		builder.WriteString(file.Status)
		builder.WriteString("|")
		builder.WriteString(fmt.Sprintf("%d/%d", file.Additions, file.Deletions))
		if file.Patch != "" {
			builder.WriteString("|")
			builder.WriteString(file.Patch)
		}
	}
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
