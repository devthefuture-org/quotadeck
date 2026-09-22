package domain

import (
	"testing"
	"time"
)

func TestNormalizeWindowUsesConsumedPercent(t *testing.T) {
	remaining := 22.0
	window, err := NormalizeWindow(QuotaWindow{ID: "dynamic", Label: "Dynamic", RemainingPercent: &remaining})
	if err != nil {
		t.Fatal(err)
	}
	if window.UsedPercent == nil || *window.UsedPercent != 78 {
		t.Fatalf("expected 78%% consumed, got %#v", window.UsedPercent)
	}
}

func TestStaleFromTracksAgeAcrossRepeatedFailures(t *testing.T) {
	now := time.Now()
	sourceAge := int64(10)
	previous := Snapshot{AccountID: "codex", FetchedAt: now.Add(-time.Minute), SourceAgeSec: &sourceAge}
	stale := StaleFrom(previous, now, StatusUnavailable, "timeout", "fetch timed out")
	stale = StaleFrom(stale, now.Add(2*time.Minute), StatusUnavailable, "timeout", "fetch timed out")
	if stale.SourceAgeSec == nil || *stale.SourceAgeSec != 190 {
		t.Fatalf("expected last known quota age of 190 seconds, got %#v", stale.SourceAgeSec)
	}
	if *previous.SourceAgeSec != 10 {
		t.Fatal("changed the original snapshot's source age")
	}
}

func TestNormalizeSnapshotRejectsDuplicateDynamicWindowIDs(t *testing.T) {
	_, err := NormalizeSnapshot(Snapshot{AccountID: "account", Windows: []QuotaWindow{
		{ID: "same", Label: "First"}, {ID: "same", Label: "Second"},
	}})
	if err == nil {
		t.Fatal("expected duplicate window id error")
	}
}

func TestNormalizeSnapshotPreservesProviderWindowOrder(t *testing.T) {
	resetSoon := time.Now().Add(time.Hour)
	resetLater := resetSoon.Add(24 * time.Hour)
	snapshot, err := NormalizeSnapshot(Snapshot{AccountID: "account", Windows: []QuotaWindow{
		{ID: "five-hour", Label: "5 hours", ResetsAt: &resetLater},
		{ID: "seven-day", Label: "7 days", ResetsAt: &resetSoon},
		{ID: "scoped-fable", Label: "Fable", ResetsAt: &resetSoon},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"five-hour", "seven-day", "scoped-fable"}
	for index, window := range snapshot.Windows {
		if window.ID != want[index] {
			t.Fatalf("window %d: expected %q, got %q", index, want[index], window.ID)
		}
	}
}
