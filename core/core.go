package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Domain types — no JSON tags, no framework types.

type Notification struct {
	ID         string
	Reason     string
	Subject    Subject
	Repository Repository
}

type Subject struct {
	Title string
	URL   string // API URL, e.g. https://api.github.com/repos/org/repo/pulls/42
	Type  string // "PullRequest", "Issue", etc.
}

type Repository struct {
	FullName string // "org/repo"
	Owner    string // "org"
}

type Reviewers struct {
	Users []string // login names
	Teams []string // team slugs
}

type Action int

const (
	ActionSkip Action = iota // not a review-requested PR
	ActionKeep               // direct review request — leave alone
	ActionMute               // team-only spam — ignore + mark read
)

func (a Action) String() string {
	switch a {
	case ActionSkip:
		return "SKIP"
	case ActionKeep:
		return "KEEP"
	case ActionMute:
		return "MUTE"
	default:
		return "UNKNOWN"
	}
}

type Decision struct {
	Notification Notification
	Action       Action
	Reason       string
}

type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

type Config struct {
	IncludeOrg string
	ExcludeOrg string
}

type Mode int

const (
	ModeRead Mode = iota
	ModeDone
)

func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(s) {
	case "", "read":
		return ModeRead, nil
	case "done":
		return ModeDone, nil
	default:
		return 0, fmt.Errorf("invalid MODE %q (valid values: read, done)", s)
	}
}

func (m Mode) ActionLabel() string {
	if m == ModeDone {
		return "DONE"
	}
	return "READ"
}

func (m Mode) ActionLabelLower() string {
	if m == ModeDone {
		return "done"
	}
	return "read"
}

// ErrAuth marks a credential the server rejected: invalid, expired, or revoked.
// Unlike a network failure, it will not succeed on a later attempt.
var ErrAuth = errors.New("token rejected: invalid, expired, or revoked")

// ErrAccess marks a credential the server accepted but that lacks rights to the
// resource, such as a missing scope or an un-authorized SSO organization.
var ErrAccess = errors.New("token lacks access: missing scope or SSO authorization")

// IsFatal reports whether an error will persist across retries, meaning a
// long-running caller should stop rather than poll again.
func IsFatal(err error) bool {
	return errors.Is(err, ErrAuth) || errors.Is(err, ErrAccess)
}

// ParseSubjectURL extracts owner, repo, and PR number from a GitHub API URL
// like "https://api.github.com/repos/org/repo/pulls/42".
func ParseSubjectURL(url string) (PRRef, error) {
	const prefix = "https://api.github.com/repos/"
	if !strings.HasPrefix(url, prefix) {
		return PRRef{}, fmt.Errorf("unexpected URL prefix: %s", url)
	}
	rest := strings.TrimPrefix(url, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[2] != "pulls" {
		return PRRef{}, fmt.Errorf("unexpected URL structure: %s", url)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil {
		return PRRef{}, fmt.Errorf("invalid PR number in URL %s: %w", url, err)
	}
	return PRRef{Owner: parts[0], Repo: parts[1], Number: number}, nil
}

// MatchesOrgFilter checks if a notification passes the org include/exclude filter.
func MatchesOrgFilter(n Notification, cfg Config) bool {
	org := n.Repository.Owner
	if cfg.IncludeOrg != "" && !strings.EqualFold(org, cfg.IncludeOrg) {
		return false
	}
	if cfg.ExcludeOrg != "" && strings.EqualFold(org, cfg.ExcludeOrg) {
		return false
	}
	return true
}

// NeedsReviewerLookup decides if a notification requires a reviewer API call.
// True when reason is "review_requested", type is "PullRequest", and it passes the org filter.
func NeedsReviewerLookup(n Notification, cfg Config) bool {
	if n.Reason != "review_requested" {
		return false
	}
	if n.Subject.Type != "PullRequest" {
		return false
	}
	return MatchesOrgFilter(n, cfg)
}

// Classify determines the action for a single notification.
// reviewers may be nil for notifications that don't need a reviewer lookup.
func Classify(n Notification, reviewers *Reviewers, login string, cfg Config) Decision {
	if !MatchesOrgFilter(n, cfg) {
		return Decision{Notification: n, Action: ActionSkip, Reason: "filtered by org"}
	}
	if n.Reason != "review_requested" || n.Subject.Type != "PullRequest" {
		return Decision{Notification: n, Action: ActionSkip, Reason: "not a review-requested PR"}
	}
	if reviewers == nil {
		return Decision{Notification: n, Action: ActionSkip, Reason: "no reviewer data"}
	}
	for _, user := range reviewers.Users {
		if strings.EqualFold(user, login) {
			return Decision{Notification: n, Action: ActionKeep, Reason: "direct review request"}
		}
	}
	return Decision{Notification: n, Action: ActionMute, Reason: "team-only review request"}
}

// ClassifyAll processes a batch of notifications.
// reviewersByURL maps subject URL to Reviewers for notifications that needed a lookup.
func ClassifyAll(notifications []Notification, reviewersByURL map[string]*Reviewers, login string, cfg Config) []Decision {
	decisions := make([]Decision, 0, len(notifications))
	for _, n := range notifications {
		reviewers := reviewersByURL[n.Subject.URL]
		decisions = append(decisions, Classify(n, reviewers, login, cfg))
	}
	return decisions
}

// CountByAction returns counts of each action type in a set of decisions.
func CountByAction(decisions []Decision) (skip, keep, mute int) {
	for _, d := range decisions {
		switch d.Action {
		case ActionSkip:
			skip++
		case ActionKeep:
			keep++
		case ActionMute:
			mute++
		}
	}
	return
}

// FormatDecisionRow formats a single decision as a line for dry-run output.
func FormatDecisionRow(d Decision) string {
	label := formatLabel(d)
	action := fmt.Sprintf("%s (%s)", d.Action, d.Reason)
	return fmt.Sprintf("%-40s  %-90s  %s", label, d.Notification.Subject.Title, action)
}

// FormatMutationRow formats a single mutation result as a line for apply output.
func FormatMutationRow(d Decision, mode Mode, err error) string {
	label := formatLabel(d)
	if err != nil {
		return fmt.Sprintf("ERROR  %s  %q  %s", label, d.Notification.Subject.Title, err)
	}
	return fmt.Sprintf("%-5s  %s  %q", mode.ActionLabel(), label, d.Notification.Subject.Title)
}

// FormatSummary renders a final summary line from counts.
func FormatSummary(scanned, actioned, kept, skipped, errors int, mode Mode) string {
	label := mode.ActionLabelLower()
	if errors > 0 {
		return fmt.Sprintf("\nDone: %d scanned, %d %s, %d errors", scanned, actioned, label, errors)
	}
	if actioned > 0 {
		return fmt.Sprintf("\nDone: %d scanned, %d %s", scanned, actioned, label)
	}
	return fmt.Sprintf("\nSummary: %d scanned, %d spam, %d kept, %d skipped", scanned, actioned, kept, skipped)
}

func formatLabel(d Decision) string {
	ref, err := ParseSubjectURL(d.Notification.Subject.URL)
	if err != nil {
		return d.Notification.Repository.FullName
	}
	return fmt.Sprintf("%s#%d", d.Notification.Repository.FullName, ref.Number)
}

// CycleStats summarizes the work done in a single pass over a set of notifications.
type CycleStats struct {
	Scanned          int
	Actioned         int
	Errors           int // mutation failures
	LookupsAttempted int
	LookupsFailed    int
	LookupsDenied    int // subset of LookupsFailed the server refused on credential grounds
	NotModified      bool
}

// ShouldStop reports whether reviewer lookups are failing in a way that polling
// again will not fix. A token that authenticates but cannot read repositories
// leaves every notification classified as "no reviewer data", so the cycle looks
// clean while nothing is ever muted. Requiring a denial distinguishes that from a
// network blip, and requiring every attempt to fail distinguishes it from a single
// inaccessible repository.
func (s CycleStats) ShouldStop() bool {
	return s.LookupsAttempted > 0 && s.LookupsFailed == s.LookupsAttempted && s.LookupsDenied > 0
}

// FormatDaemonCycleSummary renders a one-line timestamped cycle summary.
func FormatDaemonCycleSummary(now time.Time, stats CycleStats, mode Mode) string {
	ts := now.UTC().Format(time.RFC3339)
	if stats.NotModified {
		return fmt.Sprintf("%s  cycle: not modified\n", ts)
	}
	line := fmt.Sprintf("%s  cycle: %d scanned, %d %s, %d errors", ts, stats.Scanned, stats.Actioned, mode.ActionLabelLower(), stats.Errors)
	if stats.LookupsFailed > 0 {
		line += fmt.Sprintf(", %d/%d reviewer lookups failed", stats.LookupsFailed, stats.LookupsAttempted)
	}
	return line + "\n"
}
