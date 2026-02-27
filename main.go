package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lmarburger/mutemath/core"
)

func main() {
	os.Exit(run())
}

func run() int {
	apply := flag.Bool("apply", false, "perform mutations (default is dry-run)")
	verbose := flag.Bool("verbose", false, "detailed output")
	daemon := flag.Bool("daemon", false, "long-running mode, polls per X-Poll-Interval")
	includeOrg := flag.String("include-org", "", "only process notifications from this org")
	excludeOrg := flag.String("exclude-org", "", "skip notifications from this org")
	flag.Parse()

	cfg := core.Config{
		IncludeOrg: *includeOrg,
		ExcludeOrg: *excludeOrg,
	}

	token, err := resolveToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 1
	}

	client := NewGitHubClient(token)

	if err := client.FetchLogin(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 1
	}

	if *verbose {
		log.Printf("authenticated as %s", client.login)
	}

	if *daemon {
		return runDaemon(client, cfg, *apply, *verbose)
	}
	return runOnce(client, cfg, *apply, *verbose)
}

func resolveToken() (string, error) {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return "", fmt.Errorf("GH_TOKEN environment variable is not set\n\nSet a GitHub Classic PAT with 'notifications' scope:\n  export GH_TOKEN=ghp_...")
	}
	return token, nil
}

func runOnce(client *GitHubClient, cfg core.Config, apply, verbose bool) int {
	result, err := client.ListUnreadNotifications("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 1
	}
	if result.NotModified || len(result.Notifications) == 0 {
		fmt.Println("No unread notifications.")
		return 0
	}

	if verbose {
		log.Printf("fetched %d unread notifications", len(result.Notifications))
	}

	if !apply {
		fmt.Println("DRY RUN — no changes will be made (use --apply to execute)")
		fmt.Println()
	}

	decisions, errCount := processNotifications(client, cfg, result.Notifications, apply, verbose)

	skip, keep, mute := core.CountByAction(decisions)
	fmt.Println(core.FormatSummary(len(decisions), mute-errCount, keep, skip, errCount))

	if errCount > 0 {
		return 1
	}
	return 0
}

func runDaemon(client *GitHubClient, cfg core.Config, apply, verbose bool) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	pollInterval := 60 * time.Second
	lastModified := ""

	log.Printf("daemon started (poll interval: %s)", pollInterval)

	for {
		result, err := client.ListUnreadNotifications(lastModified)
		now := time.Now()

		if err != nil {
			log.Printf("cycle error: %s", err)
		} else {
			if result.LastModified != "" {
				lastModified = result.LastModified
			}
			if result.PollInterval > 0 {
				pollInterval = result.PollInterval
			}

			if result.NotModified || len(result.Notifications) == 0 {
				if verbose {
					fmt.Print(core.FormatDaemonCycleSummary(now, 0, 0, 0, true))
				}
			} else {
				decisions, errCount := processNotifications(client, cfg, result.Notifications, apply, verbose)
				_, _, muted := core.CountByAction(decisions)
				fmt.Print(core.FormatDaemonCycleSummary(now, len(decisions), muted-errCount, errCount, false))
			}
		}

		select {
		case s := <-sig:
			log.Printf("received %s, shutting down", s)
			return 0
		case <-time.After(pollInterval):
			// Next cycle.
		}
	}
}

// processNotifications classifies and optionally mutates notifications one at a time,
// printing each result as it goes. Returns all decisions and the error count.
func processNotifications(client *GitHubClient, cfg core.Config, notifications []core.Notification, apply, verbose bool) ([]core.Decision, int) {
	reviewersByURL := make(map[string]*core.Reviewers)
	decisions := make([]core.Decision, 0, len(notifications))
	errCount := 0

	for _, n := range notifications {
		// Fetch reviewer data if needed (with dedup).
		if core.NeedsReviewerLookup(n, cfg) {
			if _, ok := reviewersByURL[n.Subject.URL]; !ok {
				reviewers, err := client.GetRequestedReviewers(n.Subject.URL)
				if err != nil {
					if verbose {
						log.Printf("warning: %s", err)
					}
				} else {
					reviewersByURL[n.Subject.URL] = reviewers
				}
			}
		}

		// Classify (pure).
		d := core.Classify(n, reviewersByURL[n.Subject.URL], client.login, cfg)
		decisions = append(decisions, d)

		// Print and optionally mutate.
		if apply && d.Action == core.ActionMute {
			var mutErr error
			if err := client.MarkThreadRead(d.Notification.ID); err != nil {
				mutErr = err
			} else if err := client.IgnoreThread(d.Notification.ID); err != nil {
				mutErr = err
			}
			if mutErr != nil {
				errCount++
			}
			fmt.Println(core.FormatMutationRow(d, mutErr))
		} else if !apply {
			fmt.Println(core.FormatDecisionRow(d))
		}
	}

	return decisions, errCount
}
