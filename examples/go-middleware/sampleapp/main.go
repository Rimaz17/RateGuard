// Command sampleapp is an ordinary net/http service protected by RateGuard.
// The integration is the five lines after the RateGuard comment; everything
// else is the app.
//
//	RATEGUARD_URL=http://127.0.0.1:8080 go run ./sampleapp
//	curl -i -H "Authorization: Bearer $KEY" http://127.0.0.1:8000/api/products
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	rateguard "github.com/Rimaz17/RateGuard/examples/go-middleware"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/products", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"name":"teapot"}]`+"\n")
	})

	// RateGuard: these five lines are the whole integration.
	rg, err := rateguard.New(os.Getenv("RATEGUARD_URL"))
	if err != nil {
		log.Fatal(err)
	}
	handler := rg.Middleware(mux)

	srv := &http.Server{Addr: ":8000", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
