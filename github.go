package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lmarburger/mutemath/core"
)

// Internal JSON types — these never leave this file.

type ghNotification struct {
	ID         string       `json:"id"`
	Reason     string       `json:"reason"`
	Subject    ghSubject    `json:"subject"`
	Repository ghRepository `json:"repository"`
}

type ghSubject struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

type ghRepository struct {
	FullName string  `json:"full_name"`
	Owner    ghOwner `json:"owner"`
}

type ghOwner struct {
	Login string `json:"login"`
}

type ghReviewersResponse struct {
	Users []ghUser `json:"users"`
	Teams []ghTeam `json:"teams"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghTeam struct {
	Slug string `json:"slug"`
}

type ghAuthenticatedUser struct {
	Login string `json:"login"`
}

// GitHubClient handles all GitHub API I/O.
type GitHubClient struct {
	token      string
	httpClient *http.Client
	login      string
}

func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *GitHubClient) setStandardHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// do executes an HTTP request with standard GitHub headers.
// Retries once on 429 or 403 with Retry-After.
func (c *GitHubClient) do(method, url string, body io.Reader) (*http.Response, error) {
	for attempt := range 2 {
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		c.setStandardHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if attempt == 0 && isRateLimited(resp) {
			wait := parseRetryAfter(resp)
			resp.Body.Close()
			time.Sleep(wait)
			continue
		}

		return resp, nil
	}
	// Unreachable, but the compiler needs it.
	return nil, fmt.Errorf("exhausted retries")
}

func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != ""
}

func parseRetryAfter(resp *http.Response) time.Duration {
	s := resp.Header.Get("Retry-After")
	if s == "" {
		return 60 * time.Second
	}
	secs, err := strconv.Atoi(s)
	if err != nil {
		return 60 * time.Second
	}
	return time.Duration(secs) * time.Second
}

// FetchLogin calls GET /user and stores the authenticated user's login.
func (c *GitHubClient) FetchLogin() error {
	resp, err := c.do("GET", "https://api.github.com/user", nil)
	if err != nil {
		return fmt.Errorf("fetch login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch login: unexpected status %d", resp.StatusCode)
	}

	var user ghAuthenticatedUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return fmt.Errorf("fetch login: %w", err)
	}
	c.login = user.Login
	return nil
}

// NotificationsResult holds the result of a ListUnreadNotifications call.
type NotificationsResult struct {
	Notifications []core.Notification
	NotModified   bool
	LastModified  string        // for conditional requests on the next poll
	PollInterval  time.Duration // server-recommended poll interval
}

// ListUnreadNotifications fetches all unread notifications, handling pagination.
// Stops when a page returns an empty array. Captures Last-Modified and X-Poll-Interval
// from response headers and returns them in the result.
// If lastModified is non-empty, sends If-Modified-Since on the first page.
// Returns NotModified=true on 304 responses.
func (c *GitHubClient) ListUnreadNotifications(lastModified string) (*NotificationsResult, error) {
	var all []core.Notification
	result := &NotificationsResult{}

	for page := 1; ; page++ {
		url := fmt.Sprintf("https://api.github.com/notifications?per_page=50&page=%d", page)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("list notifications: %w", err)
		}
		c.setStandardHeaders(req)

		if lastModified != "" && page == 1 {
			req.Header.Set("If-Modified-Since", lastModified)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list notifications page %d: %w", page, err)
		}

		// Handle rate limiting inline for this special case
		if isRateLimited(resp) {
			wait := parseRetryAfter(resp)
			resp.Body.Close()
			time.Sleep(wait)

			resp, err = c.httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("list notifications page %d (retry): %w", page, err)
			}
		}

		// Capture polling metadata from first page
		if page == 1 {
			if lm := resp.Header.Get("Last-Modified"); lm != "" {
				result.LastModified = lm
			}
			if pi := resp.Header.Get("X-Poll-Interval"); pi != "" {
				if secs, err := strconv.Atoi(pi); err == nil {
					result.PollInterval = time.Duration(secs) * time.Second
				}
			}
		}

		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			result.NotModified = true
			return result, nil
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("list notifications page %d: unexpected status %d", page, resp.StatusCode)
		}

		var ghNotifs []ghNotification
		if err := json.NewDecoder(resp.Body).Decode(&ghNotifs); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("list notifications page %d: %w", page, err)
		}
		resp.Body.Close()

		if len(ghNotifs) == 0 {
			break
		}

		for _, gn := range ghNotifs {
			all = append(all, toNotification(gn))
		}
	}

	result.Notifications = all
	return result, nil
}

// GetRequestedReviewers fetches reviewers for a PR given its API subject URL.
func (c *GitHubClient) GetRequestedReviewers(subjectURL string) (*core.Reviewers, error) {
	ref, err := core.ParseSubjectURL(subjectURL)
	if err != nil {
		return nil, fmt.Errorf("get reviewers: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/requested_reviewers", ref.Owner, ref.Repo, ref.Number)
	resp, err := c.do("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("get reviewers for %s/%s#%d: %w", ref.Owner, ref.Repo, ref.Number, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get reviewers for %s/%s#%d: unexpected status %d", ref.Owner, ref.Repo, ref.Number, resp.StatusCode)
	}

	var ghReviewers ghReviewersResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghReviewers); err != nil {
		return nil, fmt.Errorf("get reviewers for %s/%s#%d: %w", ref.Owner, ref.Repo, ref.Number, err)
	}

	return toReviewers(ghReviewers), nil
}

// MarkThreadRead marks a notification thread as read.
func (c *GitHubClient) MarkThreadRead(threadID string) error {
	url := fmt.Sprintf("https://api.github.com/notifications/threads/%s", threadID)
	resp, err := c.do("PATCH", url, nil)
	if err != nil {
		return fmt.Errorf("mark thread %s read: %w", threadID, err)
	}
	defer resp.Body.Close()

	// 205 Reset Content is the expected success response.
	if resp.StatusCode != http.StatusResetContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mark thread %s read: unexpected status %d", threadID, resp.StatusCode)
	}
	return nil
}

// IgnoreThread mutes/ignores a notification thread.
func (c *GitHubClient) IgnoreThread(threadID string) error {
	url := fmt.Sprintf("https://api.github.com/notifications/threads/%s/subscription", threadID)
	body := strings.NewReader(`{"ignored":true}`)
	resp, err := c.do("PUT", url, body)
	if err != nil {
		return fmt.Errorf("ignore thread %s: %w", threadID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("ignore thread %s: unexpected status %d", threadID, resp.StatusCode)
	}
	return nil
}

// Conversion functions: GitHub JSON types → core types.

func toNotification(gn ghNotification) core.Notification {
	return core.Notification{
		ID:     gn.ID,
		Reason: gn.Reason,
		Subject: core.Subject{
			Title: gn.Subject.Title,
			URL:   gn.Subject.URL,
			Type:  gn.Subject.Type,
		},
		Repository: core.Repository{
			FullName: gn.Repository.FullName,
			Owner:    gn.Repository.Owner.Login,
		},
	}
}

func toReviewers(gr ghReviewersResponse) *core.Reviewers {
	users := make([]string, len(gr.Users))
	for i, u := range gr.Users {
		users[i] = u.Login
	}
	teams := make([]string, len(gr.Teams))
	for i, t := range gr.Teams {
		teams[i] = t.Slug
	}
	return &core.Reviewers{Users: users, Teams: teams}
}
