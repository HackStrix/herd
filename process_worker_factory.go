// factory.go — WorkerFactory implementations.
//
// # What lives here
//
//   - ProcessFactory: the default factory. Spawns any OS binary, assigns it
//     an OS-allocated TCP port, and polls GET <address>/health until 200 OK.
//     Users pass this to New[C] so they never have to implement WorkerFactory
//     themselves for the common HTTP case.
//
// # Port allocation
//
// Ports are assigned by the OS (net.Listen("tcp", "127.0.0.1:0")).
// The binary receives its port via the PORT environment variable AND via
// any arg that contains the literal string "{{.Port}}" — that token is
// replaced with the actual port number at spawn time.
//
// # Health polling
//
// After the process starts, ProcessFactory polls GET <address>/health
// every 200ms, up to 30 attempts (6 seconds total). If the worker never
// responds with 200 OK, Spawn returns an error and kills the process.
// The concrete port + binary are logged at startup.
//
// # Crash monitoring
//
// A background goroutine calls cmd.Wait(). On exit, if the worker still
// holds a sessionID the pool's onCrash callback is invoked so the session
// affinity map is cleaned up.
package herd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrWorkerDead = errors.New("worker process has died")

// ---------------------------------------------------------------------------
// processWorker — concrete Worker[*http.Client] backed by exec.Cmd
// ---------------------------------------------------------------------------

// processWorker implements Worker[*http.Client].
// It is the value returned by ProcessFactory.Spawn.
type processWorker struct {
	id         string
	port       int
	address    string // "http://127.0.0.1:<port>"
	healthPath string // e.g. "/health" or "/"
	client     *http.Client

	cgroupHandle sandboxHandle

	mu        sync.Mutex
	cmd       *exec.Cmd
	sessionID string // guarded by mu

	// draining is set to 1 atomically before Kill so that monitor() does not
	// attempt a restart after the process exits.
	draining atomic.Int32

	// onCrash is wired up by the pool after Spawn returns.
	// Called with the sessionID when the process exits unexpectedly.
	onCrash func(sessionID string)

	// dead is closed when the process exits.
	// or when the worker is explicitly killed.
	// all the others can listen to this channel to know when the worker is dead.
	dead chan struct{}
}

func (w *processWorker) ID() string           { return w.id }
func (w *processWorker) Address() string      { return w.address }
func (w *processWorker) Client() *http.Client { return w.client }

// Healthy performs a GET <address><healthPath> and returns nil on 200 OK.
// ctx controls the timeout of this single request.
func (w *processWorker) Healthy(ctx context.Context) error {

	select {
	case <-w.dead:
		return ErrWorkerDead
	default:
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.address+w.healthPath, nil)
	if err != nil {
		return err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		log.Println("health: unexpected error making request", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Println("health: unexpected status", resp.StatusCode)
		return fmt.Errorf("health: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Close drains and kills the subprocess.
// After Close returns, the process is guaranteed to be gone.
func (w *processWorker) Close() error {
	w.draining.Store(1)
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// monitor waits for the subprocess to exit and fires onCrash if the worker
// still had an active session. It does NOT restart the process — restart
// is the pool's responsibility (via the pool's available channel + factory).
func (w *processWorker) monitor() {
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()

	_ = cmd.Wait() // blocks until process exits

	// broadcast to all the listeners that the worker is dead.
	close(w.dead)
	if w.cgroupHandle != nil {
		w.cgroupHandle.Cleanup()
	}

	w.mu.Lock()
	prevSession := w.sessionID
	w.sessionID = ""
	w.mu.Unlock()

	if prevSession != "" && w.draining.Load() == 0 {
		log.Printf("[worker %s] crashed with active session %s", w.id, prevSession)
		if w.onCrash != nil {
			w.onCrash(prevSession)
		}
	}
}

// ---------------------------------------------------------------------------
// ProcessFactory
// ---------------------------------------------------------------------------

// ProcessFactory is the default WorkerFactory[*http.Client].
// It spawns `binary` as a subprocess, allocates a free OS port, and polls
// GET <address>/health until the worker reports healthy.
//
// Use NewProcessFactory to create one; pass it directly to New[C]:
//
//	pool, err := herd.New(herd.NewProcessFactory("./my-binary", "--port", "{{.Port}}"))
type ProcessFactory struct {
	binary                string
	args                  []string      // may contain "{{.Port}}" — replaced at spawn time
	extraEnv              []string      // additional KEY=VALUE env vars; "{{.Port}}" is replaced here too
	healthPath            string        // path to poll for liveness; defaults to "/health"
	startTimeout          time.Duration // maximum time to wait for the first successful health check
	startHealthCheckDelay time.Duration // delay the health check for the first time.
	enableSandbox         bool          // true by default for isolation
	namespaceCloneFlags   uintptr       // Linux namespaces to enable for sandboxed workers
	cgroupMemory          int64         // bytes; 0 means unlimited
	cgroupCPU             int64         // quota in micros per 100ms period; 0 means unlimited
	cgroupPIDs            int64         // max pids; -1 means unlimited
	counter               atomic.Int64
}

// NewProcessFactory returns a ProcessFactory that spawns the given binary.
//
// Any arg containing the literal string "{{.Port}}" is replaced with the
// OS-assigned port number at spawn time. The port is also injected via the
// PORT environment variable for binaries that prefer env-based config.
//
//	factory := herd.NewProcessFactory("./ollama", "serve", "--port", "{{.Port}}")
func NewProcessFactory(binary string, args ...string) *ProcessFactory {
	return &ProcessFactory{
		binary:                binary,
		args:                  args,
		healthPath:            "/health",
		startTimeout:          30 * time.Second,
		startHealthCheckDelay: 1 * time.Second,
		enableSandbox:         true,
		namespaceCloneFlags:   defaultNamespaceCloneFlags(),
		cgroupPIDs:            100,
	}
}

// WithEnv appends an extra KEY=VALUE environment variable that is injected
// into every worker spawned by this factory. The literal string "{{.Port}}"
// is replaced with the worker's allocated port number, which is useful for
// binaries that accept the listen address via an env var rather than a flag.
//
//	factory := herd.NewProcessFactory("ollama", "serve").
//		WithEnv("OLLAMA_HOST=127.0.0.1:{{.Port}}").
//		WithEnv("OLLAMA_MODELS=/tmp/shared-ollama-models")
func (f *ProcessFactory) WithEnv(kv string) *ProcessFactory {
	f.extraEnv = append(f.extraEnv, kv)
	return f
}

// WithHealthPath sets the HTTP path that herd polls to decide whether a worker
// is ready. The path must return HTTP 200 when the process is healthy.
//
// Default: "/health"
//
// Use this for binaries that expose liveness on a non-standard path:
//
//	factory := herd.NewProcessFactory("ollama", "serve").
//		WithHealthPath("/")   // ollama serves GET / → 200 "Ollama is running"
func (f *ProcessFactory) WithHealthPath(path string) *ProcessFactory {
	f.healthPath = path
	return f
}

// WithStartTimeout sets the maximum duration herd will poll the worker's
// health endpoint after spawning the process before giving up and killing it.
//
// Default: 30 seconds
func (f *ProcessFactory) WithStartTimeout(d time.Duration) *ProcessFactory {
	f.startTimeout = d
	return f
}

// WithStartHealthCheckDelay delay the health check for the first time.
// let the process start and breath before hammering with health checks
func (f *ProcessFactory) WithStartHealthCheckDelay(d time.Duration) *ProcessFactory {
	f.startHealthCheckDelay = d
	return f
}

// WithMemoryLimit sets the cgroup memory limit, in bytes, for each spawned worker.
// A value of 0 disables the memory limit.
func (f *ProcessFactory) WithMemoryLimit(bytes int64) *ProcessFactory {
	if bytes < 0 {
		panic("herd: WithMemoryLimit bytes must be >= 0")
	}
	f.cgroupMemory = bytes
	return f
}

// WithCPULimit sets the cgroup CPU quota in cores for each spawned worker.
// For example, 0.5 means half a CPU and 2 means two CPUs. A value of 0 disables the limit.
func (f *ProcessFactory) WithCPULimit(cores float64) *ProcessFactory {
	if cores < 0 {
		panic("herd: WithCPULimit cores must be >= 0")
	}
	if cores == 0 {
		f.cgroupCPU = 0
		return f
	}
	f.cgroupCPU = int64(cores * 100_000)
	return f
}

// WithPIDsLimit sets the cgroup PID limit for each spawned worker.
// Pass -1 for unlimited. Values of 0 or less than -1 are invalid.
func (f *ProcessFactory) WithPIDsLimit(n int64) *ProcessFactory {
	if n == 0 || n < -1 {
		panic("herd: WithPIDsLimit n must be > 0 or -1 for unlimited")
	}
	f.cgroupPIDs = n
	return f
}

// WithInsecureSandbox disables the namespace/cgroup sandbox.
// Use only for local debugging on non-Linux systems or when you explicitly
// trust the spawned processes.
func (f *ProcessFactory) WithInsecureSandbox() *ProcessFactory {
	f.enableSandbox = false
	return f
}

func streamLogs(workerID string, pipe io.ReadCloser, isError bool) {
	// bufio.Scanner guarantees we read line-by-line, preventing torn logs.
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()
		// Route this to your actual logger (e.g., slog, zap, or standard log)
		if isError {
			log.Printf("[worker:%s] STDERR: %s", workerID, line)
		} else {
			log.Printf("[worker:%s] STDOUT: %s", workerID, line)
		}
	}
}

// Spawn implements WorkerFactory[*http.Client].
// It allocates a free port, starts the binary, and blocks until the worker
// passes a /health check or ctx is cancelled.
func (f *ProcessFactory) Spawn(ctx context.Context) (Worker[*http.Client], error) {
	port, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("herd: ProcessFactory: find free port: %w", err)
	}

	id := fmt.Sprintf("worker-%d", f.counter.Add(1))
	address := fmt.Sprintf("http://127.0.0.1:%d", port)
	portStr := fmt.Sprintf("%d", port)

	// Substitute {{.Port}} in args
	resolvedArgs := make([]string, len(f.args))
	for i, a := range f.args {
		resolvedArgs[i] = strings.ReplaceAll(a, "{{.Port}}", portStr)
	}

	// Substitute {{.Port}} in extra env vars
	resolvedEnv := make([]string, len(f.extraEnv))
	for i, e := range f.extraEnv {
		resolvedEnv[i] = strings.ReplaceAll(e, "{{.Port}}", portStr)
	}

	// During program exits, this should be cleaned up by the Shutdown method
	cmd := exec.Command(f.binary, resolvedArgs...)
	cmd.Env = append(os.Environ(), append([]string{"PORT=" + portStr}, resolvedEnv...)...)
	var cgroupHandle sandboxHandle

	if f.enableSandbox {
		h, err := applySandboxFlags(cmd, id, sandboxConfig{
			memoryMaxBytes: f.cgroupMemory,
			cpuMaxMicros:   f.cgroupCPU,
			pidsMax:        f.cgroupPIDs,
			cloneFlags:     f.namespaceCloneFlags,
		})
		if err != nil {
			return nil, fmt.Errorf("herd: ProcessFactory: failed to apply sandbox: %w", err)
		}
		cgroupHandle = h
	} else {
		log.Printf("[%s] WARNING: running UN-SANDBOXED. Not recommended for production.", id)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("herd: ProcessFactory: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("herd: ProcessFactory: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("herd: ProcessFactory: start %s: %w", f.binary, err)
	}
	if cgroupHandle != nil {
		cgroupHandle.PostStart()
	}
	log.Printf("[%s] started pid=%d addr=%s", id, cmd.Process.Pid, address)

	// Stream logs in background
	go streamLogs(id, stdout, false)
	go streamLogs(id, stderr, true)

	w := &processWorker{
		id:           id,
		port:         port,
		address:      address,
		healthPath:   f.healthPath,
		client:       &http.Client{Timeout: 3 * time.Second},
		cgroupHandle: cgroupHandle,
		cmd:          cmd,
		dead:         make(chan struct{}),
	}

	// Monitor the process in background — fires onCrash if it exits unexpectedly
	go w.monitor()

	// wait for start health check delay
	time.Sleep(f.startHealthCheckDelay)

	// Poll /health until the worker is ready or ctx expires
	waitCtx, cancel := context.WithTimeout(ctx, f.startTimeout)
	defer cancel()
	if err := waitForHealthy(waitCtx, w); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("herd: ProcessFactory: %s never became healthy: %w", id, err)
	}

	log.Printf("[%s] ready", id)
	return w, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitForHealthy polls w.Healthy every 200ms until it returns nil or ctx
// is cancelled.
func waitForHealthy(ctx context.Context, w Worker[*http.Client]) error {
	const pollInterval = 200 * time.Millisecond

	for {
		hCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		err := w.Healthy(hCtx)
		cancel()

		if err == nil {
			return nil
		}

		// THE FIX: If the process is dead, stop polling immediately.
		if errors.Is(err, ErrWorkerDead) {
			return fmt.Errorf("aborted health check: %w", err)
		}

		// Check parent context before sleeping
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// findFreePort asks the OS for an available TCP port by binding to :0.
// This is the same technique used by the steel-orchestrator.
func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
