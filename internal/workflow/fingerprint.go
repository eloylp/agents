package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

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

func PRFingerprint(pr github.PullRequest, files []github.PullFile, maxBytes int) string {
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
	return fmt.Sprintf("pr:%s:%d:%s:%s", fingerprintVersion, pr.Number, pr.Head.SHA, contentHash)
}

func sha256Hex(value string) string {
	payload := sha256.Sum256([]byte(value))
	return hex.EncodeToString(payload[:])
}

func truncateBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	truncated := string(runes)
	if len(truncated) <= maxBytes {
		return truncated
	}
	return truncated[:maxBytes]
}
