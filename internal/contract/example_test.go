package contract

import "testing"

func TestExampleProfileSelectionIsComplete(t *testing.T) {
	p, err := LoadProfile("../../examples/profile.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.EffectiveSelection().Problems(); len(probs) != 0 {
		t.Fatalf("the shipped example policy must be usable as is: %v", probs)
	}
}
