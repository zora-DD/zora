package agenttools

import (
	"math"
	"testing"
)

func TestCalculate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		expression string
		want       float64
		wantError  bool
	}{
		{name: "precedence", expression: "2 + 3 * 4", want: 14},
		{name: "parentheses", expression: "(128 + 72) * 3.5", want: 700},
		{name: "unary", expression: "-4 * (2 + 1)", want: -12},
		{name: "decimal", expression: ".5 + 1.25", want: 1.75},
		{name: "division by zero", expression: "3 / 0", wantError: true},
		{name: "invalid token", expression: "2 + os.Exit(1)", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calculate(tt.expression)
			if tt.wantError {
				if err == nil {
					t.Fatalf("calculate(%q) expected error", tt.expression)
				}
				return
			}
			if err != nil {
				t.Fatalf("calculate(%q): %v", tt.expression, err)
			}
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("calculate(%q) = %v, want %v", tt.expression, got, tt.want)
			}
		})
	}
}
