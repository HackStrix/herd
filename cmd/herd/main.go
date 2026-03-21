package main

import (
	"log"
	"os"
)

func main() {
	if err := Execute(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
