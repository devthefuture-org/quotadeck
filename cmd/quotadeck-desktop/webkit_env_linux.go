//go:build desktop && linux

package main

import (
	"log"
	"os"
)

func applyLinuxWebKitFixes() {
	if _, set := os.LookupEnv("WEBKIT_DISABLE_DMABUF_RENDERER"); set {
		return
	}
	if err := os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1"); err != nil {
		log.Printf("quotadeck-desktop: could not set WEBKIT_DISABLE_DMABUF_RENDERER: %v", err)
	}
}
