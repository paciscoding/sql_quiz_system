package services

import (
	"encoding/json"
	"testing"
)

func TestValuesEqual(t *testing.T) {
	tests := []struct {
		name string
		a    any
		b    any
		want bool
	}{
		{name: "int equals float same magnitude", a: int64(1), b: float64(1.0), want: true},
		{name: "int equals int literal float", a: 1, b: 1.0, want: true},
		{name: "string equals string", a: "1", b: "1", want: true},
		{name: "float within epsilon", a: 1.0000000001, b: 1.0, want: true},
		{name: "nil equals nil", a: nil, b: nil, want: true},
		{name: "nil vs zero not equal", a: nil, b: 0, want: false},
		{name: "abc equals abc", a: "abc", b: "abc", want: true},
		{name: "bytes equal string", a: []byte("x"), b: "x", want: true},
		{name: "bool true equals true", a: true, b: true, want: true},
		{name: "bool true vs string true fallback equal", a: true, b: "true", want: true},
		{name: "two vs three not equal", a: 2, b: 3, want: false},
		// extra coverage
		{name: "float32 equals float64", a: float32(2.5), b: float64(2.5), want: true},
		{name: "json.Number equals int", a: json.Number("5"), b: 5, want: true},
		{name: "float beyond epsilon not equal", a: 1.00001, b: 1.0, want: false},
		{name: "bool false vs true not equal", a: false, b: true, want: false},
		{name: "one nil not equal", a: 1, b: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := valuesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("valuesEqual(%#v, %#v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			// valuesEqual should be symmetric.
			if got := valuesEqual(tt.b, tt.a); got != tt.want {
				t.Errorf("valuesEqual(%#v, %#v) [reversed] = %v, want %v", tt.b, tt.a, got, tt.want)
			}
		})
	}
}
