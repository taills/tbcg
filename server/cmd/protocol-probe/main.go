package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopped)
	if err := runProbe(os.Getenv, stopped); err != nil {
		log.Fatal(err)
	}
}
