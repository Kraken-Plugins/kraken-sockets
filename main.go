package main

import (
	"flag"

	"github.com/cbartram/kraken-sockets/server"
)

func main() {
	var host, port string
	flag.StringVar(&port, "port", "8080", "port to listen on")
	flag.StringVar(&host, "host", "0.0.0.0", "host to listen on")
	flag.Parse()

	server.Listen(host, port)
}
