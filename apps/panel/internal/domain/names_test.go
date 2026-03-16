package domain

import "testing"

func TestValidateCollectorName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "valid", value: "collector-1"},
		{name: "required", value: "   ", wantErr: true},
		{name: "surrounding whitespace", value: " collector-1 ", wantErr: true},
		{name: "too long", value: string(make([]byte, maxCollectorNameLength+1)), wantErr: true},
		{name: "invalid character", value: "collector/name", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateCollectorName(tt.value)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.value, err)
			}
		})
	}
}

func TestValidateAppName(t *testing.T) {
	t.Parallel()

	if err := ValidateAppName("app.main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateNotifierName(t *testing.T) {
	t.Parallel()

	if err := ValidateNotifierName(" "); err == nil {
		t.Fatal("expected error for empty notifier name")
	}
}

func TestValidateAPIKeyName(t *testing.T) {
	t.Parallel()

	if err := ValidateAPIKeyName("primary key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateAPIKeyName(" primary key "); err == nil {
		t.Fatal("expected error for surrounding whitespace")
	}
}

func TestValidateUserID(t *testing.T) {
	t.Parallel()

	if err := ValidateUserID(0); err == nil {
		t.Fatal("expected error for zero user ID")
	}
	if err := ValidateUserID(1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
