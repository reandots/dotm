package chezmoi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProbeExecutable checks that executable supports the read-only command and JSON
// formats used by dotm, without consulting or modifying the user's chezmoi state.
func ProbeExecutable(ctx context.Context, executable string) error {
	root, err := os.MkdirTemp("", "dotm-chezmoi-probe-")
	if err != nil {
		return fmt.Errorf("prepare isolated chezmoi compatibility probe: %w", err)
	}
	defer os.RemoveAll(root)
	fail := func(cause error) error {
		return fmt.Errorf("chezmoi %q is incompatible with dotm; repair/remove this candidate before setup: %w",
			executable, cause)
	}
	home, source := filepath.Join(root, "home"), filepath.Join(root, "source")
	config := filepath.Join(root, "config.toml")
	for _, path := range []string{home, source} {
		if mkdirErr := os.MkdirAll(path, 0o700); mkdirErr != nil {
			return fail(mkdirErr)
		}
	}
	for path, content := range map[string]string{
		config: "mode = \"file\"\n",
		filepath.Join(source, "dot_dotm-bootstrap-probe"):   "probe\n",
		filepath.Join(source, "dot_dotm-bootstrap-ignored"): "ignored\n",
		filepath.Join(source, ".chezmoiignore"):             ".dotm-bootstrap-ignored\n",
	} {
		if writeErr := os.WriteFile(path, []byte(content), 0o600); writeErr != nil {
			return fail(writeErr)
		}
	}
	inherited := os.Environ()
	environment := inherited[:0]
	for _, value := range inherited {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "XDG_") &&
			!strings.HasPrefix(value, "CHEZMOI_") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"))
	ctx = context.WithValue(WithExecutable(ctx, executable), commandEnvironmentKey{}, environment)
	command := applyCommand{source: source}
	run := func(args ...string) ([]byte, error) {
		flags := []string{"--config", config, "--destination", home}
		data, message, runErr := command.run(ctx, append(flags, args...)...)
		if runErr != nil {
			return nil, fail(fmt.Errorf("%s: %w: %s", args[0], runErr, message))
		}
		return data, nil
	}
	data, err := run("dump-config", "--format=json")
	if err != nil {
		return err
	}
	var settings struct {
		DestDir string `json:"destDir"`
		Mode    string `json:"mode"`
	}
	if decodeErr := json.Unmarshal(data, &settings); decodeErr != nil {
		return fail(fmt.Errorf("decode dump-config: %w", decodeErr))
	}
	if settings.DestDir != home || settings.Mode != "file" {
		return fail(fmt.Errorf("dump-config returned unsupported destination/mode"))
	}
	data, err = run("dump", "--format=json")
	if err != nil {
		return err
	}
	var state map[string]targetEntry
	if decodeErr := json.Unmarshal(data, &state); decodeErr != nil {
		return fail(fmt.Errorf("decode target state: %w", decodeErr))
	}
	if len(state) != 1 || state[".dotm-bootstrap-probe"].Type != "file" {
		return fail(fmt.Errorf("dump returned unsupported target state or ignore behavior"))
	}
	data, err = run("ignored", "--nul-path-separator")
	if err != nil {
		return err
	}
	if string(data) != ".dotm-bootstrap-ignored\x00" {
		return fail(fmt.Errorf("ignored returned unsupported NUL-separated paths"))
	}
	data, err = run("--override-data", `{"dotm":{"probe":"ok"}}`, "execute-template",
		`{{ dict "sourceDir" .chezmoi.sourceDir "destDir" .chezmoi.destDir `+
			`"configFile" .chezmoi.configFile "probe" .dotm.probe | toJson }}`)
	if err != nil {
		return err
	}
	var binding struct {
		SourceDir  string `json:"sourceDir"`
		DestDir    string `json:"destDir"`
		ConfigFile string `json:"configFile"`
		Probe      string `json:"probe"`
	}
	if decodeErr := json.Unmarshal(data, &binding); decodeErr != nil {
		return fail(fmt.Errorf("decode template metadata: %w", decodeErr))
	}
	if binding.SourceDir != source || binding.DestDir != home || binding.ConfigFile != config || binding.Probe != "ok" {
		return fail(fmt.Errorf("execute-template returned unsupported policy metadata/override data"))
	}
	return nil
}
