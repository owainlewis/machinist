package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	defaultNotifyTimeout = 10 * time.Second
	maxNotifyInFlight    = 16
)

// Notifier tells someone when a job needs a person: when its pull request waits
// for a person or the job fails. Implementations must not block the caller or
// fail the ticket.
type Notifier interface {
	Notify(ticket tickets.Ticket)
}

// Notification is the JSON message posted to notify.webhook when a job needs a
// person. Only job metadata is included; question text, prompts and agent
// output are never sent.
type Notification struct {
	Text      string `json:"text"`
	TicketID  string `json:"ticket_id"`
	Title     string `json:"title"`
	Repo      string `json:"repo"`
	State     string `json:"state"`
	CreatedBy string `json:"created_by"`
	URL       string `json:"url,omitempty"`
}

// BuildNotification turns a ticket into the JSON payload sent to a webhook.
// Without publicURL, the link is omitted from both the url field and text.
func BuildNotification(ticket tickets.Ticket, publicURL string) Notification {
	ticketURL := ""
	if base := strings.TrimRight(strings.TrimSpace(publicURL), "/"); base != "" {
		ticketURL = base + "/#/jobs/" + url.PathEscape(ticket.ID)
	}
	return Notification{
		Text:      notificationText(ticket, ticketURL),
		TicketID:  ticket.ID,
		Title:     ticket.Title,
		Repo:      ticket.Repo,
		State:     ticket.State,
		CreatedBy: ticket.CreatedBy,
		URL:       ticketURL,
	}
}

func notificationText(ticket tickets.Ticket, ticketURL string) string {
	action := ticket.State
	switch ticket.State {
	case tickets.StatePRReady:
		action = "PR ready"
	case tickets.StateFailed:
		action = "failed"
	}
	var b strings.Builder
	if ticket.Repo != "" {
		b.WriteByte('[')
		b.WriteString(ticket.Repo)
		b.WriteString("] ")
	}
	b.WriteString(action)
	if title := strings.TrimSpace(ticket.Title); title != "" {
		b.WriteString(": ")
		b.WriteString(title)
	}
	meta := []string{ticket.ID}
	if by := strings.TrimSpace(ticket.CreatedBy); by != "" {
		meta = append(meta, "assigned by "+by)
	}
	b.WriteString(" (")
	b.WriteString(strings.Join(meta, ", "))
	b.WriteByte(')')
	if ticketURL != "" {
		b.WriteByte(' ')
		b.WriteString(ticketURL)
	}
	return b.String()
}

// needsPerson reports whether ticket is now waiting on a person: failed, or
// pr_ready with no control-plane auto-merge / check watch still running.
func needsPerson(ticket tickets.Ticket) bool {
	return ticket.State == tickets.StateFailed || (ticket.State == tickets.StatePRReady && ticket.Shipping == "")
}

// WebhookNotifier posts one JSON message per qualifying transition to a webhook
// URL, asynchronously and without retries. Failed posts are logged.
type WebhookNotifier struct {
	URL       string
	PublicURL string
	Client    *http.Client
	Log       io.Writer

	initOnce sync.Once
	sem      chan struct{}
	wg       sync.WaitGroup
	logMu    sync.Mutex
}

// NewWebhookNotifier returns a WebhookNotifier for webhookURL, or nil when
// webhookURL is empty.
func NewWebhookNotifier(webhookURL, publicURL string, log io.Writer) *WebhookNotifier {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil
	}
	if log == nil {
		log = io.Discard
	}
	return &WebhookNotifier{
		URL:       webhookURL,
		PublicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		Client:    &http.Client{Timeout: defaultNotifyTimeout},
		Log:       log,
	}
}

func (n *WebhookNotifier) init() {
	n.initOnce.Do(func() {
		n.sem = make(chan struct{}, maxNotifyInFlight)
	})
}

// Notify queues an asynchronous POST to the webhook. It never blocks the caller
// and never retries on failure.
func (n *WebhookNotifier) Notify(ticket tickets.Ticket) {
	if n == nil || strings.TrimSpace(n.URL) == "" {
		return
	}
	n.init()
	select {
	case n.sem <- struct{}{}:
	default:
		n.logf("machinist: notify %s: too many notifications in flight, dropping\n", ticket.ID)
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		defer func() { <-n.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout())
		defer cancel()
		if err := n.post(ctx, ticket); err != nil {
			n.logf("machinist: notify %s: %v\n", ticket.ID, redactURLError(err))
		}
	}()
}

// Wait blocks until all in-flight webhook posts have finished.
func (n *WebhookNotifier) Wait() {
	if n == nil {
		return
	}
	n.wg.Wait()
}

func (n *WebhookNotifier) timeout() time.Duration {
	if n.Client != nil && n.Client.Timeout > 0 {
		return n.Client.Timeout
	}
	return defaultNotifyTimeout
}

func (n *WebhookNotifier) httpClient() *http.Client {
	if n.Client != nil {
		return n.Client
	}
	return &http.Client{Timeout: defaultNotifyTimeout}
}

func (n *WebhookNotifier) post(ctx context.Context, ticket tickets.Ticket) error {
	payload, err := json.Marshal(BuildNotification(ticket, n.PublicURL))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := n.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook replied %s", response.Status)
	}
	return nil
}

func (n *WebhookNotifier) logf(format string, args ...any) {
	if n.Log == nil {
		return
	}
	n.logMu.Lock()
	defer n.logMu.Unlock()
	fmt.Fprintf(n.Log, format, args...)
}

// redactURLError strips the webhook URL from *url.Error so secret webhook paths
// are never written to logs.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
