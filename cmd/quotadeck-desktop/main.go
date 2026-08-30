//go:build desktop

package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"strings"

	"github.com/devthefuture-org/quotadeck/internal/application"
	"github.com/devthefuture-org/quotadeck/internal/config"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

var version = "dev"

//go:embed appicon.png
var appIcon []byte

type desktopApp struct {
	runtime *application.Runtime
}

func main() {
	applyLinuxWebKitFixes()

	configPath := config.DefaultPath()
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("quotadeck-desktop: load configuration: %v", err)
	}
	appRuntime, err := application.New(cfg, configPath, version)
	if err != nil {
		log.Fatalf("quotadeck-desktop: start runtime: %v", err)
	}
	defer appRuntime.Close()
	app := &desktopApp{runtime: appRuntime}

	err = wails.Run(&options.App{
		Title:     "QuotaDeck",
		Width:     1280,
		Height:    840,
		MinWidth:  760,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets:  nil,
			Handler: trustedDesktopHandler(appRuntime.Handler()),
		},
		EnableDefaultContextMenu: true,
		BackgroundColour:         &options.RGBA{R: 17, G: 19, B: 15, A: 255},
		Linux: &linux.Options{
			Icon:        appIcon,
			ProgramName: "quotadeck",
		},
		OnStartup:  app.onStartup,
		OnShutdown: app.onShutdown,
	})
	if err != nil {
		log.Fatalf("quotadeck-desktop: wails run failed: %v", err)
	}
}

func (a *desktopApp) onStartup(ctx context.Context) {
	a.runtime.Start(ctx)
}

func (a *desktopApp) onShutdown(_ context.Context) {
	if err := a.runtime.Close(); err != nil {
		log.Printf("quotadeck-desktop: close runtime: %v", err)
	}
}

// Wails invokes the application's handler in-process, without a TCP peer.
// Normalise those trusted requests to loopback so the existing local-only
// refresh guard keeps the exact same policy as the CLI server.
func trustedDesktopHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		clone := request.Clone(request.Context())
		clone.RemoteAddr = "127.0.0.1:0"
		origin := clone.Header.Get("Origin")
		if strings.HasPrefix(origin, "wails://") || origin == "http://wails.localhost" {
			clone.Header = clone.Header.Clone()
			clone.Header.Del("Origin")
		}
		next.ServeHTTP(writer, clone)
	})
}
