package main

import (
	"reflect"
	"testing"
)

func TestReorderArgs(t *testing.T) {
	flagsWithArgs := map[string]bool{
		"period": true,
		"year":   true,
		"unit":   true,
	}

	// Case 1: directory before flags
	args1 := []string{"/my/backup", "--period", "year", "--unit", "mi"}
	expected1 := []string{"--period", "year", "--unit", "mi", "/my/backup"}
	got1 := reorderArgs(flagsWithArgs, args1)
	if !reflect.DeepEqual(got1, expected1) {
		t.Errorf("got %v, want %v", got1, expected1)
	}

	// Case 2: flags before directory
	args2 := []string{"--period", "week", "/my/backup"}
	expected2 := []string{"--period", "week", "/my/backup"}
	got2 := reorderArgs(flagsWithArgs, args2)
	if !reflect.DeepEqual(got2, expected2) {
		t.Errorf("got %v, want %v", got2, expected2)
	}

	// Case 3: boolean flag mixed in
	args3 := []string{"/my/backup", "--all-years", "--period", "year"}
	expected3 := []string{"--all-years", "--period", "year", "/my/backup"}
	got3 := reorderArgs(flagsWithArgs, args3)
	if !reflect.DeepEqual(got3, expected3) {
		t.Errorf("got %v, want %v", got3, expected3)
	}

	// Case 4: --flag=value format
	args4 := []string{"/my/backup", "--period=year"}
	expected4 := []string{"--period=year", "/my/backup"}
	got4 := reorderArgs(flagsWithArgs, args4)
	if !reflect.DeepEqual(got4, expected4) {
		t.Errorf("got %v, want %v", got4, expected4)
	}
}
