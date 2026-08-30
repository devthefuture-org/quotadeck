package domain

import "testing"

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

func TestNormalizeSnapshotRejectsDuplicateDynamicWindowIDs(t *testing.T) {
	_, err := NormalizeSnapshot(Snapshot{AccountID: "account", Windows: []QuotaWindow{
		{ID: "same", Label: "First"}, {ID: "same", Label: "Second"},
	}})
	if err == nil {
		t.Fatal("expected duplicate window id error")
	}
}
