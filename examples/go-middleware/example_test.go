package rateguard_test

import (
	"log"
	"net/http"

	rateguard "github.com/Rimaz17/RateGuard/examples/go-middleware"
)

// Protecting a service takes a client and one wrapped handler.
func ExampleClient_Middleware() {
	mux := http.NewServeMux() // your application

	rg, err := rateguard.New("http://127.0.0.1:8080")
	if err != nil {
		log.Fatal(err)
	}
	protected := rg.Middleware(mux)

	_ = protected // serve it as usual: http.ListenAndServe(":8000", protected)
}

// A sidecar is reached over its Unix socket, with no TCP hop.
func ExampleNew_sidecar() {
	rg, err := rateguard.New("unix:///run/rateguard/rateguard.sock")
	if err != nil {
		log.Fatal(err)
	}
	_ = rg
}
