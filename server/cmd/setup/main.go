// Command setup is an interactive, Claude-Code-style installer for the
// rish-mcp Android agent: it makes sure adb is available, walks the user
// through connecting a device, gets an APK onto it, and installs it.
//
// One thing this tool deliberately does NOT do: drive the Android 11+
// wireless-pairing handshake itself. That handshake happens on-device,
// inside the app (AdbShellClient / libadb-android) -- that's the entire
// point of the app keeping an ADB fallback without a PC in the loop. Shizuku
// is also supported and preferred when the owner grants it. A PC's adb is
// only load-bearing for two things: installing the APK, and the
// pre-Android-11 `adb tcpip` USB bridge (wireless pairing doesn't exist
// before Android 11). This tool covers exactly those two things, then
// hands off to the in-app pairing screen.
package main

import (
	"archive/zip"
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// --- minimal ANSI styling (no TUI framework: this has to run correctly
// whether or not stdin/stdout is a real terminal, since it's tested here
// by piping input non-interactively) ---

const (
	styleReset  = "\x1b[0m"
	styleBold   = "\x1b[1m"
	styleDim    = "\x1b[2m"
	styleAccent = "\x1b[38;5;209m" // roughly matches the site's --accent orange
	styleGood   = "\x1b[32m"
	styleBad    = "\x1b[31m"
)

func colorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

var useColor = colorsEnabled()

func style(s, code string) string {
	if !useColor {
		return s
	}
	return code + s + styleReset
}

func heading(s string) string { return style(s, styleBold+styleAccent) }
func dim(s string) string     { return style(s, styleDim) }
func good(s string) string    { return style("✓ "+s, styleGood) }
func bad(s string) string     { return style("✗ "+s, styleBad) }

func step(n int, title string) {
	fmt.Println()
	fmt.Println(heading(fmt.Sprintf("Step %d — %s", n, title)))
}

// --- stdin prompts ---

var stdin = bufio.NewReader(os.Stdin)

// nonInteractive: for an agent bootstrapping a fresh machine (-yes / -y /
// RISH_MCP_YES=1). -action picks the menu entry; every prompt below takes
// its default instead of blocking on stdin -- see prompt().
var nonInteractive bool

// Test seams: runDeviceSetup's device-poll loop normally sleeps ~2s between
// attempts and gives up after 15. Tests shrink both so the loop runs fast.
var devicePollInterval = 2 * time.Second
var devicePollAttempts = 15

func prompt(label string) string {
	if nonInteractive {
		// promptDefault/promptYesNo both route through here and treat ""
		// as "take the default"; every bare prompt() call site already
		// treats "" as "skip this optional value" -- so short-circuiting
		// here alone makes the whole flow non-blocking.
		fmt.Println(label + " " + dim("(non-interactive, skipped)"))
		return ""
	}
	fmt.Print(label + " ")
	line, err := stdin.ReadString('\n')
	if err != nil {
		fmt.Println()
		fmt.Println(bad("input closed"))
		os.Exit(1)
	}
	return strings.TrimSpace(line)
}

func promptDefault(label, def string) string {
	v := prompt(fmt.Sprintf("%s %s", label, dim("["+def+"]")))
	if v == "" {
		return def
	}
	return v
}

func promptYesNo(label string, def bool) bool {
	suffix := "[Y/n]"
	if !def {
		suffix = "[y/N]"
	}
	v := strings.ToLower(strings.TrimSpace(prompt(label + " " + dim(suffix))))
	if v == "" {
		return def
	}
	return v == "y" || v == "yes"
}

// --- main flow ---

func configuredServerURL() string {
	return os.Getenv("RISH_MCP_SERVER")
}

func main() {
	// Legacy GitHub releases contain the Shizuku implementation. Fail closed
	// and build the rewrite locally unless the operator explicitly configures
	// a compatible version server.
	defaultServer := configuredServerURL()
	serverURL := flag.String("server", defaultServer, "base URL of an explicitly trusted rish-mcp version server (unset builds locally)")
	yes := flag.Bool("yes", false, "non-interactive: take every prompt's default, run one -action and exit (for agents/scripts)")
	flag.BoolVar(yes, "y", false, "shorthand for -yes")
	action := flag.String("action", "setup", "which menu entry to run in -yes mode: setup, apk, or relay")
	flag.Parse()
	nonInteractive = *yes || os.Getenv("RISH_MCP_YES") == "1"

	fmt.Println(heading("rish-mcp setup"))
	fmt.Println(dim("adb + pairing + build + relay, all from one place."))

	for {
		var choice string
		if nonInteractive {
			switch *action {
			case "setup":
				choice = "1"
			case "apk":
				choice = "2"
			case "relay":
				choice = "3"
			default:
				fmt.Println(bad("-action must be one of: setup, apk, relay"))
				os.Exit(1)
			}
			fmt.Println(dim("(non-interactive) → action=" + *action))
		} else {
			fmt.Println()
			fmt.Println(heading("What do you want to do?"))
			fmt.Println("  1) Full device setup -- adb, pairing, install the app")
			fmt.Println("  2) Just build/download the APK")
			fmt.Println("  3) Start a relay server")
			fmt.Println("  4) Exit")
			choice = promptDefault("Choice", "1")
		}

		var err error
		switch choice {
		case "1":
			err = runDeviceSetup(*serverURL)
		case "2":
			err = runBuildAPKOnly(*serverURL)
		case "3":
			err = runStartRelay()
		case "4":
			return
		default:
			fmt.Println(bad("not a valid choice"))
			continue
		}
		if err != nil {
			fmt.Println(bad(err.Error()))
			if nonInteractive {
				os.Exit(1)
			}
		}

		// Non-interactive mode does exactly the one -action and stops --
		// there's nobody to ask "back to the menu?".
		if nonInteractive {
			return
		}
		if !promptYesNo("Back to the menu?", true) {
			return
		}
	}
}

func runDeviceSetup(serverURL string) error {
	adbPath, err := ensureADB()
	if err != nil {
		return fmt.Errorf("couldn't get adb: %w", err)
	}
	fmt.Println(good("adb ready: " + adbPath))

	// Fail fast: if there's no way to get an APK later, don't waste the
	// user's time on device setup first.
	if serverURL == "" {
		if _, err := exec.LookPath("docker"); err != nil {
			fmt.Println(bad("no way to get an APK: Docker isn't installed, and no -server / RISH_MCP_SERVER is set."))
			fmt.Println(dim("Either install Docker Desktop (used to build the app without needing the Android SDK locally),"))
			fmt.Println(dim("or pass -server <url> to download a prebuilt APK instead."))
			return nil
		}
	}

	step(1, "connect your device")
	fmt.Println("Plug the phone in over USB and enable USB debugging (Settings → Developer")
	fmt.Println("options → USB debugging), or make sure it's already reachable over adb.")
	deviceFound := false
	for attempt := 0; !nonInteractive || attempt < devicePollAttempts; attempt++ { // ~30s of polling, not a busy-loop, in agent mode
		if nonInteractive {
			if attempt > 0 {
				time.Sleep(devicePollInterval)
			}
		} else {
			prompt(dim("press enter once it's connected"))
		}
		devices, err := listDevices(adbPath)
		if err != nil {
			fmt.Println(bad(err.Error()))
			continue
		}
		if len(devices) == 0 {
			fmt.Println(bad("no device seen by `adb devices` yet -- check the USB debugging prompt on the phone"))
			continue
		}
		fmt.Println(good(fmt.Sprintf("found device: %s", devices[0])))
		deviceFound = true
		break
	}
	if !deviceFound {
		return fmt.Errorf("no device showed up on `adb devices` after %d attempts (non-interactive mode doesn't wait forever)", devicePollAttempts)
	}

	step(2, "Android version")
	is11Plus := promptYesNo("Is the device on Android 11 or newer?", true)
	bridgePort := ""
	if !is11Plus {
		fmt.Println(dim("Wireless pairing doesn't exist before Android 11, so we bridge over USB instead."))
		bridgePort = promptDefault("TCP port for the adb bridge", "5555")
		out, err := exec.Command(adbPath, "tcpip", bridgePort).CombinedOutput()
		if err != nil {
			return fmt.Errorf("adb tcpip failed: %s", strings.TrimSpace(string(out)))
		}
		fmt.Println(good(fmt.Sprintf("adbd now listening on port %s -- you can unplug the USB cable", bridgePort)))
	}

	step(3, "get the APK")
	apkPath, err := acquireAPK(serverURL)
	if err != nil {
		return err
	}
	fmt.Println(good("APK ready: " + apkPath))

	step(4, "install")
	out, err := exec.Command(adbPath, "install", "-r", apkPath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Success") {
		return fmt.Errorf("adb install failed:\n%s", string(out))
	}
	fmt.Println(good("installed"))

	step(5, "configure the app")
	fmt.Println(dim("These get sent to the app as launch extras, not baked into the build --"))
	fmt.Println(dim("see docs/USAGE.md §3.4 (headless provisioning)."))
	relayURL := prompt("Relay URL (e.g. wss://mcp.example.com/agent):")
	deviceToken := prompt("Device token:")
	if relayURL != "" && deviceToken != "" {
		args := []string{
			"shell", "am", "start", "-n", "kr.scin.rishmcp/.ProvisioningActivity",
			"--es", "relay", relayURL,
			"--es", "token", deviceToken,
			"--ez", "autostart", "true",
		}
		if bridgePort != "" {
			args = append(args, "--ei", "adbPort", bridgePort)
		}
		out, err := exec.Command(adbPath, args...).CombinedOutput()
		if err != nil {
			fmt.Println(bad("am start failed: " + strings.TrimSpace(string(out))))
		} else {
			fmt.Println(good("app launched with relay + token pre-filled"))
		}
	} else {
		fmt.Println(dim("skipped -- you can fill these in from the app's Configuration card instead"))
	}

	step(6, "authorize shell access")
	fmt.Println("Recommended: start Shizuku in ADB mode, then tap Grant Shizuku in the")
	fmt.Println("app's Shell access card. rish-mcp deliberately rejects root-mode Shizuku.")
	fmt.Println("If you prefer the built-in ADB fallback:")
	if is11Plus {
		fmt.Println("On the phone: Settings → Developer options → Wireless debugging → Pair device")
		fmt.Println("with pairing code. Enter that port + 6-digit code in the app's \"Shell access\"")
		fmt.Println("card, tap Pair. Then note the (different) port on the main Wireless")
		fmt.Println("debugging screen, enter it under \"Connect port\", tap Save port.")
	} else {
		fmt.Println(fmt.Sprintf("The bridge port (%s) is already listening and, if you filled in step 5,", bridgePort))
		fmt.Println("already saved as the app's Connect port.")
	}
	if relayURL == "" || deviceToken == "" {
		fmt.Println("Fill in Relay URL + Device token in the Configuration card and tap Save & Start.")
	}
	fmt.Println()
	fmt.Println(good("done"))
	return nil
}

// runBuildAPKOnly is runDeviceSetup's step 3 on its own, for someone who
// just wants an APK (e.g. to hand-install, or to check a build works)
// without going through pairing.
func runBuildAPKOnly(serverURL string) error {
	apkPath, err := acquireAPK(serverURL)
	if err != nil {
		return err
	}
	fmt.Println(good("APK ready: " + apkPath))
	return nil
}

// runStartRelay launches server/cmd/relay in the foreground, streaming its
// logs, until the user kills it (Ctrl+C) or it exits on its own. AI_TOKEN/
// DEVICE_TOKEN are required by the relay (see cmd/relay/main.go) -- offers
// to generate random ones rather than requiring the user already have some.
const relayImage = "ghcr.io/turin-dev/rish-mcp-relay:latest"

func runStartRelay() error {
	// No local checkout is fine -- we fall back to the prebuilt image
	// below instead of requiring one just to run the relay.
	repoRoot, _ := findRepoRoot()

	aiToken := os.Getenv("AI_TOKEN")
	if aiToken == "" {
		if promptYesNo("No AI_TOKEN set. Generate a random one?", true) {
			aiToken = randomToken()
			fmt.Println(dim("AI_TOKEN=" + aiToken))
		} else {
			aiToken = prompt("AI_TOKEN:")
		}
	}
	deviceToken := os.Getenv("DEVICE_TOKEN")
	if deviceToken == "" {
		if promptYesNo("No DEVICE_TOKEN set. Generate a random one?", true) {
			deviceToken = randomToken()
			fmt.Println(dim("DEVICE_TOKEN=" + deviceToken))
		} else {
			deviceToken = prompt("DEVICE_TOKEN:")
		}
	}
	if aiToken == "" || deviceToken == "" {
		return fmt.Errorf("AI_TOKEN and DEVICE_TOKEN are both required")
	}
	port := promptDefault("Port", "8080")

	env := append(os.Environ(),
		"AI_TOKEN="+aiToken,
		"DEVICE_TOKEN="+deviceToken,
		"PORT="+port,
	)

	var cmd *exec.Cmd
	if repoRoot != "" {
		serverDir := filepath.Join(repoRoot, "server")
		if _, err := exec.LookPath("go"); err == nil {
			fmt.Println(dim("go run ./cmd/relay"))
			cmd = exec.Command("go", "run", "./cmd/relay")
			cmd.Dir = serverDir
		} else if _, err := exec.LookPath("docker"); err == nil {
			fmt.Println(dim("docker build --target relay -t rishmcp-relay " + serverDir))
			build := exec.Command("docker", "build", "--target", "relay", "-t", "rishmcp-relay", ".")
			build.Dir = serverDir
			build.Stdout = os.Stdout
			build.Stderr = os.Stderr
			if err := build.Run(); err != nil {
				return fmt.Errorf("docker build failed: %w", err)
			}
			cmd = exec.Command("docker", "run", "--rm", "-p", port+":"+port,
				"-e", "AI_TOKEN="+aiToken, "-e", "DEVICE_TOKEN="+deviceToken, "-e", "PORT="+port,
				"rishmcp-relay")
		}
	}

	if cmd == nil {
		// No local checkout (or no go/docker to build it with) -- fall back
		// to the prebuilt image, which needs neither.
		if _, err := exec.LookPath("docker"); err != nil {
			if repoRoot != "" {
				return fmt.Errorf("neither go nor docker found -- install one to run the relay")
			}
			return fmt.Errorf("no local checkout and docker not found -- install Docker to run the prebuilt relay image, or clone the repo and install Go")
		}
		if repoRoot == "" {
			fmt.Println(dim("No local checkout found -- pulling the prebuilt image instead."))
		}
		fmt.Println(dim("docker pull " + relayImage))
		pull := exec.Command("docker", "pull", relayImage)
		pull.Stdout = os.Stdout
		pull.Stderr = os.Stderr
		if err := pull.Run(); err != nil {
			return fmt.Errorf("docker pull failed: %w", err)
		}
		cmd = exec.Command("docker", "run", "--rm", "-p", port+":"+port,
			"-e", "AI_TOKEN="+aiToken, "-e", "DEVICE_TOKEN="+deviceToken, "-e", "PORT="+port,
			relayImage)
	}

	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	fmt.Println(good("starting relay on :" + port + " -- Ctrl+C to stop"))
	return cmd.Run()
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the OS RNG is broken; nothing sane to fall back to
	}
	return hex.EncodeToString(b)
}

// --- adb discovery / install ---

func ensureADB() (string, error) {
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}
	cacheDir, err := platformToolsCacheDir()
	if err != nil {
		return "", err
	}
	cached := filepath.Join(cacheDir, adbBinaryName())
	if _, err := os.Stat(cached); err == nil {
		return cached, nil
	}
	fmt.Println(dim("adb not found on PATH."))
	if !promptYesNo("Download Android platform-tools now?", true) {
		return "", fmt.Errorf("adb is required")
	}
	return downloadPlatformTools(cacheDir)
}

func adbBinaryName() string {
	if runtime.GOOS == "windows" {
		return filepath.Join("platform-tools", "adb.exe")
	}
	return filepath.Join("platform-tools", "adb")
}

func platformToolsCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".rish-mcp")
	return dir, os.MkdirAll(dir, 0o755)
}

func platformToolsURL() (string, error) {
	switch runtime.GOOS {
	case "windows":
		return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip", nil
	case "darwin":
		return "https://dl.google.com/android/repository/platform-tools-latest-darwin.zip", nil
	case "linux":
		return "https://dl.google.com/android/repository/platform-tools-latest-linux.zip", nil
	default:
		return "", fmt.Errorf("unsupported OS %q -- install adb yourself and re-run", runtime.GOOS)
	}
}

// platformToolsURLFunc is a test seam: it defaults to platformToolsURL but
// tests can swap it for a local server to exercise downloadPlatformTools
// end-to-end without touching dl.google.com.
var platformToolsURLFunc = platformToolsURL

func downloadPlatformTools(cacheDir string) (string, error) {
	url, err := platformToolsURLFunc()
	if err != nil {
		return "", err
	}
	fmt.Println(dim("downloading " + url))
	zipPath := filepath.Join(cacheDir, "platform-tools.zip")
	if err := downloadFile(url, zipPath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(zipPath)
	if err := unzip(zipPath, cacheDir); err != nil {
		return "", fmt.Errorf("extract failed: %w", err)
	}
	adbPath := filepath.Join(cacheDir, adbBinaryName())
	if runtime.GOOS != "windows" {
		_ = os.Chmod(adbPath, 0o755)
	}
	if _, err := os.Stat(adbPath); err != nil {
		return "", fmt.Errorf("adb missing from extracted archive: %w", err)
	}
	return adbPath, nil
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func unzip(src, destDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func listDevices(adbPath string) ([]string, error) {
	out, err := exec.Command(adbPath, "devices").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("adb devices failed: %w", err)
	}
	var devices []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "device" {
			devices = append(devices, fields[0])
		}
	}
	return devices, nil
}

// --- APK acquisition ---

func acquireAPK(serverURL string) (string, error) {
	cacheDir, err := platformToolsCacheDir()
	if err != nil {
		return "", err
	}

	if serverURL == "" {
		fmt.Println(dim("No --server / RISH_MCP_SERVER set -- building locally instead."))
		return buildLocally()
	}

	fmt.Println("A version server is configured: " + serverURL)
	if !promptYesNo("Download the official prebuilt APK from it?", true) {
		return buildLocally()
	}
	apkURL := strings.TrimRight(serverURL, "/") + "/agent.apk"
	dest := filepath.Join(cacheDir, "rish-mcp-agent.apk")
	fmt.Println(dim("downloading " + apkURL))
	if err := downloadFile(apkURL, dest); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	return dest, nil
}

func buildLocally() (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker not found -- install Docker Desktop, or pass -server to download a prebuilt APK instead")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(repoRoot, "app")
	fmt.Println(dim("docker build -t rishmcp-android-build -f Dockerfile.build " + appDir))
	build := exec.Command("docker", "build", "-t", "rishmcp-android-build", "-f", "Dockerfile.build", ".")
	build.Dir = appDir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("docker build failed: %w", err)
	}

	outDir := filepath.Join(appDir, "app", "build", "outputs", "apk", "debug")
	fmt.Println(dim("running the build inside the image and copying the APK out"))
	run := exec.Command("docker", "run", "--rm", "-v", appDir+":/work",
		"rishmcp-android-build", "bash", "-c",
		"cd /work && gradle assembleDebug --no-daemon")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		return "", fmt.Errorf("gradle build failed: %w", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "", fmt.Errorf("no build output at %s: %w", outDir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".apk") {
			return filepath.Join(outDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .apk found in %s", outDir)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "app", "Dockerfile.build")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("couldn't find repo root (looking for app/Dockerfile.build) -- run this from inside the rish-mcp checkout")
		}
		dir = parent
	}
}
