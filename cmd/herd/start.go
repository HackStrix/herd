package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hackstrix/herd"
	"github.com/spf13/cobra"
)

var (
	httpPort   int
	socketPath string
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the herd daemon",
	Long:  `Starts the herd daemon, exposing a gRPC Control Plane on a Unix socket and an HTTP Data Plane proxy.`,
	Run: func(cmd *cobra.Command, args []string) {
		runDaemon()
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().IntVarP(&httpPort, "http-port", "p", 8080, "Port for the HTTP Data Plane proxy")
	startCmd.Flags().StringVarP(&socketPath, "socket-path", "s", "/tmp/herd.sock", "Path to the Unix socket for the gRPC Control Plane")
}

func runDaemon() {
	// 1. Setup graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("Starting herd daemon...")

	// 2. Initialize the Pool
	// Note: We use a placeholder factory setup for now. This will be replaced by actual configuration in future phases.
	factory := herd.NewProcessFactory("echo", "placeholder") 
	
	// Ensure we don't error out on unsupported configurations
	factory.WithStartTimeout(0) 

	pool, err := herd.New(factory)
	if err != nil {
		log.Fatalf("failed to initialize pool: %v", err)
	}

	defer func() {
		log.Println("Shutting down pool...")
		// Will assume Shutdown(context.Background()) exists, we'll verify this during compilation check.
		// If Pool does not have Shutdown, we'll update this. 
		err := pool.Shutdown(context.Background())
		if err != nil {
			log.Println("Error shutting down pool:", err)
		}
		log.Println("Daemon gracefully stopped.")
	}()

	log.Printf("HTTP proxy will listen on :%d", httpPort)
	log.Printf("gRPC socket path will be: %s", socketPath)

	log.Println("Daemon is running. Press CTRL+C to stop.")

	// Block until signal is received
	<-ctx.Done()
	log.Println("Received shutdown signal. Gracefully stopping...")
}
