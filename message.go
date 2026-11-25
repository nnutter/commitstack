package commitstack

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

const messageFingerprintVersion = "v1"

func messageFingerprint(commits []historyCommit) string {
	sum := sha256.Sum256([]byte("message-" + messageFingerprintVersion + "\x00" + effectiveMessage(commits)))
	return messageFingerprintVersion + ":" + hex.EncodeToString(sum[:])
}

func effectiveMessage(commits []historyCommit) string {
	for _, commit := range slices.Backward(commits) {
		switch feedbackKind(commit.subject) {
		case "fixup!":
			continue
		case "amend!", "reword!":
			return messageBody(commit.message)
		default:
			return normalizeMessage(commit.message)
		}
	}
	return normalizeMessage(commits[0].message)
}

func messageBody(message string) string {
	_, body, found := strings.Cut(message, "\n\n")
	if !found {
		return ""
	}
	return normalizeMessage(body)
}

func normalizeMessage(message string) string {
	return strings.TrimRight(message, "\n")
}

func feedbackKind(subject string) string {
	match := feedbackSubject.FindStringSubmatch(subject)
	if match == nil {
		return ""
	}
	return match[1]
}
