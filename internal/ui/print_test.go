package ui

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeSink struct {
	mu   sync.Mutex
	logs []string
}

func (f *fakeSink) Log(executionID, jobName, level, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, executionID+"|"+jobName+"|"+level+"|"+message)
	return nil
}

func (f *fakeSink) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

func TestBindRoutesUiOutputToSink(t *testing.T) {
	s := &fakeSink{}
	SetSink(s)
	defer SetSink(nil)

	// Without a binding, output is terminal-only (not forwarded).
	Printf("hello %s", "world")
	assert.Len(t, s.messages(), 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		unbind := Bind("exec-1", "repo-a")
		Errorf("boom %d", 42)
		unbind()
		Printf("after unbind")
	}()
	<-done

	msgs := s.messages()
	assert.Len(t, msgs, 1, "only the bound call should be forwarded")
	assert.Equal(t, "exec-1|repo-a|error|boom 42", msgs[0])
}

func TestBindIsPerGoroutine(t *testing.T) {
	s := &fakeSink{}
	SetSink(s)
	defer SetSink(nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		unbind := Bind("exec-A", "repo-a")
		defer unbind()
		Printf("A")
	}()
	go func() {
		defer wg.Done()
		unbind := Bind("exec-B", "repo-b")
		defer unbind()
		Printf("B")
	}()
	wg.Wait()

	msgs := s.messages()
	assert.Len(t, msgs, 2)
	assert.Contains(t, msgs, "exec-A|repo-a|info|A")
	assert.Contains(t, msgs, "exec-B|repo-b|info|B")
}
