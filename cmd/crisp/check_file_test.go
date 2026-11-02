package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const badFileAnswer = "BADFILE 这段回答需要足够长，才能触发质量门禁的语义审查流程并让模拟评审给出 material 判定。这段回答需要足够长，才能触发质量门禁的语义审查流程并让模拟评审给出 material 判定。这段回答需要足够长，才能触发质量门禁的语义审查流程并让模拟评审给出 material 判定。"

func writeCheckFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestCheckFilePass(t *testing.T) {
	path := writeCheckFile(t, "good.md", goodAnswer)
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)

	stdout, stderr, code := runCrisp(t, []string{"check-file", path}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "PASS") {
		t.Fatalf("expected PASS in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "checked=1 blocked=0") {
		t.Fatalf("unexpected summary line: %q", stdout)
	}
	if calls := m.calls.Load(); calls != 1 {
		t.Fatalf("expected one critic call, got %d", calls)
	}
}

func TestCheckFileBlocks(t *testing.T) {
	path := writeCheckFile(t, "bad.md", badFileAnswer)
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(materialVerdict())
	})
	m.configure(t)

	stdout, stderr, code := runCrisp(t, []string{"check-file", path}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "BLOCK") {
		t.Fatalf("expected BLOCK in output, got %q", stdout)
	}
}

func TestCheckFileMultipleFilesJSON(t *testing.T) {
	good := writeCheckFile(t, "good.md", goodAnswer)
	bad := writeCheckFile(t, "bad.md", badFileAnswer)
	m := newMockCritic(t, func(_ int, body string) (int, string, string) {
		if strings.Contains(body, "BADFILE") {
			return 200, "application/json", chatResponse(materialVerdict())
		}
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)

	stdout, stderr, code := runCrisp(t, []string{"check-file", "--json", good, bad}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, stderr)
	}
	var report struct {
		Files []struct {
			Path     string `json:"path"`
			Decision string `json:"decision"`
			FailOpen bool   `json:"fail_open"`
		} `json:"files"`
		Summary struct {
			Checked int `json:"checked"`
			Blocked int `json:"blocked"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("check-file --json output is not valid JSON: %v (%q)", err, stdout)
	}
	if len(report.Files) != 2 {
		t.Fatalf("expected 2 file results, got %d", len(report.Files))
	}
	if report.Summary.Checked != 2 || report.Summary.Blocked != 1 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	if report.Files[0].Decision != "pass" || report.Files[1].Decision != "block" {
		t.Fatalf("unexpected decisions: %+v", report.Files)
	}
}

func TestCheckFileMissingFileFailsOpen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.md")
	stdout, stderr, code := runCrisp(t, []string{"check-file", "--json", missing}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr)
	}
	var report struct {
		Files []struct {
			Decision string `json:"decision"`
			FailOpen bool   `json:"fail_open"`
			Error    string `json:"error"`
		} `json:"files"`
		Summary struct {
			Errors int `json:"errors"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, stdout)
	}
	if len(report.Files) != 1 || !report.Files[0].FailOpen || report.Files[0].Error == "" {
		t.Fatalf("unexpected file result: %+v", report.Files)
	}
	if report.Summary.Errors != 1 {
		t.Fatalf("expected 1 error, got %+v", report.Summary)
	}
}

func TestCheckFileStdin(t *testing.T) {
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)
	stdout, stderr, code := runCrisp(t, []string{"check-file", "-"}, goodAnswer)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "-: PASS") {
		t.Fatalf("expected stdin PASS line, got %q", stdout)
	}
}

func TestCheckFileUsageErrors(t *testing.T) {
	if _, _, code := runCrisp(t, []string{"check-file"}, ""); code != 2 {
		t.Fatalf("no-args exit code = %d, want 2", code)
	}
	if _, _, code := runCrisp(t, []string{"check-file", "--nope", "x.md"}, ""); code != 2 {
		t.Fatalf("unknown-flag exit code = %d, want 2", code)
	}
}

func TestCheckFileShortAnswerSkipsCritic(t *testing.T) {
	path := writeCheckFile(t, "short.md", shortAnswer)
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(materialVerdict())
	})
	m.configure(t)
	stdout, _, code := runCrisp(t, []string{"check-file", path}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if calls := m.calls.Load(); calls != 0 {
		t.Fatalf("short answer must not call the critic, got %d calls", calls)
	}
	if !strings.Contains(stdout, "critic=skipped") {
		t.Fatalf("expected critic=skipped, got %q", stdout)
	}
}

func TestCheckFileCriticFailureFailsOpen(t *testing.T) {
	path := writeCheckFile(t, "good.md", goodAnswer)
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 500, "application/json", `{"error":"internal"}`
	})
	m.configure(t)
	stdout, _, code := runCrisp(t, []string{"check-file", "--json", path}, "")
	if code != 0 {
		t.Fatalf("critic failure must fail open with exit 0, got %d", code)
	}
	var report struct {
		Files []struct {
			Decision string `json:"decision"`
			FailOpen bool   `json:"fail_open"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(report.Files) != 1 || report.Files[0].Decision != "pass" || !report.Files[0].FailOpen {
		t.Fatalf("unexpected fail-open result: %+v", report.Files)
	}
}

func TestCheckFileCriticNotConfiguredFailsOpen(t *testing.T) {
	path := writeCheckFile(t, "good.md", goodAnswer)
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)
	t.Setenv("QUALITY_GATE_BASE_URL", "")
	t.Setenv("QUALITY_GATE_MODEL", "")
	stdout, _, code := runCrisp(t, []string{"check-file", "--json", path}, "")
	if code != 0 {
		t.Fatalf("unconfigured critic must fail open with exit 0, got %d", code)
	}
	if !strings.Contains(stdout, `"fail_open":true`) {
		t.Fatalf("expected fail_open true, got %q", stdout)
	}
}
