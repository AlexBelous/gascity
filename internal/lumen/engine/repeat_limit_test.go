package engine

import "testing"

func TestRepeatLimitForDialect(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		dialect string
		want    int
	}{
		{
			name:    "current default",
			dialect: SemanticDialectCurrent,
			want:    DefaultRepeatLimit,
		},
		{
			name:    "current host override",
			opts:    Options{RepeatLimit: 7},
			dialect: SemanticDialectCurrent,
			want:    7,
		},
		{
			name:    "legacy default",
			dialect: SemanticDialectLegacy,
			want:    legacyRepeatLimit,
		},
		{
			name:    "legacy replay ignores new override",
			opts:    Options{RepeatLimit: 7},
			dialect: SemanticDialectLegacy,
			want:    legacyRepeatLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repeatLimitForDialect(tt.opts, tt.dialect)
			if err != nil {
				t.Fatalf("repeatLimitForDialect: %v", err)
			}
			if got != tt.want {
				t.Fatalf("repeatLimitForDialect() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConfiguredRepeatLimitRejectsNegative(t *testing.T) {
	if _, err := configuredRepeatLimit(Options{RepeatLimit: -1}); err == nil {
		t.Fatal("configuredRepeatLimit(-1) succeeded, want error")
	}
}
