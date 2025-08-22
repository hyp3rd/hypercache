package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

func TestNewItemPoolManager(t *testing.T) {
	manager := NewItemPoolManager()
	if manager == nil {
		t.Fatal("Expected non-nil ItemPoolManager")
	}
}

func TestItemPoolManager_Get(t *testing.T) {
	manager := NewItemPoolManager()

	item := manager.Get()
	if item == nil {
		t.Fatal("Expected non-nil Item")
	}
}

func TestItemPoolManager_Put(t *testing.T) {
	manager := NewItemPoolManager()

	// Test putting nil item
	manager.Put(nil)

	// Test putting valid item
	item := &Item{Key: "test", Value: "value"}
	manager.Put(item)

	// Get item back and verify it's zeroed
	retrieved := manager.Get()
	if retrieved.Key != "" || retrieved.Value != nil {
		t.Error("Expected item to be zeroed after Put")
	}
}

func TestItem_SetSize(t *testing.T) {
	item := &Item{Value: "test string"}

	err := item.SetSize()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if item.Size <= 0 {
		t.Error("Expected positive size")
	}
}

func TestItem_SizeMB(t *testing.T) {
	item := &Item{Size: 1024 * 1024} // 1 MB

	sizeMB := item.SizeMB()
	if sizeMB != 1.0 {
		t.Errorf("Expected 1.0 MB, got %f", sizeMB)
	}
}

func TestItem_SizeKB(t *testing.T) {
	item := &Item{Size: 1024} // 1 KB

	sizeKB := item.SizeKB()
	if sizeKB != 1.0 {
		t.Errorf("Expected 1.0 KB, got %f", sizeKB)
	}
}

func TestItem_Touch(t *testing.T) {
	item := &Item{}
	initialTime := item.LastAccess
	initialCount := item.AccessCount

	time.Sleep(time.Millisecond) // Ensure time difference
	item.Touch()

	if !item.LastAccess.After(initialTime) {
		t.Error("Expected LastAccess to be updated")
	}

	if item.AccessCount != initialCount+1 {
		t.Errorf("Expected AccessCount to be %d, got %d", initialCount+1, item.AccessCount)
	}
}

func TestItem_Valid(t *testing.T) {
	tests := []struct {
		name    string
		item    *Item
		wantErr error
	}{
		{
			name:    "empty key",
			item:    &Item{Key: "", Value: "test"},
			wantErr: sentinel.ErrInvalidKey,
		},
		{
			name:    "whitespace key",
			item:    &Item{Key: "   ", Value: "test"},
			wantErr: sentinel.ErrInvalidKey,
		},
		{
			name:    "nil value",
			item:    &Item{Key: "test", Value: nil},
			wantErr: sentinel.ErrNilValue,
		},
		{
			name:    "negative expiration",
			item:    &Item{Key: "test", Value: "value", Expiration: -time.Second},
			wantErr: sentinel.ErrInvalidExpiration,
		},
		{
			name:    "valid item",
			item:    &Item{Key: "test", Value: "value", Expiration: time.Second},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.item.Valid()
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestItem_Expired(t *testing.T) {
	tests := []struct {
		name        string
		item        *Item
		wantExpired bool
	}{
		{
			name: "never expires (zero expiration)",
			item: &Item{
				Expiration: 0,
				LastAccess: time.Now().Add(-time.Hour),
			},
			wantExpired: false,
		},
		{
			name: "not expired",
			item: &Item{
				Expiration: time.Hour,
				LastAccess: time.Now().Add(-time.Minute),
			},
			wantExpired: false,
		},
		{
			name: "expired",
			item: &Item{
				Expiration: time.Minute,
				LastAccess: time.Now().Add(-time.Hour),
			},
			wantExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.Expired(); got != tt.wantExpired {
				t.Errorf("Expected expired=%v, got %v", tt.wantExpired, got)
			}
		})
	}
}
