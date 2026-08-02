// qkserve runs QK headless: it opens a shoot folder and serves the culling
// UI over the LAN — the same remote-session server the desktop app embeds,
// usable from any machine with Go (and the vehicle for end-to-end tests).
//
//	qkserve /Volumes/CARD/DCIM/100MSDCF
//	qkserve -demo            # synthetic shoot, for trying the UI
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/shaumik/qk-photo-viewer/internal/preview/previewtest"
	"github.com/shaumik/qk-photo-viewer/internal/remote"
	"github.com/shaumik/qk-photo-viewer/internal/server"
)

func main() {
	demo := flag.Bool("demo", false, "serve a synthetic demo shoot instead of a real folder")
	frontend := flag.String("frontend", "frontend", "path to the frontend directory")
	flag.Parse()

	dir := flag.Arg(0)
	if *demo {
		var err error
		if dir, err = makeDemoShoot(); err != nil {
			log.Fatal(err)
		}
	}
	if dir == "" {
		log.Fatal("usage: qkserve <shoot folder>  (or -demo)")
	}

	svc := server.New()
	res, err := svc.OpenFolder(dir)
	if err != nil {
		log.Fatal(err)
	}
	rs, err := remote.Start(svc.Handler(), os.DirFS(*frontend))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("QK serving %d photos from %s\n", len(res.Photos), dir)
	fmt.Printf("URL: %s\n", rs.URL)
	select {} // Ctrl+C to stop
}

func makeDemoShoot() (string, error) {
	dir, err := os.MkdirTemp("", "qk-demo-*")
	if err != nil {
		return "", err
	}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("DSC%05d.ARW", 4810+i)
		err := previewtest.WriteARW(filepath.Join(dir, name),
			previewtest.RealJPEG(96, 64, i), previewtest.RealJPEG(1200, 800, i))
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}
