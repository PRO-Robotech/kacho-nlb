// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// semaphore.go — простой buffered-channel limiter для concurrent stream slots.
//
// Используется handler-ом Subscribe: каждый stream должен сначала захватить
// слот (TryAcquire) — если все слоты заняты, клиент получает ResourceExhausted
// сразу, без блокирования.
package internal_lifecycle

// Semaphore — counting semaphore поверх buffered channel. Cap = max concurrent
// holders. Не fair (Go-runtime сам выбирает waiting goroutine), но fairness
// здесь не требуется: TryAcquire не блокирующий, либо мгновенный success, либо
// мгновенный fail.
type Semaphore struct {
	slots chan struct{}
}

// NewSemaphore создаёт semaphore с указанной capacity. Panic при cap <= 0
// (защита от неинициализированного config — caller обязан валидировать).
func NewSemaphore(cap int) *Semaphore {
	if cap <= 0 {
		panic("internal_lifecycle: Semaphore capacity must be > 0")
	}
	return &Semaphore{slots: make(chan struct{}, cap)}
}

// TryAcquire пытается захватить слот неблокирующе. Возвращает true если успех
// (caller обязан вызвать Release когда закончит), false если все слоты заняты.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release освобождает один слот. Должен вызываться ровно один раз на каждый
// успешный TryAcquire (обычно через defer).
func (s *Semaphore) Release() {
	<-s.slots
}

// Capacity возвращает максимальное число одновременных holders.
func (s *Semaphore) Capacity() int {
	return cap(s.slots)
}

// Held возвращает текущее число захваченных слотов (snapshot, может устареть
// сразу после возврата). Используется для observability/metrics.
func (s *Semaphore) Held() int {
	return len(s.slots)
}
