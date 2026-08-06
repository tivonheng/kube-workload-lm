package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type manifestList struct {
	Manifests []struct {
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

func requiredPlatforms(reader io.Reader) error {
	var list manifestList
	if err := json.NewDecoder(reader).Decode(&list); err != nil {
		return fmt.Errorf("decode manifest inspection: %w", err)
	}
	found := map[string]bool{}
	for _, item := range list.Manifests {
		found[item.Platform.OS+"/"+item.Platform.Architecture] = true
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if !found[platform] {
			return fmt.Errorf("manifest is missing required platform %s", platform)
		}
	}
	return nil
}

func main() {
	if err := requiredPlatforms(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
