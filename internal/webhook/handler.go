package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// Handler implements the GitHub webhook receiver: HMAC verification,
// delivery dedupe, and per-event-type parsing into workflow events. It is
// the single piece of webhook-domain logic in this package.
//
// The handler reads repos from SQLite on every request; the static HTTP
// config (webhook path, secret, body limits) is captured once at
// construction since those never mutate via CRUD.
type Handler struct {
	delivery *DeliveryStore
	channels *workflow.DataChannels
	store    *store.Store
	httpCfg  config.HTTPConfig
	logger   zerolog.Logger
}

// NewHandler constructs a Handler. delivery, channels, store, and httpCfg
// are required; logger may be the daemon's root logger (the handler
// scopes a sub-logger via the standard component label).
func NewHandler(delivery *DeliveryStore, channels *workflow.DataChannels, st *store.Store, httpCfg config.HTTPConfig, logger zerolog.Logger) *Handler {
	return &Handler{
		delivery: delivery,
		channels: channels,
		store:    st,
		httpCfg:  httpCfg,
		logger:   logger.With().Str("component", "webhook").Logger(),
	}
}

// RegisterRoutes mounts POST {httpCfg.WebhookPath} on r.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle(h.httpCfg.WebhookPath, withTimeout(http.HandlerFunc(h.handleGitHubWebhook))).Methods(http.MethodPost)
}

// repoByName returns the named repo from SQLite, or false if not present.
func (h *Handler) repoByName(name string) (fleet.Repo, bool) {
	repos, err := h.store.ReadRepos()
	if err != nil {
		h.logger.Error().Err(err).Msg("webhook: read repos")
		return fleet.Repo{}, false
	}
	want := fleet.NormalizeRepoName(name)
	for _, r := range repos {
		if r.Name == want {
			return r, true
		}
	}
	return fleet.Repo{}, false
}

func (h *Handler) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.httpCfg.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(body, h.httpCfg.WebhookSecret, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// Delivery dedup is checked only after signature verification so
	// unauthenticated requests cannot poison the dedupe cache.
	if h.delivery.SeenOrAdd(deliveryID, time.Now()) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	switch event {
	case "issues":
		h.handleIssuesEvent(r.Context(), w, body, deliveryID)
	case "pull_request":
		h.handlePullRequestEvent(r.Context(), w, body, deliveryID)
	case "issue_comment":
		h.handleIssueCommentEvent(r.Context(), w, body, deliveryID)
	case "pull_request_review":
		h.handlePullRequestReviewEvent(r.Context(), w, body, deliveryID)
	case "pull_request_review_comment":
		h.handlePullRequestReviewCommentEvent(r.Context(), w, body, deliveryID)
	case "push":
		h.handlePushEvent(r.Context(), w, body, deliveryID)
	default:
		h.logger.Warn().Str("event", event).Str("delivery_id", deliveryID).Msg("unhandled webhook event type")
		w.WriteHeader(http.StatusAccepted)
	}
}

// ─── webhook payload shapes ───────────────────────────────────────────────────

type webhookRepository struct {
	FullName string `json:"full_name"`
}

type webhookSender struct {
	Login string `json:"login"`
}

type webhookLabel struct {
	Name string `json:"name"`
}

type webhookIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

type webhookPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
}

type webhookComment struct {
	Body string `json:"body"`
}

type webhookReview struct {
	Body  string `json:"body"`
	State string `json:"state"`
}

// ─── event-type handlers ──────────────────────────────────────────────────────

// handleIssuesEvent handles X-GitHub-Event: issues.
// For "labeled" actions it filters to AI labels and emits "issues.labeled".
// For lifecycle actions (opened, edited, reopened, closed) it emits the
// corresponding "issues.{action}" event.
// Events from issues that are pull requests (GitHub sends both) are dropped
// for the "labeled" action; the pull_request event handles those.
func (h *Handler) handleIssuesEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Action     string            `json:"action"`
		Label      webhookLabel      `json:"label"`
		Issue      webhookIssue      `json:"issue"`
		Repository webhookRepository `json:"repository"`
		Sender     webhookSender     `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repoRef := workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled}

	if payload.Issue.PullRequest != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch payload.Action {
	case "labeled":
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "issues.labeled",
			Number: payload.Issue.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"label": payload.Label.Name,
			},
		}
		h.enqueue(ctx, w, ev, deliveryID)
	case "opened", "edited", "reopened", "closed":
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "issues." + payload.Action,
			Number: payload.Issue.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"title": payload.Issue.Title,
				"body":  payload.Issue.Body,
			},
		}
		h.enqueue(ctx, w, ev, deliveryID)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// handlePullRequestEvent handles X-GitHub-Event: pull_request.
// For "labeled" actions it filters to AI labels (and skips drafts) and emits
// "pull_request.labeled". For lifecycle actions it emits "pull_request.{action}".
func (h *Handler) handlePullRequestEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Action      string             `json:"action"`
		Label       webhookLabel       `json:"label"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repoRef := workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled}

	switch payload.Action {
	case "labeled":
		if payload.PullRequest.Draft {
			h.logger.Info().Str("repo", repo.Name).Int("number", payload.PullRequest.Number).Msg("pull request skipped, draft")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "pull_request.labeled",
			Number: payload.PullRequest.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"label": payload.Label.Name,
			},
		}
		h.enqueue(ctx, w, ev, deliveryID)
	case "opened", "synchronize", "ready_for_review", "closed":
		eventPayload := map[string]any{
			"title": payload.PullRequest.Title,
			"draft": payload.PullRequest.Draft,
		}
		if payload.Action == "closed" {
			eventPayload["merged"] = payload.PullRequest.Merged
		}
		ev := workflow.Event{
			ID:      deliveryID,
			Repo:    repoRef,
			Kind:    "pull_request." + payload.Action,
			Number:  payload.PullRequest.Number,
			Actor:   payload.Sender.Login,
			Payload: eventPayload,
		}
		h.enqueue(ctx, w, ev, deliveryID)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// handleIssueCommentEvent handles X-GitHub-Event: issue_comment.
// Only "created" actions are forwarded as "issue_comment.created".
func (h *Handler) handleIssueCommentEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Action     string            `json:"action"`
		Comment    webhookComment    `json:"comment"`
		Issue      webhookIssue      `json:"issue"`
		Repository webhookRepository `json:"repository"`
		Sender     webhookSender     `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "issue_comment.created",
		Number: payload.Issue.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"body": payload.Comment.Body,
		},
	}
	h.enqueue(ctx, w, ev, deliveryID)
}

// handlePullRequestReviewEvent handles X-GitHub-Event: pull_request_review.
// Only "submitted" actions are forwarded as "pull_request_review.submitted".
func (h *Handler) handlePullRequestReviewEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Action      string             `json:"action"`
		Review      webhookReview      `json:"review"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "submitted" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "pull_request_review.submitted",
		Number: payload.PullRequest.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"state": payload.Review.State,
			"body":  payload.Review.Body,
		},
	}
	h.enqueue(ctx, w, ev, deliveryID)
}

// handlePullRequestReviewCommentEvent handles X-GitHub-Event:
// pull_request_review_comment. Only "created" actions are forwarded as
// "pull_request_review_comment.created".
func (h *Handler) handlePullRequestReviewCommentEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Action      string             `json:"action"`
		Comment     webhookComment     `json:"comment"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "pull_request_review_comment.created",
		Number: payload.PullRequest.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"body": payload.Comment.Body,
		},
	}
	h.enqueue(ctx, w, ev, deliveryID)
}

// handlePushEvent handles X-GitHub-Event: push.
func (h *Handler) handlePushEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	
	var payload struct {
		Ref        string            `json:"ref"`
		After      string            `json:"after"`
		Repository webhookRepository `json:"repository"`
		Sender     webhookSender     `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := h.repoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Ignore branch deletions (After is all-zero SHA) and non-branch refs
	// (tags, notes). Only "new commit pushed to a branch" maps to push events.
	const deletedSHA = "0000000000000000000000000000000000000000"
	if payload.After == deletedSHA || !strings.HasPrefix(payload.Ref, "refs/heads/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:    deliveryID,
		Repo:  workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:  "push",
		Actor: payload.Sender.Login,
		Payload: map[string]any{
			"ref":      payload.Ref,
			"head_sha": payload.After,
		},
	}
	h.enqueue(ctx, w, ev, deliveryID)
}

// enqueue pushes ev onto the event queue, handling all error cases.
func (h *Handler) enqueue(ctx context.Context, w http.ResponseWriter, ev workflow.Event, deliveryID string) {
	if err := h.channels.PushEvent(ctx, ev); err != nil {
		if errors.Is(err, workflow.ErrEventQueueFull) {
			h.delivery.Delete(deliveryID)
			h.logger.Warn().Str("repo", ev.Repo.FullName).Str("kind", ev.Kind).Msg("event queue full, dropping webhook")
			http.Error(w, "event queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, workflow.ErrQueueClosed) {
			h.logger.Warn().Str("repo", ev.Repo.FullName).Msg("queue closed during shutdown, dropping webhook")
			http.Error(w, "shutting down, retry later", http.StatusServiceUnavailable)
			return
		}
		h.delivery.Delete(deliveryID)
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// verifySignature checks the HMAC-SHA256 signature from X-Hub-Signature-256.
// hmac.Equal is used for the final comparison to avoid timing attacks that
// could leak information about the expected value through execution time.
func verifySignature(payload []byte, secret, signature string) bool {
	signature = strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	if signature == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
