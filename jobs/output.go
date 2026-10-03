package jobs

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// outputStore is the per-job output carrier. There are two shapes: a
// manager-owned file (the manager creates it and holds its write handle) and a
// product-owned path (the manager only ever reads it). The manager treats both
// the same way for reading, which is what keeps "fetch by offset" identical.
type outputStore interface {
	// write appends text (a no-op for a product-owned path).
	write(text string)
	// stats returns the committed size, line count and truncation flag.
	stats() (int64, int, bool)
	// close releases any write handle (a no-op for a product-owned path).
	close()
	// path is the file the read side reads by offset.
	path() string
	// remove deletes a manager-owned file (a no-op for a product-owned path,
	// whose lifecycle belongs to the product).
	remove()
}

// outputWriter owns the bounded output file of one manager-owned job.
//
// It keeps its own state (size / lines / cap) behind its own mutex so that no
// lock is ever held across both the table lock and the output lock: the table
// layer never calls into a writer while holding its mutex through a callback
// that could take the table lock again. The only edge is table -> writer.
//
// Past the size cap, bytes are dropped while the writer still reports success:
// the cap protects disk and context budget, it is not the command's failure,
// and reporting a short write would make the command itself exit abnormally
// (I-6).
type outputWriter struct {
	mu        sync.Mutex
	file      *os.File
	filePath  string
	size      int64
	lines     int
	remain    int64
	truncated bool
	onChange  func()
}

// newOutputWriter creates (or truncates) the output file of one job.
func newOutputWriter(path string, limit int64, onChange func()) (*outputWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("jobs: open output %q: %w", path, err)
	}
	return &outputWriter{file: file, filePath: path, remain: limit, onChange: onChange}, nil
}

// newOutputStore picks the output shape for one job: a product-owned path when
// Spec.OutputPath is set, otherwise a manager-owned file under ownPath.
func newOutputStore(outputPath, ownPath string, limit int64, onChange func()) (outputStore, error) {
	if path := strings.TrimSpace(outputPath); path != "" {
		return &externalOutput{filePath: path}, nil
	}
	return newOutputWriter(ownPath, limit, onChange)
}

// externalOutput is a product-owned output file. The manager never opens it for
// writing - it only reads it through an offset cursor and never holds a handle
// across the job's life - so a directory holding it stays removable while the
// job is registered (the reason this shape exists).
//
// Size comes from a stat; the line count is counted once per size change and
// cached, because stats() is called on every observation.
type externalOutput struct {
	mu          sync.Mutex
	filePath    string
	cachedSize  int64
	cachedLines int
	cached      bool
}

func (o *externalOutput) write(string) {}

func (o *externalOutput) close() {}

func (o *externalOutput) remove() {}

func (o *externalOutput) path() string { return o.filePath }

func (o *externalOutput) stats() (int64, int, bool) {
	if o == nil {
		return 0, 0, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	info, err := os.Stat(o.filePath)
	if err != nil {
		return 0, 0, false
	}
	size := info.Size()
	if !o.cached || size != o.cachedSize {
		o.cachedSize = size
		o.cachedLines = countFileLines(o.filePath, size)
		o.cached = true
	}
	return size, o.cachedLines, false
}

// lineCountCap bounds how much of an output file the line counter reads: it is
// a magnitude for a projection, and counting lines must never hold up a read.
const lineCountCap = 1 << 20

// countFileLines counts the newlines in at most lineCountCap bytes of path.
// It is a magnitude, not a byte-exact wc -l: a huge file must not hold up the
// read path.
func countFileLines(path string, size int64) int {
	if size <= 0 {
		return 0
	}
	if size > lineCountCap {
		size = lineCountCap
	}
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	lines, read := 0, int64(0)
	for read < size {
		chunk, err := reader.ReadBytes('\n')
		read += int64(len(chunk))
		if len(chunk) > 0 {
			lines++
		}
		if err != nil {
			break
		}
	}
	return lines
}

// path implements outputStore.
func (w *outputWriter) path() string {
	if w == nil {
		return ""
	}
	return w.filePath
}

// remove implements outputStore: close the write handle, then unlink the file.
// Both steps are best-effort - a failed unlink only means the log lingers.
func (w *outputWriter) remove() {
	if w == nil {
		return
	}
	w.close()
	_ = os.Remove(w.filePath)
}

// write appends text, dropping whatever exceeds the cap.
func (w *outputWriter) write(text string) {
	if w == nil || text == "" {
		return
	}
	w.mu.Lock()
	if w.file == nil {
		w.mu.Unlock()
		return
	}
	written := 0
	if w.remain > 0 {
		data := []byte(text)
		if int64(len(data)) > w.remain {
			data = data[:w.remain]
			w.truncated = true
		}
		n, err := w.file.Write(data)
		if n > 0 {
			w.remain -= int64(n)
			w.size += int64(n)
			w.lines += bytes.Count(data[:n], []byte{'\n'})
			written = n
		} else if err != nil {
			w.truncated = true
		}
	} else {
		w.truncated = true
	}
	w.mu.Unlock()
	if written > 0 && w.onChange != nil {
		w.onChange()
	}
}

// stats returns the committed size, line count and truncation flag.
func (w *outputWriter) stats() (int64, int, bool) {
	if w == nil {
		return 0, 0, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size, w.lines, w.truncated
}

// close releases the file handle. Later writes become no-ops.
func (w *outputWriter) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}

// readRange reads [offset, offset+length) from a job's output file.
//
// Reading a file that is still being appended is safe: Go opens files allowing
// shared reads on Windows, and the caller never asks for bytes beyond the
// committed size. A chunk is trimmed back to the last complete rune so a
// multi-byte character is never cut in half between two fetches.
func readRange(path string, offset, length int64) (string, error) {
	if length <= 0 {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A product-owned output file may not exist yet (the product
			// creates it lazily): "nothing written" is not a read failure.
			return "", nil
		}
		return "", fmt.Errorf("jobs: read output %q: %w", path, err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("jobs: seek output %q: %w", path, err)
	}
	buffer := make([]byte, length)
	read, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", fmt.Errorf("jobs: read output %q: %w", path, err)
	}
	buffer = buffer[:read]
	for len(buffer) > 0 {
		r, size := utf8.DecodeLastRune(buffer)
		if r != utf8.RuneError || size > 1 {
			break
		}
		buffer = buffer[:len(buffer)-1]
	}
	return string(buffer), nil
}

// jobSink is the execution-side channel handed to an Executor.
type jobSink struct {
	manager *manager
	run     *run
}

func (s *jobSink) Note(text string) { s.run.writer.write(text) }

func (s *jobSink) SignalBytes() { s.manager.signalChange() }

func (s *jobSink) Exit(code int) {
	s.manager.mu.Lock()
	s.run.exitCode = code
	s.manager.mu.Unlock()
}

func (s *jobSink) Complete(state State, summary string) {
	s.manager.finalize(s.run, state, summary)
}

var _ Sink = (*jobSink)(nil)
