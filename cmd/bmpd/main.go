// Command bmpd is BMP's server: it keeps each user's songs, playlists and
// play counts, and serves them to BMP and to Subsonic apps on phones.
//
//	bmpd adduser <name>          create a user and print its password
//	bmpd -domain music.example   serve HTTPS with a Let's Encrypt certificate
//	bmpd                         serve plain HTTP on -listen, for testing at home
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"bmp/internal/server"
)

func main() {
	data := flag.String("data", "data", "folder holding users.json and each user's music")
	domain := flag.String("domain", "", "domain to serve over HTTPS on ports 443 and 80")
	listen := flag.String("listen", ":4533", "address to serve plain HTTP on when there is no domain")
	flag.Parse()

	if flag.Arg(0) == "adduser" {
		if flag.NArg() != 2 {
			log.Fatal("usage: bmpd adduser <name>")
		}
		password, apiKey, err := server.AddUser(*data, flag.Arg(1))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user:     %s\npassword: %s\nAPI key:  %s\n\nThey are shown only once. Restart bmpd to let the user in.\n", flag.Arg(1), password, apiKey)
		return
	}

	shared, err := server.New(*data)
	if err != nil {
		log.Fatal(err)
	}
	defer shared.Close()
	if shared.Users() == 0 {
		log.Printf("no users yet; add one with: bmpd -data %s adduser <name>", *data)
	}
	web := &http.Server{Handler: shared.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = web.Shutdown(shutdown)
	}()

	if *domain == "" {
		web.Addr = *listen
		log.Printf("serving http on %s", *listen)
		err = web.ListenAndServe()
	} else {
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      autocert.DirCache(filepath.Join(*data, "certs")),
		}
		// Port 80 answers Let's Encrypt's checks and sends everyone else to HTTPS.
		go func() { log.Print(http.ListenAndServe(":80", manager.HTTPHandler(nil))) }()
		web.Addr, web.TLSConfig = ":443", manager.TLSConfig()
		log.Printf("serving https://%s", *domain)
		err = web.ListenAndServeTLS("", "")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
