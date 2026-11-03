package platformagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// logEntry is one run-log record in the shape TAA reports.
type logEntry struct {
	Seq     uint64 `json:"seq"`
	Message string `json:"message"`
}

// logRequest mirrors taa/internal/controller.taaLogRequest field for field,
// which is why the payload parses with the platform's existing decoder. The
// path is not TAA's: this is the LLM's own log channel.
//
// requestId is always "system" and taskId stays empty: the LLM has no task
// concept, which is exactly the shape TAA reports while idle.
type logRequest struct {
	DockerID  string     `json:"dockerId"`
	RequestID string     `json:"requestId"`
	TaskID    string     `json:"taskId,omitempty"`
	SeqStart  uint64     `json:"seqStart"`
	Entries   []logEntry `json:"entries"`
}

// Agent owns the three outbound reporting channels.
type Agent struct {
	cfg    Config
	logs   *LogBuffer
	client *http.Client

	mu      sync.Mutex
	pending []logEntry
	failing map[string]bool

	// registered is closed once the first registration succeeds; the heartbeat
	// loop waits on it so it never reports liveness for an instance the platform
	// does not know.
	registered chan struct{}
	// reregister carries "the platform became unreachable" from the probes to the
	// registration loop. Buffered with capacity 1: the signal means "register
	// again", so while one is already pending or being acted on, more add nothing.
	reregister chan struct{}
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	startOnce  sync.Once
	stopOnce   sync.Once
}

// New builds an Agent. A Config with an empty PlatformAddr yields (nil, nil):
// reporting is then disabled and the caller may still use its own LogBuffer.
func New(cfg Config) (*Agent, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.PlatformAddr) == "" {
		return nil, nil
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Snapshot == nil {
		return nil, fmt.Errorf("platformagent: snapshot source is required")
	}
	if cfg.Logs == nil {
		cfg.Logs = NewLogBuffer(cfg.LogBufferSize)
	}
	return &Agent{
		cfg:        cfg,
		logs:       cfg.Logs,
		client:     cfg.HTTPClient,
		failing:    make(map[string]bool),
		registered: make(chan struct{}),
		reregister: make(chan struct{}, 1),
	}, nil
}

// Logs exposes the shared buffer so the service can instrument its own paths.
func (a *Agent) Logs() *LogBuffer {
	if a == nil {
		return nil
	}
	return a.logs
}

// postJSON sends one plaintext JSON payload. There is no envelope in either
// direction on this channel; the channel's integrity is the deployment's
// problem, not this function's.
func (a *Agent) postJSON(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.PlatformAddr+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// reportFailure records a reporting failure. Both stderr and the platform log
// buffer get one entry per outage -- the transition into failure -- not one per
// attempt: these loops keep running for the life of the process, so an
// unconditional line would grow the guest's tmpfs for as long as the platform is
// unreachable. The registration ladder prints its own throttled progress line,
// because "still retrying" is the state a reader of that log wants to see.
func (a *Agent) reportFailure(channel string, err error) {
	a.mu.Lock()
	wasFailing := a.failing[channel]
	a.failing[channel] = true
	a.mu.Unlock()

	if !wasFailing {
		// Add mirrors to stderr as well, so this is the only call needed.
		a.logs.Add(LevelWarn, "platform", "%s reporting failed: %v", channel, err)

		// Losing the platform is what triggers a fresh registration. It is
		// signalled on the transition only: during one outage both the heartbeat
		// and the log loop would otherwise raise it on every period, and the
		// registration loop is already retrying on its own ladder.
		//
		// A platform that answers with an error status is reachable and still
		// holds a record, so an HTTP-level rejection deliberately does not
		// trigger re-registration. Two more cases are excluded: the register
		// channel is already retrying on its own ladder, and before the first
		// registration there is nothing to re-register -- either would only
		// produce a duplicate registration once the ladder succeeds.
		if channel != "register" && a.hasRegistered() && isConnectivityError(err) {
			select {
			case a.reregister <- struct{}{}:
			default: // a signal is already pending; one re-registration covers it
			}
		}
	}
}

// hasRegistered reports whether the first registration has completed. Before
// that, the registration loop is already retrying on its own ladder and a
// wake-up signal would only cause a duplicate registration.
func (a *Agent) hasRegistered() bool {
	select {
	case <-a.registered:
		return true
	default:
		return false
	}
}

// isConnectivityError reports whether err means the platform could not be
// reached at all, as opposed to reached and rejected.
func isConnectivityError(err error) bool {
	if err == nil {
		return false
	}
	// http.Client wraps every transport failure in *url.Error.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isConnectivityError(urlErr.Err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// Dial failures (refused, host unreachable, network unreachable) and resets
	// in the middle of a request surface as *net.OpError.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// Client.Timeout surfaces as an unexported net.Error, not as *net.OpError.
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// reportSuccess records the recovery transition, so the platform sees both ends
// of an outage.
func (a *Agent) reportSuccess(channel string) {
	a.mu.Lock()
	wasFailing := a.failing[channel]
	delete(a.failing, channel)
	a.mu.Unlock()

	if wasFailing {
		a.logs.Add(LevelInfo, "platform", "%s reporting recovered", channel)
	}
}

// pendingCount reports the size of the unsent log queue.
func (a *Agent) pendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// flushLogs moves the ring into the pending queue and pushes it in batches.
// A failed batch stays at the head of the queue, so ordering across retries is
// preserved and the platform never sees a seq go backwards.
func (a *Agent) flushLogs(ctx context.Context) {
	a.mu.Lock()
	for _, entry := range a.logs.Drain() {
		a.pending = append(a.pending, logEntry{Seq: entry.Seq, Message: entry.FormatMessage()})
	}
	// Cap the queue as well as the ring: with the platform down for a long time
	// the ring alone would not bound what is waiting to be sent.
	if over := len(a.pending) - a.cfg.LogBufferSize; over > 0 {
		a.pending = append([]logEntry(nil), a.pending[over:]...)
	}
	a.mu.Unlock()

	for {
		a.mu.Lock()
		if len(a.pending) == 0 {
			a.mu.Unlock()
			return
		}
		size := len(a.pending)
		if size > DefaultMaxLogBatchSize {
			size = DefaultMaxLogBatchSize
		}
		batch := append([]logEntry(nil), a.pending[:size]...)
		a.mu.Unlock()

		err := a.postJSON(ctx, LogEndpoint, logRequest{
			DockerID:  a.cfg.DockerID,
			RequestID: "system",
			SeqStart:  batch[0].Seq,
			Entries:   batch,
		})
		if err != nil {
			a.reportFailure("log", err)
			return
		}
		a.reportSuccess("log")

		a.mu.Lock()
		if len(a.pending) >= size {
			a.pending = append([]logEntry(nil), a.pending[size:]...)
		}
		a.mu.Unlock()
	}
}

// Start launches the registration, heartbeat and log-flush loops. It is safe to
// call once; a disabled agent (nil) is a no-op.
func (a *Agent) Start(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		a.cancel = cancel
		a.wg.Add(3)
		go a.registerLoop(runCtx)
		go a.heartbeatLoop(runCtx)
		go a.logLoop(runCtx)
	})
}

// Stop cancels the loops, waits for them, and makes one last best-effort flush
// so records produced during shutdown still reach the platform.
func (a *Agent) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.wg.Wait()

		flushCtx, cancel := context.WithTimeout(context.Background(), DefaultRequestTimeout)
		defer cancel()
		a.flushLogs(flushCtx)
	})
}

// registerLoop registers, retrying for as long as it takes, and then registers
// again whenever a probe reports that the platform became unreachable.
func (a *Agent) registerLoop(ctx context.Context) {
	defer a.wg.Done()

	if err := a.registerUntilSuccess(ctx); err != nil {
		return
	}
	a.logs.Add(LevelInfo, "platform", "registered with %s as %s", a.cfg.PlatformAddr, a.cfg.DockerID)
	close(a.registered)

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.reregister:
			// A reachability failure means the platform may have restarted or
			// lost the record, and the certificate this process presents may have
			// rotated since the last registration. Register again; the ladder
			// keeps trying until it sticks.
			a.logs.Add(LevelInfo, "platform", "platform became unreachable, registering again")
			if err := a.registerUntilSuccess(ctx); err != nil {
				return
			}
			a.logs.Add(LevelInfo, "platform", "re-registered with %s as %s", a.cfg.PlatformAddr, a.cfg.DockerID)
		}
	}
}

// registerUntilSuccess attempts registration on the retry ladder until it
// succeeds or ctx is cancelled. It returns nil only on success.
//
// The ladder has no give-up window. Registration is what makes this instance
// exist on the platform: a bounded window would mean that any platform outage
// longer than the window leaves a perfectly healthy inference service
// permanently invisible -- no record, heartbeats blocked on the registration
// signal, logs undeliverable -- with no way back short of restarting the pod.
// TAA bounds its own retry for a different reason (its log lives on the guest's
// tmpfs and every attempt carries the full attestation payload); here the
// payload is small and the logging is throttled, so nothing accumulates.
func (a *Agent) registerUntilSuccess(ctx context.Context) error {
	delay := registerRetryInitialDelay
	for attempt := 1; ; attempt++ {
		err := a.registerOnce(ctx)
		if err == nil {
			a.reportSuccess("register")
			return nil
		}
		a.reportFailure("register", err)
		if attempt == 1 || attempt%registerFailureLogEvery == 0 {
			log.Printf("[WARN] [platform] register attempt %d failed, retrying in %s: %v", attempt, delay, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > registerRetryMaxDelay {
			delay = registerRetryMaxDelay
		}
	}
}

// heartbeatLoop starts only after registration, so a heartbeat always refers to
// an instance the platform already knows.
func (a *Agent) heartbeatLoop(ctx context.Context) {
	defer a.wg.Done()

	select {
	case <-ctx.Done():
		return
	case <-a.registered:
	}

	if err := a.sendHeartbeat(ctx); err != nil {
		a.reportFailure("heartbeat", err)
	} else {
		a.reportSuccess("heartbeat")
	}

	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.sendHeartbeat(ctx); err != nil {
				a.reportFailure("heartbeat", err)
			} else {
				a.reportSuccess("heartbeat")
			}
		}
	}
}

// logLoop pushes the ring on a fixed period. It is independent of registration:
// logs are worth keeping even while the registration is still being retried.
func (a *Agent) logLoop(ctx context.Context) {
	defer a.wg.Done()

	ticker := time.NewTicker(a.cfg.LogFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.flushLogs(ctx)
		}
	}
}
