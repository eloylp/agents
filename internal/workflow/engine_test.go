package workflow

import (
	"reflect"
	"testing"
)

func TestUniqueStrings(t *testing.T) {
	input := []string{"claude", "openai", "claude", "openai", "claude"}
	got := uniqueStrings(input)
	want := []string{"claude", "openai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueStrings() = %v, want %v", got, want)
	}
}
