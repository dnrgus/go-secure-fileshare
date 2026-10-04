package main

import (
	"context"
	"sync"
	"time"
)

// limiter 는 IP별 토큰 버킷이다. 분당 N회를 허용하고, 최대 N회까지 한꺼번에 쓸 수 있다(burst).
type limiter struct {
	mu    sync.Mutex
	rate  float64 // 초당 충전되는 토큰 수
	burst float64
	m     map[string]*visitor
}

type visitor struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute int) *limiter {
	return &limiter{
		rate:  float64(perMinute) / 60,
		burst: float64(perMinute),
		m:     make(map[string]*visitor),
	}
}

func (l *limiter) Allow(key string) bool {
	if l.burst <= 0 { // 비활성화
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	v, ok := l.m[key]
	if !ok {
		v = &visitor{tokens: l.burst, last: now}
		l.m[key] = v
	}
	v.tokens += now.Sub(v.last).Seconds() * l.rate
	if v.tokens > l.burst {
		v.tokens = l.burst
	}
	v.last = now

	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}

// GCLoop 는 오래 안 보인 IP 항목을 지워 맵이 무한히 커지는 것을 막는다.
func (l *limiter) GCLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.mu.Lock()
			for k, v := range l.m {
				if now.Sub(v.last) > 2*time.Minute {
					delete(l.m, k)
				}
			}
			l.mu.Unlock()
		}
	}
}
