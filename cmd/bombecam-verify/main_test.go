package main

import (
	"os"
	"reflect"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	key := "TEST_VERIFY_ENV_VAR"
	os.Unsetenv(key)
	if val := getEnvOrDefault(key, "default_val"); val != "default_val" {
		t.Fatalf("expected default_val, got %q", val)
	}

	os.Setenv(key, "my_val")
	defer os.Unsetenv(key)
	if val := getEnvOrDefault(key, "default_val"); val != "my_val" {
		t.Fatalf("expected my_val, got %q", val)
	}
}

func TestSubjectMACs(t *testing.T) {
	camMAC := "aa:bb:cc:dd:ee:01, aa:bb:cc:dd:ee:02"
	canaryMAC := "11:22:33:44:55:66"

	subjects := subjectMACs(camMAC, canaryMAC)
	expected := []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "11:22:33:44:55:66"}
	if !reflect.DeepEqual(subjects, expected) {
		t.Fatalf("expected %v, got %v", expected, subjects)
	}
}
