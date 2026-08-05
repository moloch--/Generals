//go:build !windows

package main

func prepareDesktopGUI() {}

func prepareArgumentConsole() error { return nil }
