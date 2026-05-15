package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// TestVerifySignature exercises the HMAC-SHA256 signature check that gates
// every incoming GitHub webhook delivery.
func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"hello":"world"}`)
	secret := "secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySignature(body, secret, sig) {
		t.Fatalf("expected signature to verify")
	}
	if verifySignature(body, secret, "sha256=deadbeef") {
		t.Fatalf("bad signature should not verify")
	}
	if verifySignature(body, "", sig) {
		t.Fatalf("empty secret must not verify")
	}
}

func TestPushEventCarriesRepoWorkspace(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	repo := fleet.Repo{
		WorkspaceID: "team-a",
		Name:        "owner/repo",
		Enabled:     true,
	}
	agents := []fleet.Agent{{
		Name:        "reviewer",
		Backend:     "claude",
		Prompt:      "Review events.",
		Description: "Reviews repository events",
	}}
	backends := map[string]fleet.Backend{"claude": {Command: "claude"}}
	if err := st.ImportAll(agents, []fleet.Repo{repo}, nil, backends, nil, nil); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(NewDeliveryStore(10*time.Minute), dc, st, config.HTTPConfig{}, zerolog.Nop())
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"0123456789012345678901234567890123456789",
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()

	h.handlePushEvent(context.Background(), w, body, "delivery-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case queued := <-dc.EventChan():
		if queued.Event.WorkspaceID != "team-a" {
			t.Fatalf("WorkspaceID = %q, want team-a", queued.Event.WorkspaceID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for queued event")
	}
}
