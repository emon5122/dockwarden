package scheduler

import (
	"time"

	"github.com/emon5122/dockwarden/internal/config"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"
)

// Scheduler manages the timing of update checks
type Scheduler struct {
	config   *config.Config
	cron     *cron.Cron
	ticker   *time.Ticker
	stopChan chan struct{}
}

// New creates a new scheduler
func New(cfg *config.Config) *Scheduler {
	return &Scheduler{
		config:   cfg,
		stopChan: make(chan struct{}),
	}
}

// Start begins the scheduler
func (s *Scheduler) Start(fn func()) {
	if s.config.Schedule != "" {
		s.startCron(fn)
	} else {
		s.startInterval(fn)
	}
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	close(s.stopChan)

	if s.cron != nil {
		s.cron.Stop()
	}

	if s.ticker != nil {
		s.ticker.Stop()
	}
}

// startCron starts cron-based scheduling
func (s *Scheduler) startCron(fn func()) {
	// Accept standard 5-field cron expressions (as documented) as well as the
	// 6-field with-seconds form. cron.WithSeconds() alone REQUIRES the seconds
	// field, which made every 5-field expression a startup crash.
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	s.cron = cron.New(cron.WithParser(parser))

	_, err := s.cron.AddFunc(s.config.Schedule, fn)
	if err != nil {
		log.Fatalf("Invalid cron schedule: %v", err)
	}

	log.Infof("Scheduled updates with cron expression: %s", s.config.Schedule)
	s.cron.Start()
}

// startInterval starts interval-based scheduling
func (s *Scheduler) startInterval(fn func()) {
	// Run immediately on start
	fn()

	interval := s.config.Interval
	if interval <= 0 {
		// time.NewTicker panics on non-positive durations, so a zero interval
		// (unset, or an unparseable value that fell through to zero) must not
		// reach it — fall back to the documented default instead of crashing.
		log.Warnf("Invalid update interval %s; falling back to 1m", interval)
		interval = time.Minute
	}
	s.ticker = time.NewTicker(interval)
	log.Infof("Scheduled updates every %s", interval)

	go func() {
		for {
			select {
			case <-s.ticker.C:
				fn()
			case <-s.stopChan:
				return
			}
		}
	}()
}
