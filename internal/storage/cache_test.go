package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCacheEntriesCRUDExpiryLRUAndPurge(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := int64(100000)

	// 1. Put and Get
	err = store.PutCacheEntry(ctx, CacheEntry{
		CacheKey:            "k1",
		RouteNamespace:      "proj:rule:m1",
		APIFormat:           "gemini_native",
		Model:               "m1",
		IsStream:            true,
		ResponseHeadersJSON: `{"Content-Type":"text/event-stream"}`,
		ResponseBody:        []byte("data: hi\n\n"),
		PromptTokens:        10,
		CompletionTokens:    5,
		Embedding:           []byte{1, 2, 3, 4},
		CreatedAtUTC:        base,
		ExpiresAtUTC:        base + 1000,
	}, 2)
	if err != nil {
		t.Fatalf("PutCacheEntry k1: %v", err)
	}

	got, err := store.GetCacheEntry(ctx, "k1", base+100)
	if err != nil || got == nil || !got.IsStream || string(got.ResponseBody) != "data: hi\n\n" {
		t.Fatalf("GetCacheEntry k1 = %+v, err=%v", got, err)
	}

	// 2. TouchCacheHit
	if err := store.TouchCacheHit(ctx, "k1", base+200); err != nil {
		t.Fatalf("TouchCacheHit: %v", err)
	}
	got, _ = store.GetCacheEntry(ctx, "k1", base+250)
	if got.HitCount != 1 || got.LastHitAtUTC != base+200 {
		t.Fatalf("expected HitCount=1, LastHitAtUTC=%d, got %+v", base+200, got)
	}

	// 3. Expired lookup returns nil
	expired, err := store.GetCacheEntry(ctx, "k1", base+1000)
	if err != nil || expired != nil {
		t.Fatalf("expected nil on expired key, got %+v, err=%v", expired, err)
	}

	// 4. LRU eviction with maxEntries=2
	for _, k := range []struct {
		key string
		ts  int64
	}{
		{"k2", base + 50},
		{"k3", base + 300},
	} {
		if err := store.PutCacheEntry(ctx, CacheEntry{
			CacheKey:            k.key,
			RouteNamespace:      "proj:rule:m1",
			APIFormat:           "openai_chat",
			Model:               "m1",
			ResponseHeadersJSON: `{}`,
			ResponseBody:        []byte("ok"),
			Embedding:           []byte{5, 6, 7, 8},
			CreatedAtUTC:        k.ts,
			ExpiresAtUTC:        base + 5000,
		}, 2); err != nil {
			t.Fatalf("PutCacheEntry %s: %v", k.key, err)
		}
	}

	// k2 (last_hit=NULL, created=base+50) should be evicted; k1 (last_hit=base+200) and k3 (created=base+300) remain.
	if ev, _ := store.GetCacheEntry(ctx, "k2", base+350); ev != nil {
		t.Fatalf("expected k2 to be LRU-evicted, got %+v", ev)
	}
	cands, err := store.ListSemanticCandidates(ctx, "proj:rule:m1", base+350, 10)
	if err != nil || len(cands) != 2 {
		t.Fatalf("expected 2 semantic candidates, got %d, err=%v", len(cands), err)
	}

	// 5. PurgeCache by namespace and global
	deleted, err := store.PurgeCache(ctx, "other")
	if err != nil || deleted != 0 {
		t.Fatalf("expected 0 deleted for 'other', got %d, err=%v", deleted, err)
	}
	deleted, err = store.PurgeCache(ctx, "")
	if err != nil || deleted != 2 {
		t.Fatalf("expected 2 deleted globally, got %d, err=%v", deleted, err)
	}
}
