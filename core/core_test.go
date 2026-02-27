package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseSubjectURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    PRRef
		wantErr bool
	}{
		{
			name: "valid PR URL",
			url:  "https://api.github.com/repos/myorg/myrepo/pulls/42",
			want: PRRef{Owner: "myorg", Repo: "myrepo", Number: 42},
		},
		{
			name: "PR number 1",
			url:  "https://api.github.com/repos/owner/repo/pulls/1",
			want: PRRef{Owner: "owner", Repo: "repo", Number: 1},
		},
		{
			name: "large PR number",
			url:  "https://api.github.com/repos/org/project/pulls/99999",
			want: PRRef{Owner: "org", Repo: "project", Number: 99999},
		},
		{
			name:    "issue URL not a PR",
			url:     "https://api.github.com/repos/org/repo/issues/42",
			wantErr: true,
		},
		{
			name:    "wrong prefix",
			url:     "https://github.com/org/repo/pull/42",
			wantErr: true,
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
		{
			name:    "missing PR number",
			url:     "https://api.github.com/repos/org/repo/pulls/",
			wantErr: true,
		},
		{
			name:    "non-numeric PR number",
			url:     "https://api.github.com/repos/org/repo/pulls/abc",
			wantErr: true,
		},
		{
			name:    "extra path segments",
			url:     "https://api.github.com/repos/org/repo/pulls/42/reviews",
			wantErr: true,
		},
		{
			name:    "too few path segments",
			url:     "https://api.github.com/repos/org/pulls/42",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSubjectURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseSubjectURL(%q) = %+v, want error", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseSubjectURL(%q) error = %v", tt.url, err)
				return
			}
			if got != tt.want {
				t.Errorf("ParseSubjectURL(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
		})
	}
}

func TestMatchesOrgFilter(t *testing.T) {
	n := func(owner string) Notification {
		return Notification{Repository: Repository{Owner: owner}}
	}

	tests := []struct {
		name string
		n    Notification
		cfg  Config
		want bool
	}{
		{
			name: "no filters passes everything",
			n:    n("anyorg"),
			cfg:  Config{},
			want: true,
		},
		{
			name: "include-org matches",
			n:    n("myorg"),
			cfg:  Config{IncludeOrg: "myorg"},
			want: true,
		},
		{
			name: "include-org does not match",
			n:    n("otherorg"),
			cfg:  Config{IncludeOrg: "myorg"},
			want: false,
		},
		{
			name: "include-org case insensitive",
			n:    n("MyOrg"),
			cfg:  Config{IncludeOrg: "myorg"},
			want: true,
		},
		{
			name: "exclude-org matches",
			n:    n("spamorg"),
			cfg:  Config{ExcludeOrg: "spamorg"},
			want: false,
		},
		{
			name: "exclude-org does not match",
			n:    n("goodorg"),
			cfg:  Config{ExcludeOrg: "spamorg"},
			want: true,
		},
		{
			name: "exclude-org case insensitive",
			n:    n("SpamOrg"),
			cfg:  Config{ExcludeOrg: "spamorg"},
			want: false,
		},
		{
			name: "both filters: included and not excluded",
			n:    n("myorg"),
			cfg:  Config{IncludeOrg: "myorg", ExcludeOrg: "other"},
			want: true,
		},
		{
			name: "both filters: not included",
			n:    n("other"),
			cfg:  Config{IncludeOrg: "myorg", ExcludeOrg: "spam"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesOrgFilter(tt.n, tt.cfg)
			if got != tt.want {
				t.Errorf("MatchesOrgFilter(%q, %+v) = %v, want %v", tt.n.Repository.Owner, tt.cfg, got, tt.want)
			}
		})
	}
}

func TestNeedsReviewerLookup(t *testing.T) {
	base := Notification{
		Reason:     "review_requested",
		Subject:    Subject{Type: "PullRequest"},
		Repository: Repository{Owner: "myorg"},
	}

	tests := []struct {
		name string
		n    Notification
		cfg  Config
		want bool
	}{
		{
			name: "review_requested PR passes",
			n:    base,
			cfg:  Config{},
			want: true,
		},
		{
			name: "wrong reason",
			n: Notification{
				Reason:     "subscribed",
				Subject:    Subject{Type: "PullRequest"},
				Repository: Repository{Owner: "myorg"},
			},
			cfg:  Config{},
			want: false,
		},
		{
			name: "wrong subject type",
			n: Notification{
				Reason:     "review_requested",
				Subject:    Subject{Type: "Issue"},
				Repository: Repository{Owner: "myorg"},
			},
			cfg:  Config{},
			want: false,
		},
		{
			name: "filtered by include-org",
			n:    base,
			cfg:  Config{IncludeOrg: "otherorg"},
			want: false,
		},
		{
			name: "filtered by exclude-org",
			n: Notification{
				Reason:     "review_requested",
				Subject:    Subject{Type: "PullRequest"},
				Repository: Repository{Owner: "spamorg"},
			},
			cfg:  Config{ExcludeOrg: "spamorg"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsReviewerLookup(tt.n, tt.cfg)
			if got != tt.want {
				t.Errorf("NeedsReviewerLookup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	prNotif := Notification{
		ID:         "1",
		Reason:     "review_requested",
		Subject:    Subject{Title: "Fix bug", URL: "https://api.github.com/repos/org/repo/pulls/42", Type: "PullRequest"},
		Repository: Repository{FullName: "org/repo", Owner: "org"},
	}

	tests := []struct {
		name       string
		n          Notification
		reviewers  *Reviewers
		login      string
		cfg        Config
		wantAction Action
	}{
		{
			name:       "direct review request keeps notification",
			n:          prNotif,
			reviewers:  &Reviewers{Users: []string{"alice", "me"}, Teams: []string{"backend"}},
			login:      "me",
			wantAction: ActionKeep,
		},
		{
			name:       "direct request is case insensitive",
			n:          prNotif,
			reviewers:  &Reviewers{Users: []string{"Me"}, Teams: []string{"backend"}},
			login:      "me",
			wantAction: ActionKeep,
		},
		{
			name:       "team-only review request gets muted",
			n:          prNotif,
			reviewers:  &Reviewers{Users: []string{"alice"}, Teams: []string{"backend"}},
			login:      "me",
			wantAction: ActionMute,
		},
		{
			name:       "no users only teams gets muted",
			n:          prNotif,
			reviewers:  &Reviewers{Users: nil, Teams: []string{"backend"}},
			login:      "me",
			wantAction: ActionMute,
		},
		{
			name:       "empty reviewers gets muted",
			n:          prNotif,
			reviewers:  &Reviewers{},
			login:      "me",
			wantAction: ActionMute,
		},
		{
			name: "non-PR notification skipped",
			n: Notification{
				Reason:     "review_requested",
				Subject:    Subject{Type: "Issue"},
				Repository: Repository{Owner: "org"},
			},
			reviewers:  nil,
			login:      "me",
			wantAction: ActionSkip,
		},
		{
			name: "non-review-requested reason skipped",
			n: Notification{
				Reason:     "mention",
				Subject:    Subject{Type: "PullRequest"},
				Repository: Repository{Owner: "org"},
			},
			reviewers:  nil,
			login:      "me",
			wantAction: ActionSkip,
		},
		{
			name:       "nil reviewers with review_requested PR skipped",
			n:          prNotif,
			reviewers:  nil,
			login:      "me",
			wantAction: ActionSkip,
		},
		{
			name:       "filtered by org skipped",
			n:          prNotif,
			reviewers:  &Reviewers{Teams: []string{"backend"}},
			login:      "me",
			cfg:        Config{ExcludeOrg: "org"},
			wantAction: ActionSkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.n, tt.reviewers, tt.login, tt.cfg)
			if got.Action != tt.wantAction {
				t.Errorf("Classify() action = %v, want %v (reason: %s)", got.Action, tt.wantAction, got.Reason)
			}
		})
	}
}

func TestClassifyAll(t *testing.T) {
	notifications := []Notification{
		{
			ID:         "1",
			Reason:     "review_requested",
			Subject:    Subject{Title: "PR 1", URL: "https://api.github.com/repos/org/repo/pulls/1", Type: "PullRequest"},
			Repository: Repository{FullName: "org/repo", Owner: "org"},
		},
		{
			ID:         "2",
			Reason:     "review_requested",
			Subject:    Subject{Title: "PR 2", URL: "https://api.github.com/repos/org/repo/pulls/2", Type: "PullRequest"},
			Repository: Repository{FullName: "org/repo", Owner: "org"},
		},
		{
			ID:         "3",
			Reason:     "mention",
			Subject:    Subject{Title: "Issue 1", Type: "Issue"},
			Repository: Repository{FullName: "org/repo", Owner: "org"},
		},
	}

	reviewersByURL := map[string]*Reviewers{
		"https://api.github.com/repos/org/repo/pulls/1": {Users: []string{"me"}, Teams: []string{"backend"}},
		"https://api.github.com/repos/org/repo/pulls/2": {Users: []string{"alice"}, Teams: []string{"backend"}},
	}

	decisions := ClassifyAll(notifications, reviewersByURL, "me", Config{})

	if len(decisions) != 3 {
		t.Fatalf("got %d decisions, want 3", len(decisions))
	}
	if decisions[0].Action != ActionKeep {
		t.Errorf("decision[0] = %v, want KEEP", decisions[0].Action)
	}
	if decisions[1].Action != ActionMute {
		t.Errorf("decision[1] = %v, want MUTE", decisions[1].Action)
	}
	if decisions[2].Action != ActionSkip {
		t.Errorf("decision[2] = %v, want SKIP", decisions[2].Action)
	}
}

func TestCountByAction(t *testing.T) {
	decisions := []Decision{
		{Action: ActionSkip},
		{Action: ActionKeep},
		{Action: ActionMute},
		{Action: ActionMute},
		{Action: ActionSkip},
	}
	skip, keep, mute := CountByAction(decisions)
	if skip != 2 || keep != 1 || mute != 2 {
		t.Errorf("CountByAction() = (%d, %d, %d), want (2, 1, 2)", skip, keep, mute)
	}
}

func TestFormatDecisionRow(t *testing.T) {
	d := Decision{
		Notification: Notification{
			Subject:    Subject{Title: "Fix bug", URL: "https://api.github.com/repos/org/repo/pulls/42", Type: "PullRequest"},
			Repository: Repository{FullName: "org/repo"},
		},
		Action: ActionMute,
		Reason: "team-only review request",
	}

	output := FormatDecisionRow(d)
	for _, want := range []string{"org/repo#42", "Fix bug", "MUTE", "team-only review request"} {
		if !strings.Contains(output, want) {
			t.Errorf("FormatDecisionRow missing %q\nGot: %s", want, output)
		}
	}
}

func TestFormatMutationRow(t *testing.T) {
	d := Decision{
		Notification: Notification{
			ID:         "1",
			Subject:    Subject{Title: "Fix bug", URL: "https://api.github.com/repos/org/repo/pulls/42"},
			Repository: Repository{FullName: "org/repo"},
		},
		Action: ActionMute,
	}

	t.Run("success", func(t *testing.T) {
		output := FormatMutationRow(d, nil)
		if !strings.Contains(output, "MUTED") || !strings.Contains(output, "org/repo#42") {
			t.Errorf("unexpected output: %s", output)
		}
	})

	t.Run("error", func(t *testing.T) {
		output := FormatMutationRow(d, fmt.Errorf("mark-read failed: 500"))
		if !strings.Contains(output, "ERROR") || !strings.Contains(output, "mark-read failed") {
			t.Errorf("unexpected output: %s", output)
		}
	})
}

func TestFormatSummary(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		output := FormatSummary(10, 0, 3, 7, 0)
		if !strings.Contains(output, "10 scanned") || !strings.Contains(output, "3 kept") || !strings.Contains(output, "7 skipped") {
			t.Errorf("unexpected output: %s", output)
		}
	})

	t.Run("apply with errors", func(t *testing.T) {
		output := FormatSummary(10, 8, 2, 0, 1)
		if !strings.Contains(output, "10 scanned") || !strings.Contains(output, "8 muted") || !strings.Contains(output, "1 errors") {
			t.Errorf("unexpected output: %s", output)
		}
	})

	t.Run("apply no errors", func(t *testing.T) {
		output := FormatSummary(10, 5, 3, 2, 0)
		if !strings.Contains(output, "10 scanned") || !strings.Contains(output, "5 muted") {
			t.Errorf("unexpected output: %s", output)
		}
		if strings.Contains(output, "errors") {
			t.Errorf("should not mention errors when there are none: %s", output)
		}
	})
}

func TestFormatDaemonCycleSummary(t *testing.T) {
	now := time.Date(2026, 2, 27, 10, 0, 0, 0, time.UTC)

	t.Run("not modified", func(t *testing.T) {
		output := FormatDaemonCycleSummary(now, 0, 0, 0, true)
		want := "2026-02-27T10:00:00Z  cycle: not modified\n"
		if output != want {
			t.Errorf("got %q, want %q", output, want)
		}
	})

	t.Run("with results", func(t *testing.T) {
		output := FormatDaemonCycleSummary(now, 3, 2, 0, false)
		want := "2026-02-27T10:00:00Z  cycle: 3 scanned, 2 muted, 0 errors\n"
		if output != want {
			t.Errorf("got %q, want %q", output, want)
		}
	})
}

