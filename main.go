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

	mode, err := core.ParseMode(os.Getenv("MODE"))
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
		return runDaemon(client, cfg, mode, *apply, *verbose)
	}
	return runOnce(client, cfg, mode, *apply, *verbose)
}

func resolveToken() (string, error) {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return "", fmt.Errorf("GH_TOKEN environment variable is not set\n\nSet a GitHub Classic PAT with 'notifications' scope:\n  export GH_TOKEN=ghp_...")
	}
	return token, nil
}

func runOnce(client *GitHubClient, cfg core.Config, mode core.Mode, apply, verbose bool) int {
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

	decisions, stats := processNotifications(client, cfg, mode, result.Notifications, apply, verbose)

	skip, keep, mute := core.CountByAction(decisions)
	fmt.Println(core.FormatSummary(len(decisions), mute-stats.Errors, keep, skip, stats.Errors, mode))

	if stats.ShouldStop() {
		fmt.Fprintln(os.Stderr, lookupsDeniedMessage)
		return 1
	}
	if stats.Errors > 0 {
		return 1
	}
	return 0
}

func runDaemon(client *GitHubClient, cfg core.Config, mode core.Mode, apply, verbose bool) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	pollInterval := 60 * time.Second
	lastModified := ""

	log.Printf("daemon started (poll interval: %s)", pollInterval)

	for {
		result, err := client.ListUnreadNotifications(lastModified)
		now := time.Now()

		if err != nil {
			if core.IsFatal(err) {
				log.Printf("fatal: %s", err)
				return 1
			}
			log.Printf("cycle error: %s", err)
		} else {
			if result.LastModified != "" {
				lastModified = result.LastModified
			}
			if result.PollInterval > 0 {
				pollInterval = result.PollInterval
			}

			if result.NotModified {
				if verbose {
					fmt.Print(core.FormatDaemonCycleSummary(now, core.CycleStats{NotModified: true}, mode))
				}
			} else if len(result.Notifications) == 0 {
				if verbose {
					fmt.Print(core.FormatDaemonCycleSummary(now, core.CycleStats{}, mode))
				}
			} else {
				_, stats := processNotifications(client, cfg, mode, result.Notifications, apply, verbose)
				fmt.Print(core.FormatDaemonCycleSummary(now, stats, mode))
				if stats.ShouldStop() {
					log.Printf("fatal: %s", lookupsDeniedMessage)
					return 1
				}
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

const lookupsDeniedMessage = "every reviewer lookup was denied; check the token's scopes and SSO authorization"

// processNotifications classifies and optionally mutates notifications one at a time,
// printing each result as it goes. Returns all decisions and the cycle's stats.
func processNotifications(client *GitHubClient, cfg core.Config, mode core.Mode, notifications []core.Notification, apply, verbose bool) ([]core.Decision, core.CycleStats) {
	reviewersByURL := make(map[string]*core.Reviewers)
	decisions := make([]core.Decision, 0, len(notifications))
	var stats core.CycleStats

	for _, n := range notifications {
		// Fetch reviewer data if needed (with dedup).
		if core.NeedsReviewerLookup(n, cfg) {
			if _, ok := reviewersByURL[n.Subject.URL]; !ok {
				stats.LookupsAttempted++
				reviewers, err := client.GetRequestedReviewers(n.Subject.URL)
				if err != nil {
					stats.LookupsFailed++
					if core.IsFatal(err) {
						stats.LookupsDenied++
					}
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
			switch mode {
			case core.ModeDone:
				if err := client.MarkThreadDone(d.Notification.ID); err != nil {
					mutErr = err
				}
			default:
				if err := client.MarkThreadRead(d.Notification.ID); err != nil {
					mutErr = err
				}
			}
			if mutErr == nil {
				if err := client.IgnoreThread(d.Notification.ID); err != nil {
					mutErr = err
				}
			}
			if mutErr != nil {
				stats.Errors++
			}
			fmt.Println(core.FormatMutationRow(d, mode, mutErr))
		} else if !apply {
			fmt.Println(core.FormatDecisionRow(d))
		}
	}

	_, _, mute := core.CountByAction(decisions)
	stats.Scanned = len(decisions)
	stats.Actioned = mute - stats.Errors
	return decisions, stats
}
