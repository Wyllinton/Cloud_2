package modules

import (
	"context"
	"fmt"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// IssueManager customises the pre-login banner stored in /etc/issue. The banner
// is interpreted by agetty, which expands escape sequences such as \n (host
// name) or \4 (IPv4 address).
type IssueManager struct {
	R runner.Runner
}

// NewIssueManager builds the manager for a transport.
func NewIssueManager(r runner.Runner) *IssueManager { return &IssueManager{R: r} }

// IssueRequest selects the fields shown in the banner.
type IssueRequest struct {
	Hostname     bool
	Date         bool
	Time         bool
	IPv4         bool
	Kernel       bool
	OSName       bool
	TTY          bool
	Custom       string
	AlsoIssueNet bool
}

const issuePath = "/etc/issue"
const issueNetPath = "/etc/issue.net"

// BuildIssue renders the banner text from the request. It is a pure function so
// the exact output can be previewed and unit tested.
func BuildIssue(req IssueRequest, custom string) string {
	var fields []string
	if req.Hostname {
		fields = append(fields, "Host: \\n")
	}
	if req.OSName {
		fields = append(fields, "Sistema: \\s \\r")
	}
	if req.Kernel && !req.OSName {
		fields = append(fields, "Kernel: \\r")
	}
	if req.IPv4 {
		fields = append(fields, "IP: \\4")
	}
	if req.Date {
		fields = append(fields, "Fecha: \\d")
	}
	if req.Time {
		fields = append(fields, "Hora: \\t")
	}
	if req.TTY {
		fields = append(fields, "Terminal: \\l")
	}

	var b strings.Builder
	b.WriteString("\n")
	if len(fields) > 0 {
		b.WriteString(strings.Join(fields, "   |   "))
		b.WriteString("\n")
	}
	if custom != "" {
		b.WriteString(custom)
		if !strings.HasSuffix(custom, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// Current reads the current /etc/issue.
func (m *IssueManager) Current(ctx context.Context) (string, error) {
	content, found, err := readFile(ctx, m.R, issuePath)
	if err != nil {
		return "", err
	}
	if !found {
		return "(no existe /etc/issue)", nil
	}
	return string(content), nil
}

// Preview returns the banner that would be written, without touching the system.
func (m *IssueManager) Preview(req IssueRequest) (string, error) {
	custom, err := validate.IssueMessage(req.Custom)
	if err != nil {
		return "", err
	}
	return BuildIssue(req, custom), nil
}

// Apply writes the banner to /etc/issue (and optionally /etc/issue.net).
func (m *IssueManager) Apply(ctx context.Context, req IssueRequest) *op.Report {
	rep := op.New("Personalizar /etc/issue")
	defer rep.Finish()

	custom, err := validate.IssueMessage(req.Custom)
	if err != nil {
		return rep.Fail(err)
	}
	content := BuildIssue(req, custom)
	if strings.TrimSpace(content) == "" {
		return rep.Fail(fmt.Errorf("el mensaje de inicio de sesión quedaría vacío"))
	}

	rep.Note("Vista previa del contenido a escribir:")
	rep.Note(content)

	if err := writeFile(ctx, m.R, rep, issuePath, []byte(content), 0644); err != nil {
		return rep.Fail(err)
	}
	if req.AlsoIssueNet {
		if err := writeFile(ctx, m.R, rep, issueNetPath, []byte(content), 0644); err != nil {
			return rep.Fail(err)
		}
	}

	res := m.R.Run(ctx, nil, "cat", "--", issuePath)
	rep.Step("Verificar contenido de "+issuePath, res)
	rep.Summary = "Mensaje de inicio de sesión actualizado en " + issuePath + "."
	return rep
}
