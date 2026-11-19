package gitopsdiff

import (
	"bytes"
	"io"
	"log"
	"sync"
)

// CaptureHelmWarnings routes the warnings helm prints through the standard logger (for example "cannot overwrite
// table with non table") to w, each distinct warning once and without a timestamp: a diff renders the same chart many
// times. It returns a function that restores the standard logger.
func CaptureHelmWarnings(w io.Writer) func() {
	flags, out := log.Flags(), log.Writer()
	log.SetFlags(0)
	log.SetOutput(&onceWriter{w: w, seen: map[string]bool{}})
	return func() {
		log.SetFlags(flags)
		log.SetOutput(out)
	}
}

// onceWriter writes each distinct message once; the standard logger writes one message per call.
type onceWriter struct {
	mu   sync.Mutex
	w    io.Writer
	seen map[string]bool
}

func (o *onceWriter) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg := string(bytes.TrimSpace(p))
	if o.seen[msg] {
		return len(p), nil
	}
	o.seen[msg] = true
	_, err := o.w.Write(p)
	return len(p), err
}
