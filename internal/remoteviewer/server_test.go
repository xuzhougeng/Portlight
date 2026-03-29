package remoteviewer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRemoteExecCommandRunsInRequestedDirectory(t *testing.T) {
	baseDir := t.TempDir()
	targetDir := filepath.Join(baseDir, "quote'd dir", "space here")

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}

	builtCommand := buildRemoteExecCommand(targetDir, "pwd")
	command := exec.Command("sh", "-lc", builtCommand)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run shell command: %v\ncommand: %s\noutput: %s", err, builtCommand, output)
	}

	if got := strings.TrimSpace(string(output)); got != targetDir {
		t.Fatalf("pwd = %q, want %q", got, targetDir)
	}
}

func TestBuildRemoteExecCommandPreservesShellSyntax(t *testing.T) {
	targetDir := t.TempDir()

	builtCommand := buildRemoteExecCommand(targetDir, `printf '%s' "$PWD"`)
	command := exec.Command(
		"sh",
		"-lc",
		builtCommand,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run shell command: %v\ncommand: %s\noutput: %s", err, builtCommand, output)
	}

	if got := strings.TrimSpace(string(output)); got != targetDir {
		t.Fatalf("pwd = %q, want %q", got, targetDir)
	}
}

func TestDeriveHTMLPreviewRoot(t *testing.T) {
	tests := []struct {
		name        string
		sessionRoot string
		remotePath  string
		want        string
	}{
		{
			name:        "use configured session root when html is under it",
			sessionRoot: "/var/www/app",
			remotePath:  "/var/www/app/pages/index.html",
			want:        "/var/www/app",
		},
		{
			name:        "fall back to filesystem root when file is outside session root",
			sessionRoot: "/var/www/app",
			remotePath:  "/opt/other/index.html",
			want:        "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveHTMLPreviewRoot(tt.sessionRoot, tt.remotePath); got != tt.want {
				t.Fatalf("deriveHTMLPreviewRoot(%q, %q) = %q, want %q", tt.sessionRoot, tt.remotePath, got, tt.want)
			}
		})
	}
}

func TestRewriteHTMLPreviewDocumentRewritesAbsolutePathsFromSessionRoot(t *testing.T) {
	content := strings.Join([]string{
		`<html><head>`,
		`<link rel="stylesheet" href="/assets/app.css">`,
		`</head><body>`,
		`<img src="/images/logo.png">`,
		`<script src="/assets/app.js"></script>`,
		`</body></html>`,
	}, "")

	got := rewriteHTMLPreviewDocument(
		content,
		"session-1",
		"/var/www/app/index.html",
		"/var/www/app",
	)

	wantRefs := []string{
		`/api/html-preview/session-1/var/www/app/assets/app.css`,
		`/api/html-preview/session-1/var/www/app/images/logo.png`,
		`/api/html-preview/session-1/var/www/app/assets/app.js`,
	}

	for _, want := range wantRefs {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten html missing %q\nhtml: %s", want, got)
		}
	}

	unexpected := `/api/html-preview/session-1/assets/app.js`
	if strings.Contains(got, unexpected) {
		t.Fatalf("rewritten html should not contain filesystem-root path %q\nhtml: %s", unexpected, got)
	}
}
