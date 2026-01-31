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
	s.cron = cron.New(cron.WithSeconds())

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

	s.ticker = time.NewTicker(s.config.Interval)
	log.Infof("Scheduled updates every %s", s.config.Interval)

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
