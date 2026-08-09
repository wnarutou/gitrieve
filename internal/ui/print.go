package ui

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/gookit/color"
)

// Sink receives log messages produced by ui.Printf/ui.Errorf from a goroutine
// that has been bound to an execution via Bind.
type Sink interface {
	Log(executionID, jobName, level, message string) error
}

var (
	sinkMu sync.RWMutex
	sink   Sink

	bindMu   sync.RWMutex
	bindings map[uint64]boundJob
)

type boundJob struct {
	executionID string
	jobName     string
}

// SetSink registers the sink that receives bound log output. Passing nil
// disables forwarding (the default, so CLI/daemon output is unchanged).
func SetSink(s Sink) {
	sinkMu.Lock()
	sink = s
	sinkMu.Unlock()
}

// Bind associates the calling goroutine with an execution record so that
// Printf/Errorf calls from this goroutine are also persisted through the sink.
// The returned function unbinds the goroutine.
func Bind(executionID, jobName string) func() {
	id := goroutineID()
	bindMu.Lock()
	if bindings == nil {
		bindings = make(map[uint64]boundJob)
	}
	bindings[id] = boundJob{executionID: executionID, jobName: jobName}
	bindMu.Unlock()
	return func() {
		bindMu.Lock()
		delete(bindings, id)
		bindMu.Unlock()
	}
}

// goroutineID returns the current goroutine's ID by parsing the runtime stack.
func goroutineID() uint64 {
	buf := make([]byte, 256)
	buf = buf[:runtime.Stack(buf, false)]
	line := string(buf)
	i := strings.Index(line, "goroutine ")
	idStr := line[i+len("goroutine "):]
	j := strings.IndexByte(idStr, ' ')
	idStr = idStr[:j]
	id, _ := strconv.ParseUint(idStr, 10, 64)
	return id
}

// logThroughSink forwards a message to the sink when the calling goroutine is
// bound. It short-circuits (no runtime.Stack call) when there is no sink or no
// bindings.
func logThroughSink(level, message string) {
	sinkMu.RLock()
	s := sink
	sinkMu.RUnlock()
	if s == nil {
		return
	}
	bindMu.RLock()
	has := len(bindings) > 0
	bindMu.RUnlock()
	if !has {
		return
	}

	id := goroutineID()
	bindMu.RLock()
	b, ok := bindings[id]
	bindMu.RUnlock()
	if !ok {
		return
	}
	_ = s.Log(b.executionID, b.jobName, level, message)
}

func Errorf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Danger.Print(msg + "\n")
	logThroughSink("error", msg)
}

func ErrorfExit(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Danger.Print(msg + "\n")
	os.Exit(1)
}

func Printf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	color.Success.Print(msg + "\n")
	logThroughSink("info", msg)
}

func Exit() {
	os.Exit(0)
}
