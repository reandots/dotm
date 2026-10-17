package installed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dotm/internal/manifest"
)

// DoctorFinding describes one independently checked prerequisite or blocked check.
type DoctorFinding struct {
	Check    string
	Message  string
	Remedy   string
	Severity Severity
	Blocked  bool
}

// Diagnose reports read-only host and session readiness. Session checks describe
// only the environment inherited by this process, not a fresh login session.
func Diagnose(ctx context.Context) (findings []DoctorFinding, executable string) {
	add := func(check, message, remedy string, severity Severity, blocked bool) {
		finding := DoctorFinding{
			Check: check, Message: message, Remedy: remedy,
			Severity: severity, Blocked: blocked,
		}
		findings = append(findings, finding)
	}
	nix, nixErr := exec.LookPath("nix")
	if nixErr != nil {
		add("nix", "Nix executable unavailable",
			"Prepare Nix through host provisioning (Ansible); doctor does not install it.", 3, false)
		add("nix-config", "effective configuration check requires Nix", "", 0, true)
		add("nix-daemon", "daemon connectivity check requires Nix", "", 0, true)
	} else {
		out, err := exec.CommandContext(ctx, nix, "config", "show", "--json").Output()
		if err != nil {
			message := fmt.Sprintf("cannot read effective Nix configuration: %s", commandError(err))
			add("nix-config", message, "Correct the host Nix configuration.", 3, false)
		} else {
			var config map[string]json.RawMessage
			if decodeErr := json.Unmarshal(out, &config); decodeErr != nil {
				message := fmt.Sprintf("invalid effective Nix configuration JSON: %v", decodeErr)
				add("nix-config", message, "Correct the host Nix configuration.", 3, false)
			} else {
				for _, key := range []string{"experimental-features", "use-xdg-base-directories"} {
					raw := config[key]
					var value any
					if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
						message := "effective Nix configuration is missing or invalid " + key
						remedy := "Enable flakes, nix-command, and XDG base directories in host Nix configuration."
						add("nix-config", message, remedy, 3, false)
						continue
					}
					if wrapped, ok := value.(map[string]any); ok {
						value = wrapped["value"]
					}
					if !requiredNixSetting(key, value) {
						message := "effective Nix configuration does not enable required " + key
						remedy := "Enable flakes, nix-command, and XDG base directories in host Nix configuration."
						add("nix-config", message, remedy, 3, false)
					}
				}
			}
		}
		out, err = exec.CommandContext(ctx, nix, "store", "info", "--json").Output()
		var store struct {
			URL string `json:"url"`
		}
		if err == nil {
			err = json.Unmarshal(out, &store)
		}
		if err == nil && store.URL != "daemon" && !strings.HasPrefix(store.URL, "unix://") {
			err = fmt.Errorf("configured store %q is not the daemon", store.URL)
		}
		if err != nil {
			message := fmt.Sprintf("ordinary-user daemon store operation failed: %s", commandError(err))
			remedy := "Prepare ordinary-user access to the Nix daemon through host provisioning."
			add("nix-daemon", message, remedy, 3, false)
		} else {
			add("nix-daemon", "ordinary-user Nix daemon store operation succeeded", "", 0, false)
		}
	}
	if home, err := os.UserHomeDir(); err != nil || !filepath.IsAbs(home) {
		add("home", "HOME must resolve to an absolute path",
			"Set HOME to the user's absolute home directory.", 1, false)
	}
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" && !filepath.IsAbs(cache) {
		add("cache", "XDG_CACHE_HOME must be absolute",
			"Set XDG_CACHE_HOME to an absolute path in the user session.", 1, false)
	}
	base, baseErr := stateHome()
	if baseErr == nil {
		if pendingErr := CheckPendingRecovery(); pendingErr != nil {
			add("package recovery", pendingErr.Error(),
				"Correct the prerequisite and resume a supported package operation; doctor never recovers.", 3, false)
		}
	}
	profile := ""
	if baseErr != nil {
		remedy := "Set an absolute XDG_STATE_HOME or correct the home directory."
		add("profile", baseErr.Error(), remedy, 1, false)
	} else {
		profile = filepath.Join(base, "nix", "profile")
		exists, err := profileExists(profile)
		switch {
		case err != nil:
			message := fmt.Sprintf("cannot inspect native profile: %v", err)
			add("profile", message, "Repair profile path permissions or dangling links.", 3, false)
		case !exists:
			add("profile", "standard native profile is absent (first use)",
				"Run init after host prerequisites are prepared.", 0, false)
		default:
			manifestErr := validateNativeManifest(profile)
			if manifestErr != nil {
				add("profile", fmt.Sprintf("native profile manifest is unusable: %v", manifestErr),
					"Repair the native Nix profile; doctor does not recover it.", 3, false)
			} else if nixErr != nil {
				add("profile", "native profile inspection blocked because Nix is unavailable",
					"Prepare Nix through host provisioning.", 0, true)
			} else if _, inspectErr := Inspect(ctx, manifest.Resolved{}); inspectErr != nil {
				add("profile", fmt.Sprintf("native profile is unusable: %v", inspectErr),
					"Repair the native Nix profile; doctor does not recover it.", 3, false)
			} else {
				add("profile", "native profile is readable", "", 0, false)
			}
		}
	}
	paths := filepath.SplitList(os.Getenv("PATH"))
	if nixErr == nil && !containsClean(paths, filepath.Dir(nix)) {
		message := "current process PATH does not include the resolved Nix executable directory"
		remedy := "Prepare the login/session environment; this check covers only the current process."
		add("session-path", message, remedy, 3, false)
	}
	if profile != "" {
		remedy := "Prepare the login/session environment; this check covers only the current process."
		binDir := filepath.Join(profile, "bin")
		if !containsClean(paths, binDir) {
			add("profile-path", "current process PATH does not include profile bin directory "+binDir, remedy, 3, false)
		}
		dataDirs := filepath.SplitList(os.Getenv("XDG_DATA_DIRS"))
		if len(dataDirs) == 0 {
			dataDirs = []string{"/usr/local/share", "/usr/share"}
		}
		shareDir := filepath.Join(profile, "share")
		if !containsClean(dataDirs, shareDir) {
			add("profile-data", "current process XDG_DATA_DIRS does not include profile share directory "+shareDir,
				remedy, 3, false)
		}
	} else {
		add("profile-session", "profile bin/share checks require a valid state directory", "", 0, true)
	}
	git, gitErr := exec.LookPath("git")
	if gitErr != nil {
		add("git", "Git executable unavailable", "Install Git through host provisioning.", 3, false)
	} else if err := probeGit(ctx, git); err != nil {
		add("git", fmt.Sprintf("Git operational compatibility failed: %v", err),
			"Repair the Git installation.", 3, false)
	} else {
		add("git", "Git is operational", "", 0, false)
	}
	selected, backendErr := resolveOperationalChezmoi(ctx)
	if backendErr != nil {
		add("chezmoi", backendErr.Error(), "Repair the selected backend.", 3, false)
	} else if selected == "" {
		add("chezmoi", "chezmoi executable unavailable",
			"Run dotm init to acquire chezmoi after host prerequisites are prepared.", 3, false)
	} else {
		executable = selected
		add("chezmoi", "chezmoi is operational and compatible", "", 0, false)
	}
	return findings, executable
}

func commandError(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return err.Error()
}

func requiredNixSetting(key string, value any) bool {
	if key == "experimental-features" {
		var features []string
		switch configured := value.(type) {
		case string:
			features = strings.Fields(configured)
		case []any:
			for _, feature := range configured {
				if text, ok := feature.(string); ok {
					features = append(features, text)
				}
			}
		}
		return contains(features, "flakes") && contains(features, "nix-command")
	}
	if key == "use-xdg-base-directories" {
		enabled, ok := value.(bool)
		return ok && enabled
	}
	return false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsClean(values []string, wanted string) bool {
	for _, value := range values {
		if filepath.Clean(value) == filepath.Clean(wanted) {
			return true
		}
	}
	return false
}

func probeGit(ctx context.Context, git string) error {
	root, err := os.MkdirTemp("", "dotm-git-probe-")
	if err != nil {
		return fmt.Errorf("prepare isolated Git probe: %w", err)
	}
	defer os.RemoveAll(root)

	home := filepath.Join(root, "home")
	if homeErr := os.Mkdir(home, 0o700); homeErr != nil {
		return fmt.Errorf("prepare isolated Git home: %w", homeErr)
	}
	repository := filepath.Join(root, "repository.git")
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
	}
	init := exec.CommandContext(ctx, git, "-c", "init.defaultBranch=main", "init", "--bare", "--quiet", repository)
	init.Env = env
	if output, initErr := init.CombinedOutput(); initErr != nil {
		return fmt.Errorf("git init --bare: %w: %s", initErr, strings.TrimSpace(string(output)))
	}
	check := exec.CommandContext(ctx, git, "-C", repository, "rev-parse", "--is-bare-repository")
	check.Env = env
	output, err := check.Output()
	if err != nil {
		return fmt.Errorf("git rev-parse: %w", err)
	}
	if strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("git rev-parse returned unsupported result %q", strings.TrimSpace(string(output)))
	}
	return nil
}

func validateNativeManifest(profile string) error {
	path := filepath.Join(profile, "manifest.json")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("manifest is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	system, err := nixSystem()
	if err != nil {
		return err
	}
	_, err = parseEntries(data, system, "")
	return err
}
