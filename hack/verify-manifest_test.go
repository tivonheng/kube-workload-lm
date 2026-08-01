package main

import (
	"strings"
	"testing"
)

func TestRequiredPlatformsAcceptsAMD64AndARM64(t *testing.T) {
	input := `{"manifests":[
		{"platform":{"os":"linux","architecture":"arm64"}},
		{"platform":{"os":"linux","architecture":"amd64"}}
	]}`
	if err := requiredPlatforms(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredPlatformsRejectsMissingOrInvalidManifest(t *testing.T) {
	for name, input := range map[string]string{
		"missing-arm64": `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}`,
		"invalid-json":  `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := requiredPlatforms(strings.NewReader(input)); err == nil {
				t.Fatal("expected manifest validation error")
			}
		})
	}
}
