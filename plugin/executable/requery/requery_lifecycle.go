package requery

import (
	"log"
	"time"
)

func (p *Requery) Close() error {
	p.closeOnce.Do(func() {
		if p.closeCh != nil {
			close(p.closeCh)
		}

		p.mu.Lock()
		scheduleTimer := p.scheduleTimer
		resumeTimer := p.resumeTimer
		taskCancel := p.taskCancel
		p.scheduleTimer = nil
		p.resumeTimer = nil
		p.taskCancel = nil
		p.taskCtx = nil
		p.mu.Unlock()

		if scheduleTimer != nil {
			scheduleTimer.Stop()
		}
		if resumeTimer != nil {
			resumeTimer.Stop()
		}
		if taskCancel != nil {
			taskCancel()
		}
		if p.onDemandStarted.Load() && p.onDemandDone != nil {
			<-p.onDemandDone
		}
	})
	return nil
}

func (p *Requery) isClosed() bool {
	if p == nil || p.closeCh == nil {
		return false
	}
	select {
	case <-p.closeCh:
		return true
	default:
		return false
	}
}

func (p *Requery) stopTimer(timer **time.Timer) {
	if timer == nil || *timer == nil {
		return
	}
	(*timer).Stop()
	*timer = nil
}

func (p *Requery) rescheduleTasks() {
	if err := p.setupScheduler(); err != nil {
		log.Printf("[requery] WARN: Failed to reschedule tasks: %v", err)
	}
}

func (p *Requery) setupScheduler() error {
	p.mu.Lock()
	p.stopTimer(&p.scheduleTimer)
	if p.isClosed() {
		p.mu.Unlock()
		return nil
	}
	allowsSweep := p.allowsSweep()
	startRaw := p.config.Scheduler.StartDatetime
	intervalMinutes := p.config.Scheduler.IntervalMinutes
	p.mu.Unlock()

	if !allowsSweep {
		log.Println("[requery] Sweep scheduler is disabled or interval is invalid in config.")
		return nil
	}

	startTime := time.Now().UTC()
	if startRaw != "" {
		parsed, err := time.Parse(time.RFC3339, startRaw)
		if err != nil {
			log.Printf("[requery] WARN: Invalid 'start_datetime' format ('%s'), using interval from now: %v", startRaw, err)
		} else {
			startTime = parsed
		}
	}

	jobFunc := func() {
		profile := p.profileForMode("quick_rebuild", 0)
		log.Printf("[requery] Scheduler is triggering a task: %s.", profile.Mode)
		if ok := p.startTaskWithSource(profile, nil, "scheduler"); !ok {
			log.Println("[requery] Scheduler skipped: previous task is still running.")
		}
	}

	now := time.Now().UTC()
	interval := time.Duration(intervalMinutes) * time.Minute
	var nextRunTime time.Time
	if startTime.After(now) {
		nextRunTime = startTime
	} else {
		elapsed := now.Sub(startTime)
		cyclesPassed := elapsed / interval
		nextRunTime = startTime.Add(time.Duration(cyclesPassed+1) * interval)
	}

	delay := nextRunTime.Sub(now)
	if delay <= 0 {
		log.Printf("[requery] Calculated next run time (%v) is in the past. Attempting to reschedule immediately.", nextRunTime.Local())
		go p.rescheduleTasks()
		return nil
	}

	log.Printf("[requery] Next scheduled run will be at %v (in %v).", nextRunTime.Local(), delay.Round(time.Second))

	timer := time.AfterFunc(delay, func() {
		if p.isClosed() {
			return
		}
		jobFunc()
		if p.isClosed() {
			return
		}
		p.rescheduleTasks()
	})

	p.mu.Lock()
	if p.isClosed() {
		p.mu.Unlock()
		timer.Stop()
		return nil
	}
	p.scheduleTimer = timer
	p.mu.Unlock()
	return nil
}
