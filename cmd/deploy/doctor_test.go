package main

import "testing"

func TestDoctorParseDiskFreeBytesRejectsMalformedOutput(t *testing.T) {
	if _, err := doctorParseDiskFreeBytes("not-a-number\n"); err == nil {
		t.Fatal("malformed df output must be rejected")
	}
}

func TestDoctorParseDiskFreeBytesAcceptsPositiveBytes(t *testing.T) {
	got, err := doctorParseDiskFreeBytes("2147483648\n")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if got != 2147483648 {
		t.Fatalf("free bytes = %d", got)
	}
}
