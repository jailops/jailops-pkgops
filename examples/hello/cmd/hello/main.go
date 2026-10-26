// hello is a tiny daemon used to demonstrate pkgops: it answers HTTP on
// -listen with a greeting read from the config file.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "listen address")
	conf := flag.String("c", "/usr/local/etc/hello/hello.conf", "config file (single line: greeting)")
	ver := flag.Bool("version", false, "print version")
	flag.Parse()
	if *ver {
		fmt.Println("hello", version)
		return
	}
	greeting := "hello from FreeBSD"
	if b, err := os.ReadFile(*conf); err == nil {
		greeting = strings.TrimSpace(string(b))
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, greeting) })
	log.Printf("hello %s listening on %s", version, *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
