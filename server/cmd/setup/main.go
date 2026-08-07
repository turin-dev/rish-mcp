// Command setup is an interactive, Claude-Code-style installer for the
// rish-mcp Android agent: it makes sure adb is available, walks the user
// through connecting a device, gets an APK onto it, and installs it.
//
// One thing this tool deliberately does NOT do: drive the Android 11+
// wireless-pairing handshake itself. That handshake happens on-device,
// inside the app (AdbShellClient / libadb-android) -- that's the entire
// point of the app not needing Shizuku or a PC in the loop. A PC's adb is
// only load-bearing for two things: installing the APK, and the
// pre-Android-11 `adb tcpip` USB bridge (wireless pairing doesn't exist
// before Android 11). This tool covers exactly those two things, then
// hands off to the in-app pairing screen.
package main

import (
	"archive/zip"
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func prompt(label string) string {
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

func main() {
	serverURL := flag.String("server", os.Getenv("RISH_MCP_SERVER"), "base URL of the official rish-mcp version server (optional; if unset, you'll build locally)")
	flag.Parse()

	fmt.Println(heading("rish-mcp setup"))
	fmt.Println(dim("Gets the Android agent onto your device: adb, the APK, and adb install."))
	fmt.Println(dim("Wireless pairing itself happens in the app afterwards -- see the last step."))

	adbPath, err := ensureADB()
	if err != nil {
		fmt.Println(bad("couldn't get adb: " + err.Error()))
		os.Exit(1)
	}
	fmt.Println(good("adb ready: " + adbPath))

	// Fail fast: if there's no way to get an APK later, don't waste the
	// user's time on device setup first.
	if *serverURL == "" {
		if _, err := exec.LookPath("docker"); err != nil {
			fmt.Println(bad("no way to get an APK: Docker isn't installed, and no -server / RISH_MCP_SERVER is set."))
			fmt.Println(dim("Either install Docker Desktop (used to build the app without needing the Android SDK locally),"))
			fmt.Println(dim("or pass -server <url> to download a prebuilt APK instead."))
			os.Exit(1)
		}
	}

	step(1, "connect your device")
	fmt.Println("Plug the phone in over USB and enable USB debugging (Settings → Developer")
	fmt.Println("options → USB debugging), or make sure it's already reachable over adb.")
	for {
		prompt(dim("press enter once it's connected"))
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
		break
	}

	step(2, "Android version")
	is11Plus := promptYesNo("Is the device on Android 11 or newer?", true)
	bridgePort := ""
	if !is11Plus {
		fmt.Println(dim("Wireless pairing doesn't exist before Android 11, so we bridge over USB instead."))
		bridgePort = promptDefault("TCP port for the adb bridge", "5555")
		out, err := exec.Command(adbPath, "tcpip", bridgePort).CombinedOutput()
		if err != nil {
			fmt.Println(bad("adb tcpip failed: " + strings.TrimSpace(string(out))))
			os.Exit(1)
		}
		fmt.Println(good(fmt.Sprintf("adbd now listening on port %s -- you can unplug the USB cable", bridgePort)))
	}

	step(3, "get the APK")
	apkPath, err := acquireAPK(*serverURL)
	if err != nil {
		fmt.Println(bad(err.Error()))
		os.Exit(1)
	}
	fmt.Println(good("APK ready: " + apkPath))

	step(4, "install")
	out, err := exec.Command(adbPath, "install", "-r", apkPath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Success") {
		fmt.Println(bad("adb install failed:\n" + string(out)))
		os.Exit(1)
	}
	fmt.Println(good("installed"))

	step(5, "configure the app")
	fmt.Println(dim("These get sent to the app as launch extras, not baked into the build --"))
	fmt.Println(dim("see docs/USAGE.md §3.3 (headless provisioning)."))
	relayURL := prompt("Relay URL (e.g. wss://mcp.example.com/agent):")
	deviceToken := prompt("Device token:")
	if relayURL != "" && deviceToken != "" {
		args := []string{
			"shell", "am", "start", "-n", "kr.scin.rishmcp/.MainActivity",
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

	step(6, "pair the app")
	if is11Plus {
		fmt.Println("On the phone: Settings → Developer options → Wireless debugging → Pair device")
		fmt.Println("with pairing code. Enter that port + 6-digit code in the app's \"ADB shell")
		fmt.Println("access\" card, tap Pair. Then note the (different) port on the main Wireless")
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

func downloadPlatformTools(cacheDir string) (string, error) {
	url, err := platformToolsURL()
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
	if err := ensureGoogleServicesJSON(appDir); err != nil {
		return "", err
	}
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

// ensureGoogleServicesJSON checks for app/app/google-services.json, the
// file that turns on the FCM low-spec wake path (build.gradle.kts only
// applies the Firebase Gradle plugin when it's present -- otherwise the
// build still succeeds, just without that feature). It's real per-project
// Firebase config, gitignored on purpose (docs/DESIGN.md §7), so each
// local build has to supply its own rather than finding one in the repo.
func ensureGoogleServicesJSON(appDir string) error {
	target := filepath.Join(appDir, "app", "google-services.json")
	if _, err := os.Stat(target); err == nil {
		fmt.Println(good("google-services.json found -- FCM wake path will be built in"))
		return nil
	}

	fmt.Println(dim("No app/app/google-services.json -- building without the FCM low-spec wake path."))
	fmt.Println(dim("(Regular devices work fine without it; this only affects Wear OS-style wake.)"))
	if !promptYesNo("Do you have your own Firebase project's google-services.json to add?", false) {
		return nil
	}
	src := prompt("Path to it:")
	if src == "" {
		return nil
	}
	if err := copyFile(src, target); err != nil {
		return fmt.Errorf("couldn't copy google-services.json: %w", err)
	}
	fmt.Println(good("copied -- FCM wake path will be built in"))
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
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
