package main

import (
	"strings"
	"testing"
)

func TestReadPassword(t *testing.T) {
	for _, input := range []string{"customer-password\n", "customer-password\r\n"} {
		got, err := readPassword(strings.NewReader(input))
		if err != nil {
			t.Fatalf("readPassword(%q): %v", input, err)
		}
		if got != "customer-password" {
			t.Fatalf("readPassword(%q) = %q", input, got)
		}
	}
}

func TestReadPasswordRejectsInvalidInput(t *testing.T) {
	for name, input := range map[string]string{
		"missing newline": "customer-password",
		"trailing data":   "customer-password\nnot-whitespace",
		"too long":        strings.Repeat("x", 1025) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readPassword(strings.NewReader(input)); err == nil {
				t.Fatal("readPassword accepted invalid input")
			}
		})
	}
}
