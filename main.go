package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const defaultTimeout = 3600

type request struct {
	Cmd      string `json:"cmd"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	IsBinary bool   `json:"is_binary"`
	Command  string `json:"command"`
	Cwd      string `json:"cwd"`
	Timeout  int    `json:"timeout"`
}

type output map[string]any

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("get working directory: %v", err)
	}
	addr := flag.String("addr", ":8080", "listen address")
	homeFlag := flag.String("home", cwd, "default home dir, overridable per request with ?homeDir=")
	flag.Parse()

	http.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, output{"status": "Healthy"})
	})

	http.HandleFunc("POST /invocations", func(w http.ResponseWriter, r *http.Request) {
		homeDir := *homeFlag
		if q := r.URL.Query().Get("home"); q != "" {
			homeDir = q
		}
		if !filepath.IsAbs(homeDir) {
			writeJSON(w, http.StatusBadRequest, output{"error": "home must be an absolute path"})
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, output{"error": "invalid JSON: " + err.Error()})
			return
		}
		log.Printf("cmd=%s home=%s", req.Cmd, homeDir)
		writeJSON(w, http.StatusOK, output{"output": invoke(r.Context(), filepath.Clean(homeDir), req)})
	})

	log.Printf("listening on %s (home %s)", *addr, *homeFlag)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func invoke(ctx context.Context, home string, req request) output {
	switch req.Cmd {
	case "exec":
		return execCommand(ctx, home, req.Command, req.Cwd, req.Timeout)
	case "file_read":
		return fileRead(home, req.Path)
	case "file_write":
		return fileWrite(home, req.Path, req.Content, req.IsBinary)
	case "dir_create":
		return dirCreate(home, req.Path)
	default:
		return output{"error": "Unknown command: " + req.Cmd}
	}
}

// relative paths resolve against home; absolute paths are used as-is
func resolvePath(home, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(home, path)
}

func execCommand(ctx context.Context, home, command, cwd string, timeout int) output {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	workDir := home
	if cwd != "" {
		workDir = resolvePath(home, cwd)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workDir
	// Run in its own process group so a timeout kills the whole tree, not just bash
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output{
			"stdout":    "",
			"stderr":    fmt.Sprintf("Command timed out after %d seconds", timeout),
			"exit_code": -1,
		}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return output{"stdout": "", "stderr": err.Error(), "exit_code": -1}
	}
	return output{"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": cmd.ProcessState.ExitCode()}
}

func fileRead(home, path string) output {
	data, err := os.ReadFile(resolvePath(home, path))
	if errors.Is(err, fs.ErrNotExist) {
		return output{"success": false, "error": "File not found: " + path, "path": path}
	}
	if err != nil {
		return output{"success": false, "error": err.Error(), "path": path}
	}
	if utf8.Valid(data) {
		return output{"success": true, "content": string(data), "path": path, "size": utf8.RuneCount(data)}
	}
	return output{
		"success":   true,
		"content":   base64.StdEncoding.EncodeToString(data),
		"path":      path,
		"is_binary": true,
		"size":      len(data),
	}
}

func fileWrite(home, path, content string, isBinary bool) output {
	data := []byte(content)
	if isBinary {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return output{"success": false, "error": "invalid base64: " + err.Error(), "path": path}
		}
		data = decoded
	}
	resolved := resolvePath(home, path)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return output{"success": false, "error": err.Error(), "path": path}
	}
	if err := os.WriteFile(resolved, data, 0o644); err != nil {
		return output{"success": false, "error": err.Error(), "path": path}
	}
	return output{
		"success": true,
		"message": fmt.Sprintf("Successfully wrote %d bytes to %s", len(data), path),
		"path":    path,
		"size":    len(data),
	}
}

func dirCreate(home, path string) output {
	if err := os.MkdirAll(resolvePath(home, path), 0o755); err != nil {
		return output{"success": false, "error": err.Error(), "path": path}
	}
	return output{"success": true, "message": "Successfully created directory " + path, "path": path}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}
