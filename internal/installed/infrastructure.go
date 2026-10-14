package installed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const infrastructureDescriptor = "chezmoi"

type infrastructureState struct {
	Descriptor string `json:"descriptor"`
	Source     string `json:"source"`
	Pending    bool   `json:"pending"`
}

func readInfrastructure(base string) (infrastructureState, error) {
	path := filepath.Join(base, "dotm", "infrastructure.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return infrastructureState{}, nil
	}
	if err != nil {
		return infrastructureState{}, fmt.Errorf("inspect infrastructure state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return infrastructureState{}, fmt.Errorf("infrastructure state is not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return infrastructureState{}, fmt.Errorf("read infrastructure state: %w", err)
	}
	var state infrastructureState
	if err := json.Unmarshal(data, &state); err != nil {
		return infrastructureState{}, fmt.Errorf("decode infrastructure state: %w", err)
	}
	if state.Descriptor != infrastructureDescriptor || !filepath.IsAbs(state.Source) ||
		!validSourcePath(base, state.Source) {
		return infrastructureState{}, fmt.Errorf("invalid infrastructure state")
	}
	return state, nil
}

func writeInfrastructure(base string, state infrastructureState) error {
	if state.Descriptor != infrastructureDescriptor || !filepath.IsAbs(state.Source) ||
		!validSourcePath(base, state.Source) {
		return fmt.Errorf("invalid infrastructure state")
	}
	path := filepath.Join(base, "dotm", "infrastructure.json")
	if err := durableMkdir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return durableReplace(path, append(data, '\n'), 0o600)
}
