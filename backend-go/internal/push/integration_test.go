package push

import (
	"context"
	"errors"
	"testing"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/testdb"
)

// These run against a disposable database (see internal/testdb) and check the SQL behind the Repo interface.

func TestStoreKeysAreCreatedOnce(t *testing.T) {
	db := store.New(testdb.Pool(t))
	ctx := context.Background()
	if _, _, ok, err := db.VAPID(ctx); err != nil || ok {
		t.Fatalf("fresh database: ok=%v err=%v", ok, err)
	}
	if err := db.SaveVAPID(ctx, "pub1", "prv1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveVAPID(ctx, "pub2", "prv2"); err != nil { // a second server starting at the same time
		t.Fatal(err)
	}
	pub, prv, ok, _ := db.VAPID(ctx)
	if !ok || pub != "pub1" || prv != "prv1" {
		t.Errorf("the first pair must win: %q %q", pub, prv)
	}
}

func TestStoreSubscriptions(t *testing.T) {
	db := store.New(testdb.Pool(t))
	ctx := context.Background()
	sub := func(ep string) store.PushSubscription {
		return store.PushSubscription{Endpoint: ep, P256dh: "k", Auth: "a", UserAgent: "ua"}
	}
	for _, ep := range []string{"https://a/1", "https://a/2"} {
		if err := db.SaveSubscription(ctx, sub(ep), 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveSubscription(ctx, sub("https://a/3"), 2); !errors.Is(err, store.ErrTooManySubscriptions) {
		t.Errorf("third subscription: %v", err)
	}
	// refreshing one that exists works at the limit, and updates its keys
	refreshed := sub("https://a/1")
	refreshed.P256dh = "k2"
	if err := db.SaveSubscription(ctx, refreshed, 2); err != nil {
		t.Fatal(err)
	}
	got, err := db.Subscription(ctx, "https://a/1")
	if err != nil || got.P256dh != "k2" || got.UserAgent != "ua" {
		t.Errorf("after refresh: %+v %v", got, err)
	}
	if _, err := db.Subscription(ctx, "https://a/none"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown endpoint: %v", err)
	}
	all, _ := db.ListSubscriptions(ctx)
	if len(all) != 2 {
		t.Errorf("list: %d", len(all))
	}

	// failures count up, success resets, the limit removes
	id := got.ID
	for i := 1; i <= 4; i++ {
		removed, err := db.MarkPushDelivery(ctx, id, false, 5)
		if err != nil || removed {
			t.Fatalf("failure %d: removed=%v err=%v", i, removed, err)
		}
	}
	if s, _ := db.Subscription(ctx, "https://a/1"); s.Failures != 4 {
		t.Errorf("failures = %d", s.Failures)
	}
	if _, err := db.MarkPushDelivery(ctx, id, true, 5); err != nil {
		t.Fatal(err)
	}
	if s, _ := db.Subscription(ctx, "https://a/1"); s.Failures != 0 {
		t.Errorf("failures after success = %d", s.Failures)
	}
	for i := 1; i <= 5; i++ {
		removed, _ := db.MarkPushDelivery(ctx, id, false, 5)
		if removed != (i == 5) {
			t.Fatalf("failure %d: removed=%v", i, removed)
		}
	}
	if _, err := db.Subscription(ctx, "https://a/1"); !errors.Is(err, store.ErrNotFound) {
		t.Error("the failing subscription was kept")
	}

	if ok, _ := db.DeleteSubscription(ctx, "https://a/2"); !ok {
		t.Error("delete reported nothing removed")
	}
	if ok, _ := db.DeleteSubscription(ctx, "https://a/2"); ok {
		t.Error("deleting twice reported a removal")
	}
}

func TestStoreConcurrentSubscribersCannotExceedTheLimit(t *testing.T) {
	db := store.New(testdb.Pool(t))
	ctx := context.Background()
	const max, tries = 5, 20
	errs := make(chan error, tries)
	for i := 0; i < tries; i++ {
		go func() {
			errs <- db.SaveSubscription(ctx, store.PushSubscription{Endpoint: "https://a/" + string(rune('a'+i)), P256dh: "k", Auth: "a"}, max)
		}()
	}
	ok := 0
	for i := 0; i < tries; i++ {
		if err := <-errs; err == nil {
			ok++
		} else if !errors.Is(err, store.ErrTooManySubscriptions) {
			t.Fatal(err)
		}
	}
	all, _ := db.ListSubscriptions(ctx)
	if ok != max || len(all) != max {
		t.Errorf("accepted %d, stored %d, limit %d", ok, len(all), max)
	}
}
