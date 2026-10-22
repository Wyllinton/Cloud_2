// Package op defines the structured result type returned by every automation
// module. Each operation must tell the user whether it succeeded and must keep
// the technical detail (command, exit code, stdout, stderr) needed to diagnose
// a failure.
package op

import (
	"fmt"
	"strings"
	"time"

	"np2/appgo/internal/runner"
)

// Step is one command execution inside an operation.
type Step struct {
	Title       string
	Command     string
	HostCommand string
	Transport   string
	Stdout      string
	Stderr      string
	ExitCode    int
	OK          bool
	Error       string
	Duration    time.Duration
}

// Artifact is a file produced by an operation that the user can download, for
// example a private SSH key.
type Artifact struct {
	Name        string
	Filename    string
	ContentType string
	Data        []byte
	Note        string
}

// Report is the outcome of an operation.
type Report struct {
	Action     string
	OK         bool
	Summary    string
	Err        error
	Steps      []Step
	Notes      []string
	Warnings   []string
	Artifacts  []Artifact
	StartedAt  time.Time
	FinishedAt time.Time
}

// New starts a report for the given action.
func New(action string) *Report {
	return &Report{Action: action, StartedAt: time.Now()}
}

// Step records the result of a command execution and reports whether it
// succeeded.
func (r *Report) Step(title string, res runner.Result) bool {
	ok := res.Ok()
	if res.Title != "" {
		title = res.Title
	}
	r.Steps = append(r.Steps, Step{
		Title:       title,
		Command:     res.CommandLine(),
		HostCommand: res.HostCommand,
		Transport:   res.Transport,
		Stdout:      strings.TrimRight(res.Stdout, "\n"),
		Stderr:      strings.TrimRight(res.Stderr, "\n"),
		ExitCode:    res.ExitCode,
		OK:          ok,
		Error:       res.ErrorString(),
		Duration:    res.Duration,
	})
	return ok
}

// Failf marks the operation as failed with a formatted message.
func (r *Report) Failf(format string, args ...any) *Report {
	r.Err = fmt.Errorf(format, args...)
	return r
}

// Fail marks the operation as failed with err.
func (r *Report) Fail(err error) *Report {
	r.Err = err
	return r
}

// Note appends an informational line shown to the user.
func (r *Report) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// Warn appends a warning shown to the user.
func (r *Report) Warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// AddArtifact registers a downloadable file produced by the operation.
func (r *Report) AddArtifact(a Artifact) {
	r.Artifacts = append(r.Artifacts, a)
}

// Failed reports whether the operation has failed so far.
func (r *Report) Failed() bool { return r.Err != nil }

// Finish computes the final status and timestamp. It must be called once, right
// before the report is rendered. It is safe to defer.
func (r *Report) Finish() {
	r.FinishedAt = time.Now()
	r.OK = r.Err == nil
	if r.OK {
		for _, s := range r.Steps {
			if !s.OK {
				r.OK = false
				break
			}
		}
	}
	switch {
	case r.Err != nil:
		r.Summary = r.Err.Error()
	case r.OK && r.Summary == "":
		r.Summary = r.Action + ": operación completada correctamente."
	}
}

// Duration returns the wall-clock time the operation took.
func (r *Report) Duration() time.Duration {
	if r.FinishedAt.IsZero() {
		return time.Since(r.StartedAt)
	}
	return r.FinishedAt.Sub(r.StartedAt)
}
