package npm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type manifest struct {
	Engines        map[string]string `json:"engines"`
	PackageManager string            `json:"packageManager"` //nolint:tagliatelle // npm's field name
}

func readManifest(tree, file string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(file))) //nolint:gosec // a manifest of the tree
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil //nolint:nilnil // no manifest is not an error
	}

	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, file, err)
	}

	return &m, nil
}
