package policy

import (
	"strings"
	"testing"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

func TestCompileNamePatterns(t *testing.T) {
	tests := []struct {
		name        string
		input       *[]string
		wantNil     bool
		wantErr     string
		wantMatches map[string]bool // pattern output tested against these strings
	}{
		{
			name:    "nil input returns nil",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "empty slice returns nil",
			input:   &[]string{},
			wantNil: true,
		},
		{
			name:  "valid patterns compile and match expected strings",
			input: &[]string{`pfb\d+`, `staging-.*`},
			wantMatches: map[string]bool{
				"pfb123":        true,
				"pfb0":          true,
				"staging-app":   true,
				"staging-":      true,
				"my-pfb123":     false,
				"pfb123-extra":  false,
				"production-db": false,
			},
		},
		{
			name:    "empty string pattern returns error",
			input:   &[]string{"valid", ""},
			wantErr: "pattern must be non-empty",
		},
		{
			name:    "invalid regex returns error",
			input:   &[]string{`[invalid`},
			wantErr: "[invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, patterns, err := compileNamePatterns(tt.input)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if compiled != nil || patterns != nil {
					t.Fatalf("expected nil results, got compiled=%v patterns=%v", compiled, patterns)
				}
				return
			}

			if len(compiled) != len(*tt.input) {
				t.Fatalf("expected %d compiled patterns, got %d", len(*tt.input), len(compiled))
			}

			for input, shouldMatch := range tt.wantMatches {
				matched := false
				for _, re := range compiled {
					if re.MatchString(input) {
						matched = true
						break
					}
				}
				if matched != shouldMatch {
					t.Errorf("input %q: got match=%v, want match=%v", input, matched, shouldMatch)
				}
			}
		})
	}
}

func TestCompileTargetSelectorOptional(t *testing.T) {
	baseKinds := &[]workload.Kind{workload.KindDeployment}

	tests := []struct {
		name         string
		target       *rawTarget
		wantErr      string
		wantSelector bool // true if compiledSelector should be non-nil
	}{
		{
			name: "namePatterns only without selector succeeds",
			target: &rawTarget{
				Kinds:        baseKinds,
				NamePatterns: &[]string{`pfb\d+`},
			},
			wantSelector: false,
		},
		{
			name: "selector only without namePatterns succeeds",
			target: &rawTarget{
				Kinds: baseKinds,
				Selector: &rawSelector{
					MatchLabels: map[string]string{"app": "demo"},
				},
			},
			wantSelector: true,
		},
		{
			name: "both selector and namePatterns succeeds",
			target: &rawTarget{
				Kinds: baseKinds,
				Selector: &rawSelector{
					MatchLabels: map[string]string{"app": "demo"},
				},
				NamePatterns: &[]string{`staging-.*`},
			},
			wantSelector: true,
		},
		{
			name: "neither selector nor namePatterns returns error",
			target: &rawTarget{
				Kinds: baseKinds,
			},
			wantErr: "at least one of selector or namePatterns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := compileTarget(tt.target)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantSelector && compiled.compiledSelector == nil {
				t.Fatal("expected non-nil compiledSelector")
			}
			if !tt.wantSelector && compiled.compiledSelector != nil {
				t.Fatal("expected nil compiledSelector")
			}
		})
	}
}
