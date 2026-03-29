package cache

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryMiss(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})
	calls := 0
	fetchFn := func() (string, error) {
		calls++
		return "hello", nil
	}

	data, found, stale, refetchFn := Query(c, "key", fetchFn)
	if found {
		t.Fatal("expected cache miss")
	}
	if stale {
		t.Fatal("expected not stale on miss")
	}
	if data != "" {
		t.Fatalf("expected zero value, got %q", data)
	}
	if refetchFn == nil {
		t.Fatal("expected refetchFn on miss")
	}

	result, err := refetchFn()
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Fatalf("expected hello, got %q", result)
	}
	if calls != 1 {
		t.Fatalf("expected 1 fetch call, got %d", calls)
	}
}

func TestQueryFresh(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})
	Set(c, "key", "cached")

	data, found, stale, refetchFn := Query(c, "key", func() (string, error) {
		t.Fatal("should not fetch")
		return "", nil
	})

	if !found {
		t.Fatal("expected cache hit")
	}
	if stale {
		t.Fatal("expected fresh")
	}
	if data != "cached" {
		t.Fatalf("expected cached, got %q", data)
	}
	if refetchFn != nil {
		t.Fatal("expected nil refetchFn for fresh data")
	}
}

func TestQueryStale(t *testing.T) {
	c := New(Options{StaleTime: time.Millisecond})
	Set(c, "key", "old")
	time.Sleep(5 * time.Millisecond)

	data, found, stale, refetchFn := Query(c, "key", func() (string, error) {
		return "new", nil
	})

	if !found {
		t.Fatal("expected cache hit")
	}
	if !stale {
		t.Fatal("expected stale")
	}
	if data != "old" {
		t.Fatalf("expected old, got %q", data)
	}
	if refetchFn == nil {
		t.Fatal("expected refetchFn for stale data")
	}

	result, err := refetchFn()
	if err != nil {
		t.Fatal(err)
	}
	if result != "new" {
		t.Fatalf("expected new, got %q", result)
	}

	// Now should be fresh again.
	data, found, stale, refetchFn = Query(c, "key", func() (string, error) {
		t.Fatal("should not fetch")
		return "", nil
	})
	if !found || stale || data != "new" || refetchFn != nil {
		t.Fatal("expected fresh hit with new data")
	}
}

func TestDeduplication(t *testing.T) {
	c := New(Options{StaleTime: time.Millisecond})
	Set(c, "key", "data")
	time.Sleep(5 * time.Millisecond)

	var fetchCount atomic.Int32
	fetchFn := func() (string, error) {
		fetchCount.Add(1)
		return "updated", nil
	}

	// First query — gets refetchFn.
	_, _, _, refetchFn1 := Query(c, "key", fetchFn)
	if refetchFn1 == nil {
		t.Fatal("expected refetchFn on first stale query")
	}

	// Second query — should not get refetchFn (dedup).
	_, found, stale, refetchFn2 := Query(c, "key", fetchFn)
	if !found || !stale {
		t.Fatal("expected stale hit")
	}
	if refetchFn2 != nil {
		t.Fatal("expected nil refetchFn due to deduplication")
	}

	// Execute the first refetch.
	refetchFn1()
	if fetchCount.Load() != 1 {
		t.Fatalf("expected exactly 1 fetch, got %d", fetchCount.Load())
	}
}

func TestInvalidate(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})
	Set(c, "key", "data")

	c.Invalidate("key")

	_, found, _, _ := Query(c, "key", func() (string, error) { return "", nil })
	if found {
		t.Fatal("expected miss after invalidation")
	}
}

func TestInvalidatePrefix(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})
	Set(c, "pull:1", "pr1")
	Set(c, "pull:2", "pr2")
	Set(c, "pulls", "list")

	c.InvalidatePrefix("pull:")

	// pull:1 and pull:2 should be gone.
	_, found1, _, _ := Query(c, "pull:1", func() (string, error) { return "", nil })
	_, found2, _, _ := Query(c, "pull:2", func() (string, error) { return "", nil })
	if found1 || found2 {
		t.Fatal("expected pull: prefixed keys to be invalidated")
	}

	// "pulls" should survive (doesn't start with "pull:").
	data, found, _, _ := Query(c, "pulls", func() (string, error) { return "", nil })
	if !found || data != "list" {
		t.Fatal("expected 'pulls' to survive prefix invalidation")
	}
}

func TestGC(t *testing.T) {
	c := New(Options{StaleTime: time.Minute, GCTime: time.Millisecond})
	Set(c, "old", "data")
	time.Sleep(5 * time.Millisecond)

	// Access "fresh" so it stays alive.
	Set(c, "fresh", "data")

	c.GC()

	_, foundOld, _, _ := Query(c, "old", func() (string, error) { return "", nil })
	if foundOld {
		t.Fatal("expected old entry to be GC'd")
	}

	data, foundFresh, _, _ := Query(c, "fresh", func() (string, error) { return "", nil })
	if !foundFresh || data != "data" {
		t.Fatal("expected fresh entry to survive GC")
	}
}

func TestRefetchFnUpdatesCache(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})

	_, _, _, refetchFn := Query(c, "key", func() (string, error) {
		return "fetched", nil
	})

	refetchFn()

	data, found, stale, _ := Query(c, "key", func() (string, error) {
		t.Fatal("should not fetch")
		return "", nil
	})
	if !found || stale || data != "fetched" {
		t.Fatalf("expected fresh cached data 'fetched', got found=%v stale=%v data=%q", found, stale, data)
	}
}

func TestQueryError(t *testing.T) {
	c := New(Options{StaleTime: time.Minute})
	expectedErr := errors.New("api error")

	_, _, _, refetchFn := Query(c, "key", func() (string, error) {
		return "", expectedErr
	})

	_, err := refetchFn()
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error, got %v", err)
	}

	// Error is cached but should become stale quickly on next query
	// with a short stale time so errors don't persist.
}
