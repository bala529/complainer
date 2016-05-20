package monitor

import (
	"fmt"
	"log"
	"time"

	"github.com/cloudflare/complainer"
	"github.com/cloudflare/complainer/label"
	"github.com/cloudflare/complainer/mesos"
	"github.com/cloudflare/complainer/reporter"
	"github.com/cloudflare/complainer/uploader"
)

const (
	// DefaultName is the default name of the complainer instance
	DefaultName = "default"
	// timeout before purging old seen tasks
	timeout = time.Minute
)

// Monitor is responsible for routing failed tasks to the configured reporters
type Monitor struct {
	name      string
	mesos     *mesos.Cluster
	uploader  uploader.Uploader
	reporters map[string]reporter.Reporter
	recent    map[string]time.Time
}

// NewMonitor creates the new monitor with a name, uploader and reporters
func NewMonitor(name string, cluster *mesos.Cluster, up uploader.Uploader, reporters map[string]reporter.Reporter) *Monitor {
	return &Monitor{
		name:      name,
		mesos:     cluster,
		uploader:  up,
		reporters: reporters,
	}
}

// Run does one run across failed tasks and reports any new failures
func (m *Monitor) Run() error {
	failures, err := m.mesos.Failures()
	if err != nil {
		return err
	}

	first := false
	if m.recent == nil {
		m.recent = map[string]time.Time{}
		first = true
	}

	for _, failure := range failures {
		if m.checkFailure(failure, first) {
			if err := m.processFailure(failure); err != nil {
				log.Printf("Error reporting failure of %s: %s", failure.ID, err)
			}
		}
	}

	m.cleanupRecent()

	return nil
}

func (m *Monitor) cleanupRecent() {
	for n, ts := range m.recent {
		if time.Since(ts) > timeout {
			delete(m.recent, n)
		}
	}
}

func (m *Monitor) checkFailure(failure complainer.Failure, first bool) bool {
	if !m.recent[failure.ID].IsZero() {
		return false
	}

	m.recent[failure.ID] = failure.Finished

	if time.Since(failure.Finished) > timeout/2 {
		return false
	}

	if first {
		return false
	}

	return true
}

func (m *Monitor) processFailure(failure complainer.Failure) error {
	log.Printf("Reporting %s", failure)

	stdoutURL, stderrURL, err := m.mesos.Logs(failure)
	if err != nil {
		return fmt.Errorf("cannot get stdout and stderr urls from mesos: %s", err)
	}

	stdoutURL, stderrURL, err = m.uploader.Upload(failure, stdoutURL, stderrURL)
	if err != nil {
		return fmt.Errorf("cannot get stdout and stderr urls from uploader: %s", err)
	}

	labels := label.NewLabels(m.name, failure.Labels)
	for n, r := range m.reporters {
		for _, i := range labels.Instances(n) {
			config := reporter.NewConfigProvider(labels, n, i)
			if err := r.Report(failure, config, stdoutURL, stderrURL); err != nil {
				log.Printf("Cannot generate report with %s [instance=%s] for task with ID %s: %s", n, i, failure.ID, err)
			}
		}
	}

	return nil
}