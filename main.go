// core-system-launcher stands up the NYCU SDC Core System on your own machine.
//
// It does four things:
//  1. Checks the environment (Docker, compose, ports)
//  2. Fetches backend and frontend at the commits pinned in versions.lock and
//     applies the patches under patches/
//  3. Generates the compose setup and builds everything inside containers
//  4. Walks through Google OAuth, or uses a no-OAuth trial mode instead
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NYCU-SDC/core-system-launcher/internal/config"
	"github.com/NYCU-SDC/core-system-launcher/internal/docker"
	"github.com/NYCU-SDC/core-system-launcher/internal/patch"
	"github.com/NYCU-SDC/core-system-launcher/internal/preflight"
	"github.com/NYCU-SDC/core-system-launcher/internal/prompt"
	"github.com/NYCU-SDC/core-system-launcher/internal/seed"
)

//go:embed all:patches
var patchesFS embed.FS

//go:embed versions.lock
var versionsLock []byte

//go:embed deploy/backend.Dockerfile
var backendDockerfile []byte

//go:embed deploy/frontend.Dockerfile
var frontendDockerfile []byte

//go:embed seed/forms.json
var seedFormsJSON []byte

// Version is injected through ldflags.
var Version = "dev"

const (
	backendRepoURL  = "https://github.com/NYCU-SDC/core-system-backend.git"
	frontendRepoURL = "https://github.com/NYCU-SDC/core-system-frontend.git"

	defaultAppPort = 8080
	defaultPGPort  = 15432
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Let calls waiting on user input be interrupted by Ctrl+C too.
	prompt.WatchInterrupt(ctx)

	cmd := "up"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "up":
		err = cmdUp(ctx, args)
	case "down":
		err = cmdDown(ctx)
	case "logs":
		err = cmdLogs(ctx, args)
	case "status":
		err = cmdStatus(ctx)
	case "doctor":
		err = cmdDoctor(ctx)
	case "reset":
		err = cmdReset(ctx)
	case "version", "-v", "--version":
		fmt.Println("core-system-launcher " + Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		err = fmt.Errorf("未知的指令：%s", cmd)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println()
			prompt.Warn("已中止")
			os.Exit(130)
		}
		fmt.Println()
		prompt.Fail("%v", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`core-system-launcher — 在自己的機器上架一套 NYCU SDC Core System

用法：
  core-system-launcher [指令]

指令：
  up        建置並啟動（第一次執行會走完整的互動設定）
  down      停止服務，資料保留
  logs      追蹤服務日誌，可指定服務名（backend / frontend / postgres）
  status    顯示各服務目前狀態
  doctor    診斷環境問題（Docker、port 等）
  reset     清掉資料庫與已下載的原始碼，全部重來
  version   顯示版本

選項：
  --rebuild  搭配 up 使用，強制重新建置 image

環境變數：
  CORE_SYSTEM_LAUNCHER_HOME  工作目錄，預設 ~/.core-system-launcher
`)
}

// ── up ───────────────────────────────────────────────────────────

func cmdUp(ctx context.Context, args []string) error {
	rebuild := false
	for _, a := range args {
		if a == "--rebuild" {
			rebuild = true
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	firstRun := cfg == nil
	if firstRun {
		cfg, err = setupInteractive(ctx)
		if err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
	} else {
		prompt.Section("使用既有設定")
		prompt.Info("入口       %s", cfg.BaseURL())
		prompt.Info("管理者     %s", cfg.AdminEmail)
		if cfg.TrialMode {
			prompt.Info("登入方式   試用模式（未接 Google OAuth）")
		} else {
			prompt.Info("登入方式   Google OAuth")
		}
		prompt.Hint("要改設定的話，先執行 reset，或直接編輯工作目錄下的 config.json")
	}

	if err := runPreflight(ctx, cfg, !firstRun); err != nil {
		return err
	}
	if err := prepareSources(ctx); err != nil {
		return err
	}

	srcDir, err := config.SrcDir()
	if err != nil {
		return err
	}
	deployDir, err := config.DeployDir()
	if err != nil {
		return err
	}
	if err := docker.WriteDeployFiles(cfg, deployDir, srcDir, backendDockerfile, frontendDockerfile); err != nil {
		return err
	}

	prompt.Section("建置並啟動")
	prompt.Hint("第一次建置要編譯 Go 與前端，通常需要數分鐘")
	upArgs := []string{"up", "-d", "--wait"}
	if rebuild || firstRun {
		upArgs = append(upArgs, "--build")
	}
	if err := docker.Compose(ctx, deployDir, upArgs...); err != nil {
		return fmt.Errorf("啟動失敗，可以用 `core-system-launcher logs` 看細節：%w", err)
	}

	if err := waitReady(ctx, cfg); err != nil {
		return err
	}
	prompt.OK("服務已啟動")

	if err := seedSampleForms(ctx, cfg); err != nil {
		// A failed seed should not fail the whole deployment: the system is up
		// and the user can still create forms themselves.
		prompt.Warn("範例表單匯入失敗：%v", err)
		prompt.Hint("系統本身已經可以使用，之後可以再自己建表單。")
	}

	printNextSteps(ctx, cfg)
	return nil
}

// waitReady blocks until the whole path actually serves requests.
//
// `compose up --wait` is not enough: neither backend nor frontend defines a
// healthcheck, so the flag only waits for containers to reach running, which
// says nothing about the Fastify gateway being able to proxy yet. Seeding used
// to fire inside that window and get a connection-closed EOF.
//
// Probing the public entry point rather than the backend's own port also
// confirms the gateway's /api proxy is working.
func waitReady(ctx context.Context, cfg *config.Config) error {
	const timeout = 90 * time.Second
	client := &http.Client{Timeout: 3 * time.Second}
	url := cfg.BaseURL() + "/api/healthz"

	deadline := time.Now().Add(timeout)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服務就緒逾時（%s）。可以用 `core-system-launcher logs` 看發生什麼事", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// seedSampleForms imports the bundled sample forms, all as drafts.
//
// Forms whose titles already exist are skipped entirely, so up stays idempotent
// instead of growing duplicates.
func seedSampleForms(ctx context.Context, cfg *config.Config) error {
	forms, err := seed.Parse(seedFormsJSON)
	if err != nil {
		return err
	}
	if len(forms) == 0 {
		return nil
	}

	uid := lookupAdminUserID(ctx, cfg)
	if uid == "" {
		return errors.New("找不到管理者帳號，可能是 setup 還沒跑完")
	}

	client, err := seed.NewClient(cfg.BaseURL(), uid)
	if err != nil {
		return err
	}
	existing, err := client.ExistingTitles(config.OrgSlug)
	if err != nil {
		return err
	}

	var todo []seed.Form
	for _, f := range forms {
		if !existing[f.Title] {
			todo = append(todo, f)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	prompt.Section("匯入範例表單")
	prompt.Hint("以草稿建立。")
	for i := range todo {
		f := &todo[i]
		if err := client.Seed(config.OrgSlug, f); err != nil {
			return fmt.Errorf("匯入「%s」時失敗：%w", f.Title, err)
		}
		n := 0
		for _, s := range f.Sections {
			n += len(s.Questions)
		}
		prompt.OK("%s %d 個區段 / %d 題", prompt.Pad(f.Title, 26), len(f.Sections), n)
	}
	return nil
}

func setupInteractive(ctx context.Context) (*config.Config, error) {
	prompt.Section("Core System Launcher")
	prompt.Info("這會在你的機器上架一套完整的 Core System（前端、後端、資料庫）。")
	prompt.Info("全部跑在 Docker 裡，不會影響到既有環境。")

	cfg := &config.Config{
		AppPort:      defaultAppPort,
		PostgresPort: defaultPGPort,
	}

	prompt.Section("1 / 3　對外 port")
	for {
		port, err := prompt.AskInt("要用哪個 port？", defaultAppPort)
		if err != nil {
			return nil, err
		}
		if port < 1 || port > 65535 {
			prompt.Warn("port 要介於 1 到 65535")
			continue
		}
		// Validate here; otherwise this only surfaces at preflight and the
		// whole setup has to be redone.
		if !preflight.PortFree(port) {
			prompt.Warn("port %d 已經被佔用了，換一個吧", port)
			prompt.Hint("想知道是誰佔的：lsof -nP -iTCP:%d -sTCP:LISTEN", port)
			continue
		}
		cfg.AppPort = port
		break
	}

	// Postgres is only published for debugging, so pick a free port silently
	// rather than making the user care.
	pgPort, err := preflight.FindFreePort(defaultPGPort)
	if err != nil {
		return nil, err
	}
	cfg.PostgresPort = pgPort
	if pgPort != defaultPGPort {
		prompt.Hint("%d 被佔用了，postgres 改用 %d（僅供除錯連線）", defaultPGPort, pgPort)
	}

	prompt.Section("2 / 3　管理者帳號")
	prompt.Hint("這個 email 會成為初始 organization「%s」的 admin。", config.OrgSlug)
	prompt.Hint("要用 Google 登入的話，請填你的 Google 帳號 email。")
	email, err := prompt.Ask("管理者 email：", "")
	if err != nil {
		return nil, err
	}
	cfg.AdminEmail = strings.ToLower(strings.TrimSpace(email))

	prompt.Section("3 / 3　登入方式")
	choice, err := prompt.Select("要怎麼登入？", []string{
		"設定 Google OAuth（完整功能，需要到 Google Cloud Console 建一組 client）",
		"試用模式（不接 OAuth，直接用內部登入端點進系統看畫面）",
	}, 0)
	if err != nil {
		return nil, err
	}

	if choice == 1 {
		cfg.TrialMode = true
		prompt.Warn("試用模式只適合先看功能，不要用在真的要給人填的環境。")
	} else {
		prompt.Info("請到 Google Cloud Console 建立 OAuth 2.0 Client ID，type 選 Web application：")
		prompt.Info("  https://console.cloud.google.com/apis/credentials")
		prompt.Info("")
		prompt.Info("在 Authorized redirect URIs 貼入這一行（要完全一致，含 port 和路徑）：")
		fmt.Printf("\n      %s\n\n", cfg.GoogleRedirectURI())
		prompt.Hint("若 consent screen 是 External + Testing 狀態，記得把上面填的 email")
		prompt.Hint("加進 Test users，否則登入會被擋成 access_blocked。")
		if err := prompt.Pause("完成後按 Enter 繼續…"); err != nil {
			return nil, err
		}
		id, err := prompt.Ask("Client ID：", "")
		if err != nil {
			return nil, err
		}
		secret, err := prompt.Ask("Client secret：", "")
		if err != nil {
			return nil, err
		}
		cfg.GoogleClientID = strings.TrimSpace(id)
		cfg.GoogleClientSecret = strings.TrimSpace(secret)
	}

	cfg.Secret, err = randomSecret()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func runPreflight(ctx context.Context, cfg *config.Config, allowPortInUse bool) error {
	prompt.Section("環境檢查")
	results := preflight.RunAll(ctx, cfg.AppPort, cfg.PostgresPort)

	running := false
	if allowPortInUse {
		// If the stack is already running, it holding the ports is expected.
		deployDir, err := config.DeployDir()
		if err == nil {
			out, _ := docker.ComposeQuiet(ctx, deployDir, "ps", "-q")
			running = strings.TrimSpace(out) != ""
		}
	}

	fatal := false
	for _, r := range results {
		if r.OK {
			prompt.OK("%s %s", prompt.Pad(r.Name, 26), r.Detail)
			continue
		}
		if running && strings.HasPrefix(r.Name, "port") {
			prompt.OK("%s 由本專案自己使用中", prompt.Pad(r.Name, 26))
			continue
		}
		prompt.Fail("%s %s", prompt.Pad(r.Name, 26), r.Detail)
		if r.Fix != "" {
			prompt.Hint("    %s", r.Fix)
		}
		if r.Fatal {
			fatal = true
		}
	}
	if fatal {
		return errors.New("環境檢查沒有通過，請先處理上面標示的問題")
	}
	return nil
}

func prepareSources(ctx context.Context) error {
	versions, err := patch.Versions(versionsLock)
	if err != nil {
		return err
	}
	srcDir, err := config.SrcDir()
	if err != nil {
		return err
	}
	sub, err := fs.Sub(patchesFS, "patches")
	if err != nil {
		return err
	}

	repos := []patch.Repo{
		{Name: "backend", URL: backendRepoURL, Commit: versions["backend"]},
		{Name: "frontend", URL: frontendRepoURL, Commit: versions["frontend"]},
	}

	prompt.Section("取得原始碼並套用修改")
	for _, r := range repos {
		dest := filepath.Join(srcDir, r.Name)

		// A matching commit alone is not enough: if the last run was interrupted
		// after fetching but before patching, the tree holds clean upstream code
		// and reusing it would build an unpatched version. A marker file records
		// that this commit's patches were applied.
		marker := filepath.Join(dest, ".launcher-patched")
		if done, _ := os.ReadFile(marker); strings.TrimSpace(string(done)) == r.Commit {
			if skipped, err := patch.Fetch(ctx, r, dest); err == nil && skipped {
				prompt.OK("%s 已是 %.8s 且修改已套用，沿用既有內容", prompt.Pad(r.Name, 9), r.Commit)
				continue
			}
		}

		// Getting here means starting over. Wipe first so patches are never
		// applied twice on top of an already-patched tree.
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if _, err := patch.Fetch(ctx, r, dest); err != nil {
			return err
		}
		prompt.OK("%s 取得 %.8s", prompt.Pad(r.Name, 9), r.Commit)

		applied, err := patch.Apply(ctx, sub, r.Name, dest)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			prompt.Hint("          沒有需要套用的修改")
		}
		for _, name := range applied {
			prompt.Hint("          套用 %s", name)
		}
		if err := os.WriteFile(marker, []byte(r.Commit+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func printNextSteps(ctx context.Context, cfg *config.Config) {
	prompt.Section("完成")
	fmt.Printf("\n      %s\n\n", cfg.BaseURL())

	if cfg.TrialMode {
		prompt.Info("目前是試用模式，Google 登入不會通。要進系統的話：")
		prompt.Info("1. 開上面的網址")
		prompt.Info("2. 打開瀏覽器開發者工具的 Console")
		prompt.Info("3. 貼上下面這段並執行，它會用內部端點換一組 cookie：")
		uid := lookupAdminUserID(ctx, cfg)
		if uid == "" {
			fmt.Printf("\n      %s\n\n", `// 找不到管理者的 user id，可先確認 backend log 是否有 "initialized users"`)
		} else {
			fmt.Printf("\n      %s\n\n", trialLoginSnippet(uid))
		}
		prompt.Hint("之後要改成正式登入，執行 reset 再跑一次 up，選 Google OAuth。")
	} else {
		prompt.Info("用 %s 透過 Google 登入即可。", cfg.AdminEmail)
		prompt.Hint("若出現 redirect_uri_mismatch，代表 Google Console 上的 redirect URI")
		prompt.Hint("和下面這一行對不起來：")
		fmt.Printf("\n      %s\n\n", cfg.GoogleRedirectURI())
	}

	prompt.Info("其他常用指令：")
	prompt.Hint("  core-system-launcher logs     追蹤日誌")
	prompt.Hint("  core-system-launcher status   看服務狀態")
	prompt.Hint("  core-system-launcher down     停掉，資料保留")
	prompt.Hint("  core-system-launcher reset    清掉所有資料重來")
}

func trialLoginSnippet(uid string) string {
	return fmt.Sprintf(
		`fetch('/api/auth/login/internal',{method:'POST',headers:{'Content-Type':'application/json'},`+
			`body:JSON.stringify({uid:'%s'}),credentials:'include'}).then(()=>location.href='/orgs/%s/forms')`,
		uid, config.OrgSlug)
}

// lookupAdminUserID reads the admin user id that setup.yaml created out of the
// database. Returns an empty string when it cannot be found; callers decide.
func lookupAdminUserID(ctx context.Context, cfg *config.Config) string {
	deployDir, err := config.DeployDir()
	if err != nil {
		return ""
	}
	out, err := docker.ComposeQuiet(ctx, deployDir, "exec", "-T", "postgres",
		"psql", "-U", "postgres", "-d", "core_system", "-tAc",
		fmt.Sprintf("select user_id from user_emails where lower(value) = lower('%s') limit 1", cfg.AdminEmail))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 36 && strings.Count(line, "-") == 4 {
			return line
		}
	}
	return ""
}

// ── Other commands ───────────────────────────────────────────────

func withDeployDir(fn func(string) error) error {
	deployDir, err := config.DeployDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(deployDir, "compose.yaml")); os.IsNotExist(err) {
		return errors.New("還沒有部署過，請先執行 `core-system-launcher up`")
	}
	return fn(deployDir)
}

func cmdDown(ctx context.Context) error {
	return withDeployDir(func(d string) error {
		if err := docker.Compose(ctx, d, "down"); err != nil {
			return err
		}
		prompt.OK("已停止，資料保留。再次啟動請執行 up。")
		return nil
	})
}

func cmdLogs(ctx context.Context, args []string) error {
	return withDeployDir(func(d string) error {
		return docker.Compose(ctx, d, append([]string{"logs", "-f", "--tail", "100"}, args...)...)
	})
}

func cmdStatus(ctx context.Context) error {
	return withDeployDir(func(d string) error {
		return docker.Compose(ctx, d, "ps")
	})
}

func cmdDoctor(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &config.Config{AppPort: defaultAppPort, PostgresPort: defaultPGPort}
		prompt.Hint("尚未部署過，用預設 port 檢查。")
	}
	prompt.Section("環境檢查")
	for _, r := range preflight.RunAll(ctx, cfg.AppPort, cfg.PostgresPort) {
		if r.OK {
			prompt.OK("%s %s", prompt.Pad(r.Name, 26), r.Detail)
			continue
		}
		prompt.Fail("%s %s", prompt.Pad(r.Name, 26), r.Detail)
		if r.Fix != "" {
			prompt.Hint("    %s", r.Fix)
		}
	}
	return nil
}

func cmdReset(ctx context.Context) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	prompt.Section("重設")
	prompt.Warn("這會刪掉資料庫（所有表單與填答資料）、已下載的原始碼，以及目前的設定。")
	prompt.Hint("位置：%s", dir)
	ok, err := prompt.Confirm("確定要重設嗎？", false)
	if err != nil {
		return err
	}
	if !ok {
		prompt.Info("已取消")
		return nil
	}

	deployDir, err := config.DeployDir()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(deployDir, "compose.yaml")); statErr == nil {
		if err := docker.Compose(ctx, deployDir, "down", "-v"); err != nil {
			prompt.Warn("停止服務時出錯，仍會繼續刪除本機檔案：%v", err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	prompt.OK("已重設，下次執行 up 會重新走一次設定流程。")
	return nil
}
