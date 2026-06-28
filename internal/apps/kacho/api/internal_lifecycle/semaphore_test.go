// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package internal_lifecycle

import (
	"sync"
	"testing"
)

// TestSemaphore_TryAcquireFillsAndRejects — основной boundary-сценарий:
// первые N TryAcquire успешны, (N+1)-й fails сразу.
func TestSemaphore_TryAcquireFillsAndRejects(t *testing.T) {
	sem := NewSemaphore(3)

	if got := sem.Capacity(); got != 3 {
		t.Fatalf("Capacity: got %d, want 3", got)
	}
	for i := 0; i < 3; i++ {
		if !sem.TryAcquire() {
			t.Fatalf("TryAcquire #%d: expected success", i+1)
		}
	}
	if sem.TryAcquire() {
		t.Fatal("TryAcquire #4: expected fail (cap exhausted)")
	}
	if got := sem.Held(); got != 3 {
		t.Fatalf("Held after fill: got %d, want 3", got)
	}
}

// TestSemaphore_ReleaseFreesSlot — Release возвращает один слот в pool,
// следующий TryAcquire успешен.
func TestSemaphore_ReleaseFreesSlot(t *testing.T) {
	sem := NewSemaphore(2)
	for i := 0; i < 2; i++ {
		if !sem.TryAcquire() {
			t.Fatalf("fill #%d: expected success", i+1)
		}
	}
	if sem.TryAcquire() {
		t.Fatal("3rd: expected fail")
	}

	sem.Release()
	if got := sem.Held(); got != 1 {
		t.Fatalf("Held after Release: got %d, want 1", got)
	}
	if !sem.TryAcquire() {
		t.Fatal("after Release: expected success")
	}
}

// TestSemaphore_Concurrent — параллельные TryAcquire соблюдают cap.
// Гонка 100 горутин на cap=10 → ровно 10 успехов.
func TestSemaphore_Concurrent(t *testing.T) {
	const cap = 10
	const tries = 100
	sem := NewSemaphore(cap)

	var wg sync.WaitGroup
	var succ int64
	var mu sync.Mutex

	for i := 0; i < tries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sem.TryAcquire() {
				mu.Lock()
				succ++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succ != cap {
		t.Fatalf("concurrent TryAcquire: got %d successes, want %d", succ, cap)
	}
	if got := sem.Held(); got != cap {
		t.Fatalf("Held after concurrent fill: got %d, want %d", got, cap)
	}
}

// TestSemaphore_PanicOnZeroCap — конструктор не позволяет cap<=0
// (иначе bufchan(0) залочит всё на первом TryAcquire с разными
// race-сценариями).
func TestSemaphore_PanicOnZeroCap(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on NewSemaphore(0)")
		}
	}()
	_ = NewSemaphore(0)
}

func TestSemaphore_PanicOnNegativeCap(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on NewSemaphore(-1)")
		}
	}()
	_ = NewSemaphore(-1)
}
