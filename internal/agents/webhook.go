package agents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Payload is the subset of a Forgejo webhook delivery we need. Field shape verified against a real
// Forgejo 11 issue_comment + pull_request delivery (see the bootstrap spike): PR comments arrive as
// issue_comment with is_pull=true; a pull_request event carries the head ref directly.
type Payload struct {
	Action  string `json:"action"`  // issue_comment: created/edited/deleted; pull_request: opened/synchronize/…
	IsPull  bool   `json:"is_pull"` // true when an issue_comment is on a PR
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Issue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"issue"`
	PullRequest *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// ParsePayload decodes a delivery body, failing loud on malformed JSON (never a silent zero value).
func ParsePayload(body []byte) (*Payload, error) {
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode webhook payload: %w", err)
	}
	if p.Repository.Name == "" || p.Repository.Owner.Login == "" {
		return nil, fmt.Errorf("webhook payload missing repository owner/name (event not understood)")
	}
	return &p, nil
}

// VerifySignature checks the HMAC-SHA256 of the raw body against the delivery signature header.
// Forgejo sends the hex digest in X-Forgejo-Signature (also X-Hub-Signature-256: "sha256=<hex>").
// Accepts either form. A constant-time compare; empty secret means verification is disabled.
func VerifySignature(secret string, body []byte, sigHeader string) error {
	if secret == "" {
		return nil
	}
	got := strings.TrimSpace(sigHeader)
	got = strings.TrimPrefix(got, "sha256=")
	if got == "" {
		return fmt.Errorf("missing webhook signature (expected HMAC-SHA256; is the hook secret set?)")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(got)), []byte(want)) {
		return fmt.Errorf("webhook signature mismatch (HMAC-SHA256 of body != header) — wrong secret or tampered payload")
	}
	return nil
}

// mentionRe matches an @handle mention. Handles are alphanumeric plus -_ and must start with an
// alphanumeric; the leading char must not be part of an email/path (require start or whitespace/punct).
var mentionRe = regexp.MustCompile(`(^|[^\w@/])@([A-Za-z0-9][\w-]*)`)

// Mentions returns the distinct handles mentioned in a comment body (lowercased, order-preserved).
func Mentions(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		h := strings.ToLower(m[2])
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
