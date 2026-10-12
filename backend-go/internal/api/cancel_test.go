package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

type cancelledStore struct{ fakeStore }

func (*cancelledStore) Summary(context.Context, store.Filter) (model.Summary, error) {
	return model.Summary{}, fmt.Errorf("query: %w", context.Canceled)
}

func TestAClientThatGoesAwayIsNotAServerError(t *testing.T) {
	var logs bytes.Buffer
	s := NewServer(&cancelledStore{}, NewAuthenticator(config.ParseTokens("")), "UTC",
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	rec := do(t, s, "GET", "/api/v1/stats/summary", "", "")
	if rec.Code != 499 {
		t.Errorf("status %d, want 499 (client closed request)", rec.Code)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a disconnected client must not be logged as an error:\n%s", logs.String())
	}
}
