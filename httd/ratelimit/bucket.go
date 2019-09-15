package ratelimit

import (
	"sync"
	"time"
)

type bucketID struct {
	global bool
	reset  time.Time
}

func newBucket(global *bucket) *bucket {
	return &bucket{
		global:    global,
		remaining: 1,
		limit:     1,
		reset:     time.Now().Add(1 * time.Hour),
	}
}

type LocalKey string

// bucket holds the rate limit info for a given key or endpoint
type bucket struct {
	sync.RWMutex
	key       string // discord designated key
	localKeys []LocalKey

	invalid     bool
	lastUpdated time.Time

	limit     uint // total allowed requests before rate limit
	remaining uint // remaining requests
	reset     time.Time

	// milliseconds
	longestTimeout  uint // store the longest timeout to simulate a reset correctly
	shortestTimeout uint

	global *bucket
	active bool
}

func (b *bucket) LinkedTo(localKey LocalKey) (yes bool) {
	b.RLock()
	for i := range b.localKeys {
		if b.localKeys[i] == localKey {
			yes = true
			break
		}
	}
	b.RUnlock()
	return yes
}

func (b *bucket) AddLocalKey(key LocalKey) {
	b.Lock()
	for i := range b.localKeys {
		if b.localKeys[i] == key {
			b.Unlock()
			return
		}
	}
	b.localKeys = append(b.localKeys, key)
	b.Unlock()
}

func (b *bucket) Acquire(now time.Time, within time.Duration) (delay time.Duration, rateLimited bool, id bucketID, err error) {
	var ok bool
	b.global.Lock()
	if b.global.active {
		delay, rateLimited, err = b.global.acquire(now, within)
		id = bucketID{global: true, reset: b.global.reset}
		ok = true
	}
	b.global.Unlock()
	if ok {
		return delay, rateLimited, id, err
	}

	b.Lock()
	defer b.Unlock()
	delay, rateLimited, err = b.acquire(now, within)
	id.reset = b.reset
	return delay, rateLimited, id, err
}

func (b *bucket) RegretAcquire(id bucketID) {
	var bu *bucket
	if id.global {
		bu = b.global
	} else {
		bu = b
	}

	bu.Lock()
	if id.reset == bu.reset {
		bu.regretAcquire()
	}
	bu.Unlock()
}

func (b *bucket) regretAcquire() {
	b.remaining++
}

func (b *bucket) acquire(now time.Time, within time.Duration) (delay time.Duration, rateLimited bool, err error) {
	b.update(now)
	if b.limited(now) {
		if within > 0 && b.reset.Before(now.Add(within)) {
			b.remaining--
			return now.Add(within).Sub(b.reset), true, nil
		}
		return 0, true, ErrRateLimited
	}

	b.remaining--
	return 0, false, nil
}

func (b *bucket) update(now time.Time) {
	if b.longestTimeout == 0 {
		return
	}

	if !b.reset.After(now) {
		b.remaining = 1 // allow only one request to be sent to update the fields
		b.reset = now.Add(time.Duration(b.longestTimeout) * time.Millisecond)
	}
	b.lastUpdated = now
}

func (b *bucket) limited(now time.Time) bool {
	return b.reset.After(now) && b.remaining == 0
}

func (b *bucket) dec() {
	b.remaining--
}

func (b *bucket) inc() {
	b.remaining++
}
