package pluginhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxFrameBytes = 4 * 1024 * 1024
	defaultCallTimeout   = 10 * time.Second
	defaultStartTimeout  = 5 * time.Second
	defaultStopTimeout   = 3 * time.Second

	// EnvNodePath and EnvScriptPath are the npm/source-channel handoff. The npm
	// launcher supplies its own process.execPath and packaged host asset. Native
	// applications should normally construct Config from signed bundle-relative
	// paths directly, but may use the same variables during packaging smoke tests.
	EnvNodePath   = "ANYPICK_PLUGIN_HOST_NODE"
	EnvScriptPath = "ANYPICK_PLUGIN_HOST_SCRIPT"
)

var (
	ErrHostClosed  = errors.New("pluginhost: host is closed")
	ErrHostCrashed = errors.New("pluginhost: host crashed")
	ErrProtocol    = errors.New("pluginhost: protocol error")

	// ErrLocalFrameRejected reports that an outbound frame was refused before
	// any byte was written: encode failure or size cap. This is not a host
	// loss and must not be classified as one — the request never left.
	ErrLocalFrameRejected = errors.New("pluginhost: local frame rejected")

	// ErrNotDispatched reports that a request frame provably never left this
	// process: the host was already gone at call entry, or the pipe write
	// accepted zero bytes. Recovery may replay such a call on a replacement
	// runtime; an ambiguous loss (crash after dispatch, or a write that
	// accepted any byte) must never be treated as not-dispatched, so this
	// sentinel is deliberately absent from the HOST_CLOSED pending-call
	// resolution path.
	ErrNotDispatched = errors.New("pluginhost: request was not dispatched")

	// ErrWriteInterrupted reports that at least one byte of a request frame
	// was accepted by the writer and then the write failed. The host may
	// already be running the method; recovery must not replay it.
	ErrWriteInterrupted = errors.New("pluginhost: outbound write was interrupted")
)

// Config resolves only explicit, absolute assets. Production callers point
// NodePath at the signed private runtime and ScriptPath at its bundled host
// script; PATH lookup is deliberately outside this package.
type Config struct {
	NodePath        string
	ScriptPath      string
	MaxFrameBytes   int
	MaxOldSpaceMB   int
	StartupTimeout  time.Duration
	CallTimeout     time.Duration
	ShutdownTimeout time.Duration
	ExtraEnv        []string
	// Stderr receives already-sanitized, single-line diagnostics. It must not
	// assume plugin output is trustworthy.
	Stderr func(string)
}

// ConfigFromEnv resolves the explicit npm/source-channel host handoff. It does
// not consult PATH: absence is a degraded "runtime unavailable" condition for
// the caller, never permission to execute a different system Node binary.
func ConfigFromEnv() (Config, error) {
	config := Config{
		NodePath:   os.Getenv(EnvNodePath),
		ScriptPath: os.Getenv(EnvScriptPath),
	}
	if config.NodePath == "" || config.ScriptPath == "" {
		return Config{}, fmt.Errorf(
			"pluginhost: %s and %s must name explicit runtime assets",
			EnvNodePath,
			EnvScriptPath,
		)
	}
	config = withDefaults(config)
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

type responseFrame struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RemoteError    `json:"error,omitempty"`
	Settled *bool           `json:"settled,omitempty"`
}

type pendingCall struct {
	response chan responseFrame
}

// Host owns one Node process and multiplexes explicit request IDs over its
// bounded stdio protocol.
type Host struct {
	config Config
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	// generation is an engine-owned identity for this exact supervised Node
	// process. PID alone is not a runtime generation because it can be reused.
	generation string

	writeMu sync.Mutex
	// testWriter, when set, replaces stdin for outbound frames so tests can
	// inject partial-write failures. Handshake still uses the real pipe.
	testWriter io.Writer
	mu         sync.Mutex
	pending    map[string]*pendingCall
	ignored    map[string]struct{}
	closing    bool
	closed     bool
	endErr     error
	done       chan struct{}

	nextID atomic.Uint64
}

// Start launches and handshakes the host. Startup failure is returned to the
// caller without affecting built-in registries or the Go engine process.
func Start(ctx context.Context, config Config) (*Host, error) {
	config = withDefaults(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	args := []string{fmt.Sprintf("--max-old-space-size=%d", config.MaxOldSpaceMB), config.ScriptPath}
	cmd := exec.Command(config.NodePath, args...)
	cmd.Env = append(os.Environ(), config.ExtraEnv...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("ANYPICK_PLUGIN_HOST_MAX_FRAME_BYTES=%d", config.MaxFrameBytes))
	setHostGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginhost: create stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginhost: create stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginhost: create stderr: %w", err)
	}

	host := &Host{
		config:  config,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]*pendingCall),
		ignored: make(map[string]struct{}),
		done:    make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pluginhost: start private Node runtime: %w", err)
	}
	host.generation = fmt.Sprintf("%d-%d", cmd.Process.Pid, time.Now().UnixNano())
	go host.readStderr(stderr)

	scanner := boundedScanner(stdout, config.MaxFrameBytes)
	handshake := make(chan error, 1)
	go func() { handshake <- host.performHandshake(scanner) }()
	startCtx, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	select {
	case err := <-handshake:
		if err != nil {
			_ = killHostGroup(cmd.Process)
			_ = cmd.Wait()
			return nil, err
		}
	case <-startCtx.Done():
		_ = killHostGroup(cmd.Process)
		_ = cmd.Wait()
		return nil, fmt.Errorf("pluginhost: handshake: %w", startCtx.Err())
	}

	go host.readLoop(scanner)
	go func() {
		err := cmd.Wait()
		host.processEnded(err)
	}()
	return host, nil
}

func withDefaults(config Config) Config {
	if config.MaxFrameBytes == 0 {
		config.MaxFrameBytes = defaultMaxFrameBytes
	}
	if config.MaxOldSpaceMB == 0 {
		config.MaxOldSpaceMB = 128
	}
	if config.StartupTimeout == 0 {
		config.StartupTimeout = defaultStartTimeout
	}
	if config.CallTimeout == 0 {
		config.CallTimeout = defaultCallTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultStopTimeout
	}
	return config
}

func validateConfig(config Config) error {
	if !filepath.IsAbs(config.NodePath) {
		return errors.New("pluginhost: NodePath must be an absolute private-runtime path")
	}
	if !filepath.IsAbs(config.ScriptPath) {
		return errors.New("pluginhost: ScriptPath must be an absolute bundle-relative asset path")
	}
	for label, path := range map[string]string{"NodePath": config.NodePath, "ScriptPath": config.ScriptPath} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("pluginhost: %s: %w", label, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("pluginhost: %s is not a regular file", label)
		}
	}
	if config.MaxFrameBytes < 4096 || config.MaxFrameBytes > 16*1024*1024 {
		return errors.New("pluginhost: MaxFrameBytes must be between 4096 and 16777216")
	}
	if config.MaxOldSpaceMB < 16 || config.MaxOldSpaceMB > 2048 {
		return errors.New("pluginhost: MaxOldSpaceMB must be between 16 and 2048")
	}
	if config.StartupTimeout <= 0 || config.CallTimeout <= 0 || config.ShutdownTimeout <= 0 {
		return errors.New("pluginhost: timeouts must be positive")
	}
	return nil
}

func boundedScanner(reader io.Reader, maximum int) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximum+1)
	return scanner
}

func (h *Host) performHandshake(scanner *bufio.Scanner) error {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("pluginhost: read hello: %w", err)
		}
		return fmt.Errorf("pluginhost: read hello: %w", io.ErrUnexpectedEOF)
	}
	var hello struct {
		Version          int      `json:"version"`
		Type             string   `json:"type"`
		ProtocolVersion  int      `json:"protocolVersion"`
		PluginAPIVersion int      `json:"pluginApiVersion"`
		MaxFrameBytes    int      `json:"maxFrameBytes"`
		Capabilities     []string `json:"capabilities"`
	}
	if err := decodeStrict(scanner.Bytes(), &hello); err != nil {
		return fmt.Errorf("%w: invalid hello: %v", ErrProtocol, err)
	}
	if hello.Version != ProtocolVersion || hello.Type != "hello" || hello.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: incompatible host protocol", ErrProtocol)
	}
	if hello.PluginAPIVersion != PluginAPIVersion {
		return fmt.Errorf("%w: host plugin API %d, engine requires %d", ErrProtocol, hello.PluginAPIVersion, PluginAPIVersion)
	}
	if hello.MaxFrameBytes != h.config.MaxFrameBytes {
		return fmt.Errorf("%w: max-frame negotiation mismatch", ErrProtocol)
	}
	required := []string{
		"activate",
		"cancel",
		"dispose",
		"invoke",
		"invoke-abort-signal",
		"retain-object-results",
		"reverse-disposal",
		"settled-acknowledgement",
	}
	for _, capability := range required {
		if !contains(hello.Capabilities, capability) {
			return fmt.Errorf("%w: host lacks %q capability", ErrProtocol, capability)
		}
	}
	if err := h.writeFrame(map[string]any{
		"version":         ProtocolVersion,
		"type":            "hello",
		"protocolVersion": ProtocolVersion,
		"client":          "anypick-go",
	}); err != nil {
		return fmt.Errorf("pluginhost: send hello: %w", err)
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("pluginhost: read ready: %w", err)
		}
		return fmt.Errorf("pluginhost: read ready: %w", io.ErrUnexpectedEOF)
	}
	var ready struct {
		Version         int    `json:"version"`
		Type            string `json:"type"`
		ProtocolVersion int    `json:"protocolVersion"`
		MaxFrameBytes   int    `json:"maxFrameBytes"`
	}
	if err := decodeStrict(scanner.Bytes(), &ready); err != nil {
		return fmt.Errorf("%w: invalid ready: %v", ErrProtocol, err)
	}
	if ready.Version != ProtocolVersion || ready.Type != "ready" ||
		ready.ProtocolVersion != ProtocolVersion || ready.MaxFrameBytes != h.config.MaxFrameBytes {
		return fmt.Errorf("%w: host rejected negotiation", ErrProtocol)
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeStrict(line []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func (h *Host) readLoop(scanner *bufio.Scanner) {
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var response responseFrame
		if err := decodeStrict(line, &response); err != nil {
			h.protocolFailed(fmt.Errorf("%w: invalid response: %v", ErrProtocol, err))
			return
		}
		if response.Version != ProtocolVersion || response.Type != "response" || response.ID == "" {
			h.protocolFailed(fmt.Errorf("%w: unexpected post-handshake frame", ErrProtocol))
			return
		}
		if (len(response.Result) == 0) == (response.Error == nil) {
			h.protocolFailed(fmt.Errorf("%w: response must contain exactly one of result or error", ErrProtocol))
			return
		}
		// The handshake requires the settled-acknowledgement capability, so
		// every response frame must carry the settlement flag. A frame without
		// one would let an unsettled coroutine masquerade as finished; reject
		// it at the protocol boundary instead of downstream.
		if response.Settled == nil {
			h.protocolFailed(fmt.Errorf("%w: response %q is missing the required settled flag", ErrProtocol, response.ID))
			return
		}
		h.mu.Lock()
		pending := h.pending[response.ID]
		_, ignored := h.ignored[response.ID]
		if pending != nil {
			delete(h.pending, response.ID)
		} else if ignored {
			delete(h.ignored, response.ID)
		}
		h.mu.Unlock()
		if pending != nil {
			pending.response <- response
		} else if !ignored {
			h.protocolFailed(fmt.Errorf("%w: response has unknown request id %q", ErrProtocol, response.ID))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		h.protocolFailed(fmt.Errorf("%w: read stdout: %v", ErrProtocol, err))
		return
	}
	// cmd.Wait supplies the process exit detail and closes pending calls.
}

func (h *Host) protocolFailed(err error) {
	h.mu.Lock()
	alreadyClosed := h.closed
	h.mu.Unlock()
	if alreadyClosed {
		return
	}
	_ = killHostGroup(h.cmd.Process)
	h.finish(err)
}

func (h *Host) processEnded(waitErr error) {
	h.mu.Lock()
	closing := h.closing
	h.mu.Unlock()
	if closing && waitErr == nil {
		h.finish(nil)
		return
	}
	if waitErr == nil {
		h.finish(fmt.Errorf("%w: unexpected clean exit", ErrHostCrashed))
		return
	}
	h.finish(fmt.Errorf("%w: %v", ErrHostCrashed, waitErr))
}

func isSettledResponse(r responseFrame) bool {
	if r.Settled == nil {
		return false
	}
	return *r.Settled
}

func (h *Host) finish(err error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.endErr = err
	if h.endErr == nil && !h.closing {
		h.endErr = ErrHostClosed
	}
	pending := h.pending
	h.pending = make(map[string]*pendingCall)
	h.ignored = make(map[string]struct{})
	close(h.done)
	h.mu.Unlock()

	for _, call := range pending {
		remote := &RemoteError{Code: "HOST_CLOSED", Message: ErrHostClosed.Error()}
		if err != nil {
			remote.Message = err.Error()
		}
		settled := false
		call.response <- responseFrame{Version: ProtocolVersion, Type: "response", Error: remote, Settled: &settled}
	}
}

func (h *Host) readStderr(reader io.Reader) {
	buffered := bufio.NewReaderSize(reader, 4096)
	line := make([]byte, 0, 4096)
	truncated := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		if remaining := 4096 - len(line); remaining > 0 {
			if len(fragment) > remaining {
				line = append(line, fragment[:remaining]...)
				truncated = true
			} else {
				line = append(line, fragment...)
			}
		} else if len(fragment) > 0 {
			truncated = true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 {
			value := sanitizeLine(string(line))
			if truncated {
				value = strings.TrimSpace(value) + "…"
			}
			if value != "" && h.config.Stderr != nil {
				h.config.Stderr(value)
			}
		}
		line = line[:0]
		truncated = false
		if err != nil {
			return
		}
	}
}

func sanitizeLine(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return '\ufffd'
	}, value)
	if len(value) > 4096 {
		value = value[:4096]
	}
	return strings.TrimSpace(value)
}

type writeKind int

const (
	writeOK writeKind = iota
	writeLocal
	writeNone
	writePartial
)

func (h *Host) writeFrame(frame any) error {
	_, err := h.writeFrameState(frame)
	return err
}

func (h *Host) writeFrameState(frame any) (writeKind, error) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return writeLocal, fmt.Errorf("%w: encode outbound frame: %v", ErrLocalFrameRejected, err)
	}
	if len(encoded)+1 > h.config.MaxFrameBytes {
		return writeLocal, fmt.Errorf("%w: outbound frame exceeds %d bytes", ErrLocalFrameRejected, h.config.MaxFrameBytes)
	}
	encoded = append(encoded, '\n')
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	writer := io.Writer(h.stdin)
	if h.testWriter != nil {
		writer = h.testWriter
	}
	accepted := 0
	for len(encoded) > 0 {
		var written int
		written, err = writer.Write(encoded)
		if written > 0 {
			accepted += written
			if written > len(encoded) {
				written = len(encoded)
			}
			encoded = encoded[written:]
		}
		if err != nil {
			if accepted == 0 {
				return writeNone, err
			}
			return writePartial, err
		}
		if written == 0 {
			if accepted == 0 {
				return writeNone, io.ErrShortWrite
			}
			return writePartial, io.ErrShortWrite
		}
	}
	return writeOK, nil
}

func mapWriteError(kind writeKind, method string, err error) error {
	switch kind {
	case writeLocal:
		return err
	case writeNone:
		return fmt.Errorf("%w: send %s: %w", ErrNotDispatched, method, err)
	case writePartial:
		return fmt.Errorf("%w: send %s: %w", ErrWriteInterrupted, method, err)
	default:
		return err
	}
}

// Activate verifies and imports one trusted plugin, atomically returning all
// registrations made during its synchronous activate call.
func (h *Host) Activate(ctx context.Context, spec PluginSpec) (*Activation, error) {
	var activation Activation
	if err := h.call(ctx, "activate", spec, &activation, false); err != nil {
		return nil, err
	}
	if err := validateActivation(spec, activation); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), h.config.ShutdownTimeout)
		defer cancel()
		var ignored struct {
			Disposed bool `json:"disposed"`
		}
		_ = h.call(cleanupCtx, "dispose", map[string]string{"activationId": activation.ActivationID}, &ignored, false)
		return nil, err
	}
	activation.host = h
	return &activation, nil
}

func validateActivation(spec PluginSpec, activation Activation) error {
	if activation.ActivationID == "" {
		return fmt.Errorf("%w: activation has no id", ErrProtocol)
	}
	if activation.Manifest.Name != spec.ExpectedName || activation.Manifest.Version == "" ||
		activation.Manifest.Main == "" || activation.Manifest.APIVersion != PluginAPIVersion {
		return fmt.Errorf("%w: activation returned an invalid manifest", ErrProtocol)
	}
	seenHandles := make(map[string]struct{}, len(activation.Descriptors))
	for _, descriptor := range activation.Descriptors {
		switch descriptor.Kind {
		case RegistrationProvider, RegistrationClient, RegistrationCatalogProvider:
		default:
			return fmt.Errorf("%w: activation returned descriptor kind %q", ErrProtocol, descriptor.Kind)
		}
		if descriptor.Handle == "" {
			return fmt.Errorf("%w: activation returned an empty object handle", ErrProtocol)
		}
		if _, exists := seenHandles[descriptor.Handle]; exists {
			return fmt.Errorf("%w: activation returned duplicate object handle %q", ErrProtocol, descriptor.Handle)
		}
		seenHandles[descriptor.Handle] = struct{}{}
		if _, err := descriptor.ID(); err != nil {
			return fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		seenMethods := make(map[string]struct{}, len(descriptor.Methods))
		for _, method := range descriptor.Methods {
			if method == "" || len(method) > 256 {
				return fmt.Errorf("%w: descriptor %q has an invalid method", ErrProtocol, descriptor.Handle)
			}
			if _, exists := seenMethods[method]; exists {
				return fmt.Errorf("%w: descriptor %q repeats method %q", ErrProtocol, descriptor.Handle, method)
			}
			seenMethods[method] = struct{}{}
		}
	}
	return nil
}

// Invoke calls a method on an arbitrary provider/client/catalog descriptor.
// Arguments and output must be JSON-serializable; callers retain typed control
// of the concrete adapter contract.
func (h *Host) Invoke(ctx context.Context, handle, method string, arguments any, output any) error {
	if arguments == nil {
		arguments = []any{}
	}
	return h.call(ctx, "invoke", map[string]any{
		"handle":    handle,
		"method":    method,
		"arguments": arguments,
	}, output, false)
}

// InvokeWithSignal calls a method while replacing one JSON object argument
// with a host-owned copy carrying the request AbortSignal. This is the only
// way cancellable plugin hooks receive a live signal: signals never cross the
// JSON boundary, and a plugin cannot substitute one through serialized input.
func (h *Host) InvokeWithSignal(
	ctx context.Context,
	handle, method string,
	arguments any,
	signalArgumentIndex int,
	output any,
) error {
	if arguments == nil {
		arguments = []any{}
	}
	return h.call(ctx, "invoke", map[string]any{
		"handle":              handle,
		"method":              method,
		"arguments":           arguments,
		"signalArgumentIndex": signalArgumentIndex,
	}, output, false)
}

// InvokeObject retains an object-valued method result behind a new RPC handle.
// This preserves nested optional capabilities without attempting to serialize
// JavaScript functions into Go. The returned handle is disposed with the same
// plugin activation as its parent.
func (h *Host) InvokeObject(
	ctx context.Context,
	handle, method string,
	arguments any,
) (*RemoteObject, error) {
	return h.invokeObject(ctx, handle, method, arguments, nil)
}

// InvokeObjectWithSignal is InvokeObject plus host-owned AbortSignal injection
// for hooks that return a retained runtime handle. The signal argument is
// intentionally an index, matching InvokeWithSignal's strict JSON boundary.
func (h *Host) InvokeObjectWithSignal(
	ctx context.Context,
	handle, method string,
	arguments any,
	signalArgumentIndex int,
) (*RemoteObject, error) {
	return h.invokeObject(ctx, handle, method, arguments, &signalArgumentIndex)
}

func (h *Host) invokeObject(
	ctx context.Context,
	handle, method string,
	arguments any,
	signalArgumentIndex *int,
) (*RemoteObject, error) {
	if arguments == nil {
		arguments = []any{}
	}
	params := map[string]any{
		"handle":       handle,
		"method":       method,
		"arguments":    arguments,
		"retainResult": true,
	}
	if signalArgumentIndex != nil {
		params["signalArgumentIndex"] = *signalArgumentIndex
	}
	var object RemoteObject
	if err := h.call(ctx, "invoke", params, &object, false); err != nil {
		return nil, err
	}
	if err := validateRemoteObject(object); err != nil {
		h.protocolFailed(err)
		return nil, err
	}
	return &object, nil
}

func validateRemoteObject(object RemoteObject) error {
	if object.Handle == "" || object.Properties == nil || len(object.Methods) == 0 {
		return fmt.Errorf("%w: invalid retained object descriptor", ErrProtocol)
	}
	seen := make(map[string]struct{}, len(object.Methods))
	for _, method := range object.Methods {
		if method == "" || len(method) > 256 {
			return fmt.Errorf("%w: retained object %q has an invalid method", ErrProtocol, object.Handle)
		}
		if _, exists := seen[method]; exists {
			return fmt.Errorf("%w: retained object %q repeats method %q", ErrProtocol, object.Handle, method)
		}
		seen[method] = struct{}{}
	}
	return nil
}

// Status reports only counts and is safe for degraded-state diagnostics.
func (h *Host) Status(ctx context.Context) (Status, error) {
	var status Status
	err := h.call(ctx, "status", map[string]any{}, &status, false)
	return status, err
}

// HealthSnapshot is a non-blocking view of the supervised process. It does
// not write to the child, so doctor can still explain a crashed or wedged host.
func (h *Host) HealthSnapshot() Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	health := Health{Running: !h.closed && !h.closing, Closing: h.closing && !h.closed}
	if h.endErr != nil {
		health.Error = h.endErr.Error()
	}
	return health
}

func (h *Host) call(ctx context.Context, method string, params, output any, internal bool) error {
	h.mu.Lock()
	if h.closed || h.closing && !internal {
		err := h.endErr
		if err == nil {
			err = ErrHostClosed
		}
		h.mu.Unlock()
		// Nothing was written for this call: the loss is provably pre-dispatch.
		return fmt.Errorf("%w: %w", ErrNotDispatched, err)
	}
	id := fmt.Sprintf("request-%d", h.nextID.Add(1))
	pending := &pendingCall{response: make(chan responseFrame, 1)}
	h.pending[id] = pending
	h.mu.Unlock()

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		callCtx, cancel = context.WithTimeout(ctx, h.config.CallTimeout)
		defer cancel()
	}
	timeout := h.config.CallTimeout
	if deadline, ok := callCtx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}
	frame := map[string]any{
		"version":   ProtocolVersion,
		"type":      "request",
		"id":        id,
		"method":    method,
		"params":    params,
		"timeoutMs": timeout.Milliseconds(),
	}
	kind, err := h.writeFrameState(frame)
	if err != nil {
		h.removePending(id, pending, false)
		return mapWriteError(kind, method, err)
	}

	select {
	case response := <-pending.response:
		if response.Error != nil {
			if response.Error.Code == "HOST_CLOSED" {
				h.mu.Lock()
				err := h.endErr
				h.mu.Unlock()
				if err != nil {
					return err
				}
				return ErrHostClosed
			}
			return response.Error
		}
		if output == nil {
			return nil
		}
		if err := decodeStrict(response.Result, output); err != nil {
			return fmt.Errorf("%w: decode %s result: %v", ErrProtocol, method, err)
		}
		return nil
	case <-callCtx.Done():
		h.removePending(id, pending, true)
		_ = h.writeFrame(map[string]any{
			"version": ProtocolVersion,
			"type":    "cancel",
			"id":      id,
		})
		return fmt.Errorf("pluginhost: %s: %w", method, callCtx.Err())
	}
}

func (h *Host) removePending(id string, pending *pendingCall, ignoreResponse bool) {
	h.mu.Lock()
	if h.pending[id] == pending {
		delete(h.pending, id)
		if ignoreResponse {
			h.ignored[id] = struct{}{}
		}
	}
	h.mu.Unlock()
}

// CallBudget exposes the configured per-call deadline so lifecycle callers can
// size their settlement wait against the same bound.
func (h *Host) CallBudget() time.Duration { return h.config.CallTimeout }

// ProcessIdentity returns the exact engine-supervised runtime generation.
// Callers may persist it to distinguish listeners verified inside this host
// from arbitrary plugin-detached children. The generation remains stable after
// termination so reconciliation can compare records after Wait has reaped it.
func (h *Host) ProcessIdentity() (generation string, pid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd == nil || h.cmd.Process == nil {
		return "", 0
	}
	return h.generation, h.cmd.Process.Pid
}

// InvokeSettling mirrors InvokeWithSignal with one extra guarantee for
// cancellable lifecycle hooks: when ctx ends, the cancel frame is written and
// the pending call STAYS OPEN until the host emits its terminal response —
// proof that the plugin coroutine has settled. A bare cancel is fire-and-
// forget: without this acknowledgement a cancelled startProxy can still be
// mid-bind when the caller starts compensating.
//
// settled=false means settlement could not be confirmed inside settleBefore:
// the coroutine may still be running, and callers must not treat an empty
// grace window as proof of anything.
func (h *Host) InvokeSettling(
	ctx context.Context,
	handle, method string,
	arguments any,
	signalArgumentIndex int,
	output any,
	settleBefore time.Duration,
) (settled bool, err error) {
	h.mu.Lock()
	if h.closed || h.closing {
		err := h.endErr
		if err == nil {
			err = ErrHostClosed
		}
		h.mu.Unlock()
		// Nothing was written: recovery may replay this call. settled=true so
		// lifecycle callers do not treat a dead host as an in-flight mutation.
		return true, fmt.Errorf("%w: %w", ErrNotDispatched, err)
	}
	id := fmt.Sprintf("request-%d", h.nextID.Add(1))
	pending := &pendingCall{response: make(chan responseFrame, 1)}
	h.pending[id] = pending
	h.mu.Unlock()

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		callCtx, cancel = context.WithTimeout(ctx, h.config.CallTimeout)
		defer cancel()
	}
	timeout := h.config.CallTimeout
	if deadline, ok := callCtx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}
	frame := map[string]any{
		"version": ProtocolVersion,
		"type":    "request",
		"id":      id,
		// The wire protocol dispatches on this frame-level method; the target
		// object method travels inside params like every other invoke.
		"method":    "invoke",
		"params":    map[string]any{"handle": handle, "method": method, "arguments": arguments, "signalArgumentIndex": signalArgumentIndex},
		"timeoutMs": timeout.Milliseconds(),
	}
	kind, err := h.writeFrameState(frame)
	if err != nil {
		h.removePending(id, pending, false)
		mapped := mapWriteError(kind, method, err)
		if kind == writePartial {
			// Bytes may already be in the host: this is an in-flight mutation.
			return false, mapped
		}
		return true, mapped
	}

	consume := func(response responseFrame) error {
		if response.Error != nil {
			if response.Error.Code == "HOST_CLOSED" {
				h.mu.Lock()
				endErr := h.endErr
				h.mu.Unlock()
				if endErr != nil {
					return endErr
				}
				return ErrHostClosed
			}
			return response.Error
		}
		if output == nil {
			return nil
		}
		if err := decodeStrict(response.Result, output); err != nil {
			return fmt.Errorf("%w: decode %s result: %v", ErrProtocol, method, err)
		}
		return nil
	}

	select {
	case response := <-pending.response:
		h.removePending(id, pending, false)
		if !isSettledResponse(response) {
			return false, consume(response)
		}
		return true, consume(response)
	case <-callCtx.Done():
		_ = h.writeFrame(map[string]any{
			"version": ProtocolVersion,
			"type":    "cancel",
			"id":      id,
		})
		if settleBefore <= 0 {
			h.removePending(id, pending, true)
			return false, fmt.Errorf("pluginhost: %s: %w", method, callCtx.Err())
		}
		timer := time.NewTimer(settleBefore)
		defer timer.Stop()
		select {
		case response := <-pending.response:
			h.removePending(id, pending, false)
			if !isSettledResponse(response) {
				_ = consume(response)
				return false, fmt.Errorf("pluginhost: %s: %w", method, callCtx.Err())
			}
			// The coroutine provably finished; surface cancellation as the
			// caller's error regardless of the host's own verdict.
			_ = consume(response)
			return true, fmt.Errorf("pluginhost: %s: %w", method, callCtx.Err())
		case <-timer.C:
			h.removePending(id, pending, true)
			return false, fmt.Errorf("pluginhost: %s: %w", method, callCtx.Err())
		case <-h.done:
			h.removePending(id, pending, true)
			return false, fmt.Errorf("pluginhost: %s: %w: %v", method, callCtx.Err(), ErrHostClosed)
		}
	}
}

// Terminate kills the host process immediately — the last resort when a
// cancelled lifecycle invocation cannot be proven settled, because nothing a
// not-yet-settled coroutine does afterwards can outlive its process. The next
// health snapshot reports degraded diagnostics; built-ins are unaffected.
// Idempotent.
func (h *Host) Terminate() error {
	h.mu.Lock()
	if h.closed {
		closing := h.closing
		err := h.endErr
		h.mu.Unlock()
		if closing || err == nil {
			return nil
		}
		return err
	}
	if h.closing {
		done := h.done
		h.mu.Unlock()
		<-done
		return nil
	}
	h.closing = true
	h.mu.Unlock()

	killErr := killHostGroup(h.cmd.Process)
	<-h.done
	return killErr
}

// Close asks the host to dispose activations in reverse order, then enforces a
// bounded process lifetime. It is idempotent.
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		err := h.endErr
		h.mu.Unlock()
		if errors.Is(err, ErrHostCrashed) || errors.Is(err, ErrProtocol) {
			return err
		}
		return nil
	}
	if h.closing {
		done := h.done
		h.mu.Unlock()
		<-done
		return nil
	}
	h.closing = true
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), h.config.ShutdownTimeout)
	defer cancel()
	var result struct {
		DisposalErrors []struct {
			ActivationID string `json:"activationId"`
			Message      string `json:"message"`
		} `json:"disposalErrors"`
	}
	err := h.call(ctx, "shutdown", map[string]any{}, &result, true)
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-ctx.Done():
		_ = killHostGroup(h.cmd.Process)
		<-h.done
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil && !errors.Is(err, ErrHostClosed) {
		return err
	}
	if len(result.DisposalErrors) > 0 {
		return fmt.Errorf("pluginhost: %d plugin disposal error(s); first: %s", len(result.DisposalErrors), result.DisposalErrors[0].Message)
	}
	return nil
}
