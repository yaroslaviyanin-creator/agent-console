//go:build !linux

package main

func restrictProcessInspection() error { return nil }
