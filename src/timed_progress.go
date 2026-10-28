package main

import (
	"context"
	"errors"
	"math"
	"time"
)

// 只在原有网络等待期间唤醒显示，不改变请求轮询和心跳的调度。
type timedProgressDisplay struct {
	clock     Clock
	start     time.Time
	base      float64
	duration  float64
	remaining time.Duration
	last      int
	notify    func(float64)
}

func (p *timedProgressDisplay) threshold(whole int) time.Duration {
	if whole == 100 {
		return p.remaining
	}
	return max(0, time.Duration(math.Ceil((float64(whole)-p.base)/100*p.duration*float64(time.Second))))
}

func (p *timedProgressDisplay) emit(elapsed time.Duration) {
	if p.notify == nil {
		return
	}
	for p.last < 100 && elapsed >= p.threshold(p.last+1) {
		p.last++
		p.notify(float64(p.last))
	}
}

func (p *timedProgressDisplay) wait(ctx context.Context, delay time.Duration) error {
	if p.notify == nil {
		return p.clock.Wait(ctx, delay)
	}
	deadline := p.clock.Now().Add(delay)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := p.clock.Now()
		elapsed := now.Sub(p.start)
		if elapsed < 0 {
			return errors.New("时钟异常")
		}
		p.emit(elapsed)
		wait := deadline.Sub(p.clock.Now())
		if wait <= 0 {
			return nil
		}
		if p.last < 100 {
			untilNext := p.threshold(p.last+1) - p.clock.Now().Sub(p.start)
			if untilNext <= 0 {
				continue
			}
			wait = min(wait, untilNext)
		}
		if err := p.clock.Wait(ctx, wait); err != nil {
			return err
		}
	}
}
