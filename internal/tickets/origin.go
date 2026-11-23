package tickets

import (
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The places a job can come from, which the API and the UI call its source.
// A tracker's jobs take the tracker's name, such as linear, so a new tracker
// needs no new constant here.
const (
	// OriginWeb is the New job form in the web app.
	OriginWeb = "web"
	// OriginAPI is the run API or the ticket API, called with a token.
	OriginAPI = "api"
	// OriginSchedule is an automation, on its schedule or run by hand.
	OriginSchedule = "schedule"
)

// webKeyPrefix starts the run API key of every job the New job form sends.
const webKeyPrefix = "run:ui-"

// trackerLink is the line a tracker's prompt ends with, which names the issue
// and links to it. See trackerPrompt in internal/sources.
var trackerLink = regexp.MustCompile(`(?:^|\n)Issue [^\n]*: (\S+)\s*$`)

// originOf works out where a job came from out of what Machinist kept before
// it recorded the source: the ticket's intake and its request's key.
func originOf(intake, requestKey string) string {
	switch {
	case strings.HasPrefix(intake, sourceSchedulePrefix):
		return OriginSchedule
	case intake == "run" && strings.HasPrefix(requestKey, webKeyPrefix):
		return OriginWeb
	case intake == "run", intake == "api", intake == "":
		return OriginAPI
	default:
		return intake
	}
}

// sourceLinkOf works out the link to a job's original issue out of what
// Machinist kept before it recorded the link: a tracker's job names the issue
// at the end of its prompt, and a job for a GitHub issue has its number.
func sourceLinkOf(origin, repo string, issue int, prompt string) string {
	switch origin {
	case OriginWeb, OriginSchedule:
		return ""
	case OriginAPI:
		if issue > 0 {
			return GitHubIssueURL(repo, issue)
		}
		return ""
	}
	if match := trackerLink.FindStringSubmatch(prompt); match != nil {
		return SourceURL(match[1])
	}
	return ""
}

// GitHubIssueURL links to an issue on GitHub.
func GitHubIssueURL(repo string, issue int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue)
}

// SourceURL returns link if it is an absolute http or https URL, and empty
// otherwise, so a link the UI shows can never run script.
func SourceURL(link string) string {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

// backfillOrigins records the source and original link of every ticket from
// before Machinist recorded them, out of the ticket's own data.
func backfillOrigins(db *sql.DB) error {
	rows, err := db.Query(`SELECT t.id,t.source,t.repo,COALESCE(t.issue_number,0),COALESCE(e.source_key,''),COALESCE(e.body,'')
  FROM tickets t LEFT JOIN ticket_entries e ON e.id=(SELECT MIN(id) FROM ticket_entries WHERE ticket_id=t.id AND kind='request')
  WHERE t.origin=''`)
	if err != nil {
		return err
	}
	type update struct{ id, origin, link string }
	var updates []update
	for rows.Next() {
		var id, intake, repo, key, prompt string
		var issue int
		if err := rows.Scan(&id, &intake, &repo, &issue, &key, &prompt); err != nil {
			rows.Close()
			return err
		}
		origin := originOf(intake, key)
		updates = append(updates, update{id, origin, sourceLinkOf(origin, repo, issue, prompt)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, found := range updates {
		if _, err := tx.Exec(`UPDATE tickets SET origin=?,source_url=? WHERE id=?`, found.origin, found.link, found.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
