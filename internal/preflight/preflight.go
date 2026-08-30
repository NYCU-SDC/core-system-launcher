// Package preflight checks the environment before anything is downloaded.
//
// Every check here comes from something that actually went wrong: a port held
// by another container, a Docker daemon that was not running, compose v1
// lacking --wait.
package preflight

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Result is the outcome of a single check.
type Result struct {
	Name string
	OK   bool
	// Fatal means there is no point continuing until it is fixed.
	Fatal  bool
	Detail string
	// Fix is what the user should do about it.
	Fix string
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// CheckDocker verifies the docker CLI exists and the daemon answers.
func CheckDocker(ctx context.Context) Result {
	r := Result{Name: "Docker", Fatal: true}
	if _, err := exec.LookPath("docker"); err != nil {
		r.Detail = "找不到 docker 指令"
		r.Fix = "請先安裝 Docker Desktop、OrbStack 或 Docker Engine"
		return r
	}
	out, err := run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		r.Detail = "docker daemon 沒有回應"
		r.Fix = "請確認 Docker 已經啟動（Docker Desktop / OrbStack 要先打開）"
		return r
	}
	r.OK, r.Detail = true, "server "+out
	return r
}

// CheckCompose verifies docker compose v2 is available.
func CheckCompose(ctx context.Context) Result {
	r := Result{Name: "docker compose", Fatal: true}
	out, err := run(ctx, "docker", "compose", "version", "--short")
	if err != nil {
		r.Detail = "docker compose 無法使用"
		r.Fix = "需要 Docker Compose v2（內建於新版 Docker，指令是 `docker compose` 不是 `docker-compose`）"
		return r
	}
	r.OK, r.Detail = true, "v"+strings.TrimPrefix(out, "v")
	return r
}

// CheckGit verifies git exists; fetching the upstream sources needs it.
func CheckGit(ctx context.Context) Result {
	r := Result{Name: "git", Fatal: true}
	out, err := run(ctx, "git", "--version")
	if err != nil {
		r.Detail = "找不到 git 指令"
		r.Fix = "請先安裝 git"
		return r
	}
	r.OK, r.Detail = true, strings.TrimPrefix(out, "git version ")
	return r
}

// CheckPort verifies a port is free.
//
// The occupant is often another container, so the message needs to be explicit
// or people assume one of their own services is at fault.
//
// Binding 0.0.0.0 rather than 127.0.0.1 matters: Docker publishes ports on
// 0.0.0.0, and on macOS binding 127.0.0.1 does not conflict with that, which
// yields a false "available" result.
func CheckPort(port int, label string) Result {
	r := Result{Name: fmt.Sprintf("port %d (%s)", port, label), Fatal: true}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		r.Detail = "已經被佔用"
		r.Fix = fmt.Sprintf("換一個 port，或先關掉佔用它的程式：\n      lsof -nP -iTCP:%d -sTCP:LISTEN", port)
		return r
	}
	_ = ln.Close()
	r.OK, r.Detail = true, "可用"
	return r
}

// PortFree reports whether a port can currently be bound.
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// FindFreePort returns the first free port at or above start. Used for ports the
// user should not have to care about, such as the debug-only postgres mapping.
func FindFreePort(start int) (int, error) {
	for p := start; p < start+200; p++ {
		if PortFree(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("從 %d 起找不到可用的 port", start)
}

// CheckArch just reports the platform and never fails.
func CheckArch() Result {
	return Result{Name: "平台", OK: true, Detail: runtime.GOOS + "/" + runtime.GOARCH}
}

// RunAll runs the whole set.
func RunAll(ctx context.Context, appPort, pgPort int) []Result {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	return []Result{
		CheckArch(),
		CheckDocker(ctx),
		CheckCompose(ctx),
		CheckGit(ctx),
		CheckPort(appPort, "對外入口"),
		CheckPort(pgPort, "postgres"),
	}
}

// HasFatal reports whether any fatal problem was found.
func HasFatal(rs []Result) bool {
	for _, r := range rs {
		if !r.OK && r.Fatal {
			return true
		}
	}
	return false
}
