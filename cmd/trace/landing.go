package main

import (
	_ "embed"
	"net/http"
)

//go:embed landing.html
var landingHTML []byte

//go:embed assets/InterVariable.woff2
var interFont []byte

//go:embed assets/SpaceGrotesk-Variable.ttf
var spaceGroteskFont []byte

//go:embed assets/OFL-Inter.txt
var interLicense []byte

//go:embed assets/trace-mark.svg
var traceMark []byte

//go:embed assets/trace-network-hero.webp
var heroBackground []byte

func (a *app) landing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(landingHTML)
}

func (a *app) font(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(interFont)
}

func (a *app) displayFont(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(spaceGroteskFont)
}

func (a *app) fontLicense(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(interLicense)
}

func (a *app) logo(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(traceMark)
}

func (a *app) heroImage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(heroBackground)
}
