package main

import (
	"reflect"
	"testing"
)

func TestParseMMRLambdasEmpty(t *testing.T) {
	got, err := parseMMRLambdas("")
	if err != nil {
		t.Fatalf("parseMMRLambdas(\"\") error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("parseMMRLambdas(\"\") = %v, want nil (no MMR modes)", got)
	}
}

func TestParseMMRLambdasValidAscending(t *testing.T) {
	got, err := parseMMRLambdas("0.9,0.5,0.7")
	if err != nil {
		t.Fatalf("parseMMRLambdas: %v", err)
	}
	want := []float64{0.5, 0.7, 0.9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMMRLambdas(\"0.9,0.5,0.7\") = %v, want %v (sorted ascending)", got, want)
	}
}

func TestParseMMRLambdasBoundaryOne(t *testing.T) {
	got, err := parseMMRLambdas("1")
	if err != nil {
		t.Fatalf("parseMMRLambdas(\"1\"): %v", err)
	}
	if want := []float64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMMRLambdas(\"1\") = %v, want %v", got, want)
	}
}

func TestParseMMRLambdasRejectsOutOfRangeOrJunk(t *testing.T) {
	for _, spec := range []string{"0", "-0.5", "1.5", "abc", "0.5,abc"} {
		if _, err := parseMMRLambdas(spec); err == nil {
			t.Errorf("parseMMRLambdas(%q) expected error, got nil", spec)
		}
	}
}
